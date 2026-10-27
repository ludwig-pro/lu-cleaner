//go:build darwin && cgo

package clean

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

type gitPriorityEvent struct {
	Args       []string
	Background int
}

func TestMain(m *testing.M) {
	if os.Getenv("LU_PRIORITY_INCOMPATIBLE_HELPER") != "" && len(os.Args) > 1 && os.Args[1] == "--internal-worktree-priority-v1" {
		f, err := os.OpenFile(os.Getenv("LU_PRIORITY_INCOMPATIBLE_HELPER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(85)
		}
		_, _ = fmt.Fprintln(f, "helper started")
		f.Close()
		os.Exit(0)
	}
	if handled, code := core.RunPrivateWorktreeHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	if realGit := os.Getenv("LU_PRIORITY_REAL_GIT"); realGit != "" && filepath.Base(os.Args[0]) == "git" {
		bg, err := unix.Getpriority(4, 0)
		if err != nil {
			os.Exit(81)
		}
		if slices.Contains(os.Args, "worktree") && slices.Contains(os.Args, "remove") {
			fd, err := unix.Open(os.Getenv("LU_PRIORITY_GIT_LOCK"), unix.O_CREAT|unix.O_RDWR, 0o600)
			if err != nil || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
				fmt.Fprintln(os.Stderr, "concurrent worktree remove detected")
				os.Exit(82)
			}
		}
		f, err := os.OpenFile(os.Getenv("LU_PRIORITY_GIT_EVENTS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(83)
		}
		_ = json.NewEncoder(f).Encode(gitPriorityEvent{Args: os.Args[1:], Background: bg})
		f.Close()
		if err := syscall.Exec(realGit, os.Args, os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(84)
		}
	}
	os.Exit(m.Run())
}

func TestPriorityWorktreeRemovalInRealRepositories(t *testing.T) {
	if scenario := os.Getenv("LU_PRIORITY_CLEAN_CASE"); scenario != "" {
		priorityCleanChild(t, scenario)
		return
	}
	taskpolicy, err := exec.LookPath("taskpolicy")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"clean", "dirty", "locked", "nested-clean", "nested-dirty", "nested-incompatible-helper", "force"} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(taskpolicy, "-d", "utility", "-g", "utility", os.Args[0], "-test.run=^TestPriorityWorktreeRemovalInRealRepositories$")
			cmd.Env = append(os.Environ(), "LU_PRIORITY_CLEAN_CASE="+scenario)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("scenario %s: %v\n%s", scenario, err, out)
			}
		})
	}
}

func priorityCleanChild(t *testing.T, scenario string) {
	needGit(t)
	if err := unix.Setpriority(4, 0, 0); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	main := f.newRepo(t, "src/main", map[string]string{".gitignore": ".claude/worktrees/\n"})
	a := filepath.Join(f.home, "wt/a")
	b := filepath.Join(f.home, "wt/b")
	git(t, main, "worktree", "add", "-q", "-b", "feat-a", a)
	if strings.HasPrefix(scenario, "nested-") {
		b = filepath.Join(a, ".claude/worktrees/b")
	}
	git(t, main, "worktree", "add", "-q", "-b", "feat-b", b)
	if scenario == "dirty" || scenario == "nested-dirty" || scenario == "force" {
		if err := os.WriteFile(filepath.Join(b, "precious.txt"), []byte("fictional work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "locked" {
		git(t, main, "worktree", "lock", b)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := f.mk(t, "test-bin")
	if err := os.Symlink(os.Args[0], filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(f.home, "git-events.jsonl")
	t.Setenv("LU_PRIORITY_REAL_GIT", realGit)
	t.Setenv("LU_PRIORITY_GIT_EVENTS", events)
	t.Setenv("LU_PRIORITY_GIT_LOCK", filepath.Join(f.home, "git.lock"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	restore, permit, err := scanctl.Activate(scanctl.Limits{Mode: scanctl.Eco})
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	ctx, cancel := context.WithTimeout(scanctl.WithWorktreePermit(context.Background(), permit), 20*time.Second)
	defer cancel()
	o := f.opts()
	o.Force = scenario == "force"
	helperMarker := filepath.Join(f.home, "helper-started")
	if scenario == "nested-incompatible-helper" {
		t.Setenv("LU_PRIORITY_INCOMPATIBLE_HELPER", helperMarker)
	}
	sum := Run(ctx, []*core.Item{wtItem(a, main), wtItem(b, main)}, o, nil)
	wantDone := 2
	wantFailed := 0
	if scenario == "dirty" || scenario == "locked" {
		wantDone = 1
	}
	if scenario == "nested-dirty" {
		wantDone = 0
	}
	if scenario == "nested-incompatible-helper" {
		wantDone, wantFailed = 0, 1
	}
	if sum.Count(StatusDone) != wantDone || sum.Count(StatusFailed) != wantFailed || sum.Count(StatusSkipped) != 2-wantDone-wantFailed {
		t.Fatalf("results=%+v", sum.Results)
	}
	if scenario == "nested-clean" && sum.Results[0].Item.Path != b {
		t.Fatal("nested order changed")
	}
	if wantDone < 2 {
		mustExist(t, filepath.Join(b, "README.md"))
	}
	if scenario == "nested-dirty" {
		mustExist(t, filepath.Join(a, "README.md"))
		mustExist(t, filepath.Join(b, "precious.txt"))
	}
	if scenario == "nested-incompatible-helper" {
		mustExist(t, filepath.Join(a, "README.md"))
		child, parent := sum.Results[0], sum.Results[1]
		if child.Item.Path != b || child.Status != StatusFailed || strings.Contains(child.Error, "--force") || strings.Contains(child.Error, "Git was not started") || !strings.Contains(child.Error, "missing helper acknowledgement") {
			t.Fatalf("incompatible helper falsely accepted: %+v", child)
		}
		if parent.Item.Path != a || parent.Status != StatusSkipped || !strings.Contains(parent.Message, "was not removed") {
			t.Fatalf("parent was not protected: %+v", parent)
		}
		data, err := os.ReadFile(helperMarker)
		if err != nil || bytes.Count(data, []byte{'\n'}) != 1 {
			t.Fatalf("helper attempt count changed: %q %v", data, err)
		}
	}
	for _, branch := range []string{"feat-a", "feat-b"} {
		git(t, main, "show-ref", "--verify", "refs/heads/"+branch)
	}
	registered := strings.Count(git(t, main, "worktree", "list", "--porcelain"), "worktree ")
	if registered != 3-wantDone {
		t.Fatalf("registry has %d entries want %d", registered, 3-wantDone)
	}
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	removes, bgChecks := 0, 0
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var e gitPriorityEvent
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(e.Args, "worktree") && slices.Contains(e.Args, "remove") {
			removes++
			if e.Background != 0 {
				t.Fatalf("removal did not receive child policy: %+v", e)
			}
			path := e.Args[len(e.Args)-1]
			if !slices.Equal(e.Args, (core.WorktreeRemoval{Path: path, Force: o.Force}).Args()) {
				t.Fatalf("Git args changed: %v", e.Args)
			}
		} else if slices.Contains(e.Args, "status") && e.Background == 1 {
			bgChecks++
		}
	}
	if removes != wantDone {
		t.Fatalf("remove calls=%d want %d", removes, wantDone)
	}
	if !o.Force && bgChecks == 0 {
		t.Fatal("no safety status remained background")
	}
	if bg, err := unix.Getpriority(4, 0); err != nil || bg != 1 {
		t.Fatal("parent did not stay background")
	}
}

type policyFailureRunner struct {
	core.ExecRunner
	err error
}

func (r policyFailureRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if slices.Contains(args, "worktree") && slices.Contains(args, "remove") {
		if r.err != nil {
			return nil, r.err
		}
		return nil, &core.WorktreeHelperError{BeforeExec: true, Err: fmt.Errorf("fixture policy uncertainty")}
	}
	return r.ExecRunner.Output(ctx, dir, name, args...)
}
func TestPriorityFailureDoesNotRecommendForce(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/main", nil)
	p := filepath.Join(f.home, "wt/a")
	git(t, main, "worktree", "add", "-q", "--detach", p)
	o := f.opts()
	o.Runner = policyFailureRunner{}
	r := Run(context.Background(), []*core.Item{wtItem(p, main)}, o, nil).Results[0]
	if r.Status != StatusFailed || strings.Contains(r.Error, "--force") || !strings.Contains(r.Error, "Git was not started") {
		t.Fatalf("misleading policy failure: %+v", r)
	}
	mustExist(t, p)
	o.Runner = policyFailureRunner{err: &core.WorktreeHelperError{Err: fmt.Errorf("fixture interrupted child")}}
	r = Run(context.Background(), []*core.Item{wtItem(p, main)}, o, nil).Results[0]
	if r.Status != StatusFailed || strings.Contains(r.Error, "--force") || strings.Contains(r.Error, "Git was not started") {
		t.Fatalf("ambiguous child failure was overstated: %+v", r)
	}
	mustExist(t, p)
}
