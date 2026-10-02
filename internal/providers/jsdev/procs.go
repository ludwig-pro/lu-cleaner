package jsdev

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

const cmdTimeout = 5 * time.Second

// procSnapshot is a read-only picture of the running processes of the user,
// used to never propose something a live process depends on.
type procSnapshot struct {
	// ok is false when the process list could not be read: callers that need
	// it to decide what is unused must then propose nothing (fail closed).
	ok bool
	// args holds one command line per process (arguments only).
	args []string
	// envText is `ps -E` output: command lines followed by each process'
	// initial environment (PATH, FNM_MULTISHELL_PATH...).
	envText string
	// bins are the executables (lsof txt) of running `node` processes.
	bins []string
	// starts are the start times of every running process.
	starts []time.Time
}

// output runs a read-only command with a timeout. Partial stdout is returned
// even when the command exits non-zero (lsof does that routinely).
func output(ctx context.Context, env *core.Env, dir string, timeout time.Duration, name string, args ...string) ([]byte, error) {
	return env.OutputTimeout(ctx, timeout, dir, name, args...)
}

func loadProcs(ctx context.Context, env *core.Env) *procSnapshot {
	ps := &procSnapshot{}
	out, err := output(ctx, env, "", cmdTimeout, "/bin/ps", "-axww", "-o", "args=")
	if err != nil || len(out) == 0 {
		return ps
	}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ps.args = append(ps.args, l)
		}
	}
	envOut, err := output(ctx, env, "", cmdTimeout, "/bin/ps", "-axwwE", "-o", "command=")
	if err != nil || len(envOut) == 0 {
		return ps
	}
	ps.envText = string(envOut)
	et, err := output(ctx, env, "", cmdTimeout, "/bin/ps", "-axo", "etime=")
	if err != nil || len(et) == 0 {
		return ps
	}
	for _, l := range strings.Split(string(et), "\n") {
		if d, ok := parseEtime(l); ok {
			ps.starts = append(ps.starts, env.Now.Add(-d))
		}
	}
	ps.ok = true

	// Executables of running node processes (the real binary, even when the
	// process was started through a shim or a PATH lookup).
	lo, _ := output(ctx, env, "", cmdTimeout, "/usr/sbin/lsof", "-nP", "-w", "-c", "node", "-a", "-d", "txt", "-Fn")
	for _, l := range strings.Split(string(lo), "\n") {
		if strings.HasPrefix(l, "n/") {
			ps.bins = append(ps.bins, l[1:])
		}
	}
	return ps
}

// parseEtime parses ps etime: [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var days int
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	fields := strings.Split(s, ":")
	if len(fields) < 2 || len(fields) > 3 {
		return 0, false
	}
	var secs int
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, true
}

// usesDir counts running processes whose command line or executable lives
// inside dir.
func (ps *procSnapshot) usesDir(dir string) int {
	if ps == nil || dir == "" {
		return 0
	}
	needle := strings.TrimSuffix(dir, "/") + "/"
	n := 0
	for _, a := range ps.args {
		if strings.Contains(a, needle) {
			n++
		}
	}
	for _, b := range ps.bins {
		if strings.HasPrefix(b, needle) {
			n++
		}
	}
	return n
}

var multishellRef = regexp.MustCompile(`fnm_multishells/([0-9]+_[0-9]+)`)

// multishellRefs returns the fnm multishell entry names referenced by any
// running process (command line or initial environment: PATH, FNM_MULTISHELL_PATH).
func (ps *procSnapshot) multishellRefs() map[string]bool {
	refs := map[string]bool{}
	if ps == nil {
		return refs
	}
	for _, m := range multishellRef.FindAllStringSubmatch(ps.envText, -1) {
		refs[m[1]] = true
	}
	for _, a := range ps.args {
		for _, m := range multishellRef.FindAllStringSubmatch(a, -1) {
			refs[m[1]] = true
		}
	}
	return refs
}

// startedAround reports whether a live process started in [t-before, t+after].
func (ps *procSnapshot) startedAround(t time.Time, before, after time.Duration) bool {
	if ps == nil {
		return false
	}
	lo, hi := t.Add(-before), t.Add(after)
	for _, s := range ps.starts {
		if !s.Before(lo) && !s.After(hi) {
			return true
		}
	}
	return false
}
