// Package update offers bounded, optional updates of directly installed releases.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/statefile"
	"golang.org/x/sys/unix"
)

type Offer struct {
	Current, Next, Target string
	GoInstall             bool
}

type Prompt interface {
	Confirm(context.Context, Offer) (bool, error)
	Notice(string)
}

type Options struct{ Version, StateDir string }

type Service struct {
	options        Options
	client         *http.Client
	now            func() time.Time
	executable     func() (string, error)
	buildInfo      func() (*debug.BuildInfo, bool)
	supported      func() bool
	lookupTimeout  time.Duration
	installTimeout time.Duration
}

func New(options Options) *Service {
	return &Service{options: options, client: newHTTPClient(), now: time.Now, executable: os.Executable, buildInfo: debug.ReadBuildInfo,
		supported: func() bool {
			return runtime.GOOS == "darwin" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64") && os.Geteuid() != 0
		},
		lookupTimeout: 1500 * time.Millisecond, installTimeout: time.Minute}
}

type attemptState struct {
	Schema         int       `json:"schema"`
	LastAttemptUTC time.Time `json:"last_attempt_utc"`
}

func reserveAttempt(dir *statefile.Dir, now time.Time) bool {
	unlock, err := dir.Lock("attempt.lock")
	if err != nil {
		return false
	}
	defer unlock()
	f, err := dir.Open("attempt.json", unix.O_RDONLY)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		var state attemptState
		if readErr != nil || len(data) > 4096 || json.Unmarshal(data, &state) != nil || state.Schema != 1 || state.LastAttemptUTC.IsZero() || now.Before(state.LastAttemptUTC.Add(24*time.Hour)) {
			return false
		}
	}
	data, err := json.Marshal(attemptState{Schema: 1, LastAttemptUTC: now.UTC()})
	return err == nil && dir.Put("attempt.json", data) == nil && dir.Sync() == nil
}

// Run never makes ordinary command execution depend on the network or an update.
// Parent cancellation is the only error returned to its caller.
func (s *Service) Run(ctx context.Context, prompt Prompt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.supported() {
		return nil
	}
	info, _ := s.buildInfo()
	current, ok := installedVersion(s.options.Version, info)
	if !ok {
		return nil
	}
	path, err := s.executable()
	if err != nil {
		return nil
	}
	target, err := inspectTarget(path)
	if err != nil {
		return nil
	}
	defer target.close()
	dir, err := statefile.OpenDir(s.options.StateDir)
	if err != nil {
		return nil
	}
	defer dir.Close()
	if !reserveAttempt(dir, s.now()) {
		return ctx.Err()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, s.lookupTimeout)
	r, err := latestStable(lookupCtx, s.client)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !r.version.newer(current) {
		return nil
	}
	unlock, err := dir.Lock("install.lock")
	if err != nil {
		return nil
	}
	defer unlock()
	if target.unchanged() != nil {
		return nil
	}
	yes, err := prompt.Confirm(ctx, Offer{Current: current.String(), Next: r.version.String(), Target: target.path, GoInstall: s.options.Version == "dev"})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !yes {
		return nil
	}
	installCtx, cancel := context.WithTimeout(ctx, s.installTimeout)
	defer cancel()
	if err = target.unchanged(); err != nil {
		prompt.Notice("warning: update skipped because the executable changed")
		return nil
	}
	candidate, err := stageVerified(installCtx, s.client, r, target)
	if candidate != nil {
		defer target.remove(candidate)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		prompt.Notice("warning: update could not be verified; continuing with " + current.String())
		return nil
	}
	backup, committed, err := commit(installCtx, target, candidate)
	if committed {
		prompt.Notice(fmt.Sprintf("Updated to %s. Backup: %s", r.version, backup))
		prompt.Notice(fmt.Sprintf("This invocation continues with %s; the next invocation uses %s.", current, r.version))
		if err != nil {
			prompt.Notice("warning: update installed but directory synchronization failed; keep the backup")
		}
	} else if ctx.Err() == nil {
		prompt.Notice("warning: update could not be installed; continuing with " + current.String())
	}
	return ctx.Err()
}
