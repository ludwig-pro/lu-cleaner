//go:build darwin && cgo

package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

type worktreeProbe struct {
	Background, ProcessIO, BackgroundIO int
	Args                                []string
	Cwd, LC, Prompt, Locks              string
	PID, Group, Descendant              int
}

func TestMain(m *testing.M) {
	if os.Getenv("LU_TEST_HELPER_MODE") == "incompatible-success" && len(os.Args) > 1 && os.Args[1] == privateWorktreeFlag {
		os.Exit(0)
	}
	if os.Getenv("LU_TEST_HELPER_MODE") == "leak-status" && len(os.Args) > 1 && os.Args[1] == privateWorktreeFlag {
		child := exec.Command("/bin/sleep", "30")
		child.ExtraFiles = []*os.File{os.NewFile(4, "status-writer")}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(93)
		}
		_ = os.WriteFile(os.Getenv("LU_TEST_HELPER_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		os.Exit(0)
	}
	if handled, code := RunPrivateWorktreeHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	if os.Getenv("LU_WORKTREE_PROBE") == "1" && len(os.Args) > 1 && os.Args[1] == "-c" {

		state, err := (nativeChildPolicy{}).read()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(90)
		}
		cwd, _ := os.Getwd()
		group, _ := unix.Getpgid(0)
		p := worktreeProbe{Background: state.background, ProcessIO: state.processIO, BackgroundIO: state.backgroundIO, Args: os.Args, Cwd: cwd, LC: os.Getenv("LC_ALL"), Prompt: os.Getenv("GIT_TERMINAL_PROMPT"), Locks: os.Getenv("GIT_OPTIONAL_LOCKS"), PID: os.Getpid(), Group: group}
		if os.Getenv("LU_WORKTREE_PROBE_MODE") == "descendant" {
			child := exec.Command("/bin/sleep", "30")
			if err := child.Start(); err != nil {
				os.Exit(91)
			}
			p.Descendant = child.Process.Pid
		}
		data, _ := json.Marshal(p)
		fmt.Fprintln(os.Stdout, string(data))
		if marker := os.Getenv("LU_WORKTREE_PROBE_MARKER"); marker != "" {
			f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				os.Exit(92)
			}
			_, _ = f.Write(append(data, '\n'))
			f.Close()
		}
		switch os.Getenv("LU_WORKTREE_PROBE_MODE") {
		case "fail":
			fmt.Fprintln(os.Stderr, "fixture git stderr")
			os.Exit(17)
		case "wait", "descendant":
			time.Sleep(30 * time.Second)
		case "observe":
			for {
				if _, err := os.Stat(os.Getenv("LU_WORKTREE_PROBE_RELEASE")); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOwnedWorktreePolicyInSubprocess(t *testing.T) {
	if mode := os.Getenv("LU_WORKTREE_POLICY_CASE"); mode != "" {
		testOwnedWorktreePolicyChild(t, mode)
		return
	}
	for _, mode := range []string{"default", "utility", "throttle", "preexisting", "fast", "expired", "cancelled", "failure", "wrapper", "running-cancel", "descendants", "external", "leaked-status", "incompatible-success"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedWorktreePolicyInSubprocess$")
			cmd.Env = append(os.Environ(), "LU_WORKTREE_POLICY_CASE="+mode)
			if mode == "external" {
				base := t.TempDir()
				ready, release := filepath.Join(base, "ready"), filepath.Join(base, "release")
				cmd.Env = append(cmd.Env, "LU_EXTERNAL_READY="+ready, "LU_EXTERNAL_RELEASE="+release, "LU_EXTERNAL_OBSERVER="+buildPolicyObserver(t))
				var out bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &out
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				waitFixtureFile(t, ready)
				observer := strings.TrimPrefix(cmd.Env[len(cmd.Env)-1], "LU_EXTERNAL_OBSERVER=")
				if externalPolicyFlag(t, observer, cmd.Process.Pid) {
					t.Fatal("fixture unexpectedly externally background before request")
				}
				if err := unix.Setpriority(4, cmd.Process.Pid, 0x1000); err != nil {
					t.Fatal(err)
				}
				if !externalPolicyFlag(t, observer, cmd.Process.Pid) {
					t.Fatal("fixture external request was not observed")
				}
				if err := os.WriteFile(release, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatalf("external policy subprocess: %v\n%s", err, out.Bytes())
				}
			} else {
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("subprocess: %v\n%s", err, out)
				}
			}
		})
	}
}

type wrappedExecRunner struct {
	ExecRunner
	called *bool
}

func (r wrappedExecRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	*r.called = true
	return r.ExecRunner.Output(ctx, dir, name, args...)
}

func testOwnedWorktreePolicyChild(t *testing.T, mode string) {
	if err := unix.Setpriority(4, 0, 0); err != nil {
		t.Fatal(err)
	}
	ioPolicy := diskDefault
	if mode == "utility" {
		ioPolicy = diskUtility
	}
	if mode == "throttle" {
		ioPolicy = diskThrottle
	}
	if err := (nativeChildPolicy{}).setProcessIO(ioPolicy); err != nil {
		t.Fatal(err)
	}
	if mode == "preexisting" {
		if err := unix.Setpriority(4, 0, 0x1000); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "external" {
		if err := os.WriteFile(os.Getenv("LU_EXTERNAL_READY"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		waitFixtureFile(t, os.Getenv("LU_EXTERNAL_RELEASE"))
	}
	limits := scanctl.Limits{Mode: scanctl.Eco}
	if mode == "fast" {
		limits.Mode = scanctl.Fast
	}
	restore, permit, err := scanctl.Activate(limits)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	ctx := scanctl.WithWorktreePermit(context.Background(), permit)
	if mode == "expired" {
		if err := restore(); err != nil {
			t.Fatal(err)
		}
	}
	parentBefore, err := (nativeChildPolicy{}).read()
	if err != nil {
		t.Fatal(err)
	}
	if parentBefore.backgroundIO != diskUtility {
		t.Skip("host background I/O policy is not utility")
	}
	bin := filepath.Join(t.TempDir(), "bin with spaces")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(os.Args[0], filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LU_WORKTREE_PROBE", "1")
	marker := filepath.Join(bin, "started")
	t.Setenv("LU_WORKTREE_PROBE_MARKER", marker)
	if mode == "failure" {
		t.Setenv("LU_WORKTREE_PROBE_MODE", "fail")
	}
	if mode == "cancelled" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	}
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	req := WorktreeRemoval{Path: "fixture path with spaces", Force: true}
	var runner Runner = ExecRunner{}
	called := false
	if mode == "wrapper" {
		runner = wrappedExecRunner{called: &called}
	}
	if mode == "incompatible-success" {
		t.Setenv("LU_TEST_HELPER_MODE", mode)
		_, err := OutputWorktreeRemoval(ctx, runner, dir, req)
		var helperErr *WorktreeHelperError
		if !errors.As(err, &helperErr) || helperErr.BeforeExec || !strings.Contains(err.Error(), "missing helper acknowledgement") {
			t.Fatalf("incompatible success was accepted or falsely certain: %v", err)
		}
		if _, e := os.Stat(marker); !os.IsNotExist(e) {
			t.Fatal("incompatible helper was retried")
		}
		return
	}
	if mode == "leaked-status" {
		t.Setenv("LU_TEST_HELPER_MODE", "leak-status")
		pidFile := filepath.Join(bin, "leaked-pid")
		t.Setenv("LU_TEST_HELPER_CHILD_PID", pidFile)
		start := time.Now()
		_, err := OutputWorktreeRemoval(ctx, runner, dir, req)
		data, _ := os.ReadFile(pidFile)
		pid, parseErr := strconv.Atoi(string(data))
		if parseErr != nil {
			t.Fatalf("missing leak fixture: %v", parseErr)
		}
		defer unix.Kill(pid, unix.SIGKILL)
		var helperErr *WorktreeHelperError
		if !errors.As(err, &helperErr) || helperErr.BeforeExec || time.Since(start) > 5*time.Second {
			t.Fatalf("status pipe not bounded or falsely certain: %v", err)
		}
		if _, e := os.Stat(marker); !os.IsNotExist(e) {
			t.Fatal("ambiguous helper was retried")
		}
		return
	}
	var out []byte
	if mode == "running-cancel" || mode == "descendants" || mode == "external" {
		probeMode := "wait"
		if mode == "descendants" {
			probeMode = "descendant"
		}
		if mode == "external" {
			probeMode = "observe"
		}
		t.Setenv("LU_WORKTREE_PROBE_MODE", probeMode)
		release := filepath.Join(bin, "release")
		t.Setenv("LU_WORKTREE_PROBE_RELEASE", release)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		type result struct {
			out []byte
			err error
		}
		done := make(chan result, 1)
		go func() { o, e := OutputWorktreeRemoval(cctx, runner, dir, req); done <- result{o, e} }()
		waitFixtureFile(t, marker)
		data, _ := os.ReadFile(marker)
		var probe worktreeProbe
		if err := json.Unmarshal(bytes.TrimSpace(data), &probe); err != nil {
			t.Fatal(err)
		}
		if mode == "external" {
			if !externalPolicyFlag(t, os.Getenv("LU_EXTERNAL_OBSERVER"), probe.PID) {
				t.Fatal("Git target lost its inherited external background request")
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
		select {
		case r := <-done:
			out, err = r.out, r.err
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup did not cancel")
		}
		if mode != "external" {
			var helperErr *WorktreeHelperError
			if !errors.As(err, &helperErr) || helperErr.BeforeExec {
				t.Fatalf("cancelled Git outcome was not marked ambiguous: %v", err)
			}
			for _, pid := range []int{probe.PID, probe.Descendant} {
				if pid != 0 {
					waitProcessStopped(t, pid)
				}
			}
			data, _ = os.ReadFile(marker)
			if bytes.Count(data, []byte{'\n'}) != 1 {
				t.Fatal("Git was restarted after cancellation")
			}
			state, _ := (nativeChildPolicy{}).read()
			if state != parentBefore {
				t.Fatal("cancellation changed parent policy")
			}
			return
		}
	} else {
		out, err = OutputWorktreeRemoval(ctx, runner, dir, req)
	}
	if mode == "cancelled" {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
		if _, e := os.Stat(marker); !os.IsNotExist(e) {
			t.Fatal("cancelled child started")
		}
		return
	}
	if mode == "failure" {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 17 || !strings.Contains(string(ee.Stderr), "fixture git stderr") {
			t.Fatalf("Git exit/stderr changed: %T %v", err, err)
		}
	} else if err != nil {
		t.Fatalf("helper: %v output=%q", err, out)
	}
	var p worktreeProbe
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("probe: %s %v", out, err)
	}
	wantBG, wantIO := 0, diskUtility
	if mode == "throttle" {
		wantIO = diskThrottle
	}
	if mode == "preexisting" || mode == "wrapper" {
		wantBG, wantIO = 1, ioPolicy
	}
	if mode == "fast" || mode == "expired" {
		wantIO = ioPolicy
	}
	if p.Background != wantBG || p.ProcessIO != wantIO || p.BackgroundIO != diskUtility {
		t.Fatalf("policy=%+v want bg=%d io=%d", p, wantBG, wantIO)
	}
	if !reflect.DeepEqual(p.Args, append([]string{"git"}, req.Args()...)) || p.Cwd != dir || p.LC != "C" || p.Prompt != "0" || p.Locks != "0" || p.PID != p.Group {
		t.Fatalf("command contract changed: %+v", p)
	}
	parentAfter, err := (nativeChildPolicy{}).read()
	if err != nil || parentAfter != parentBefore {
		t.Fatalf("parent policy changed: before=%+v after=%+v err=%v", parentBefore, parentAfter, err)
	}
	if mode == "wrapper" && !called {
		t.Fatal("custom Output bypassed")
	}
}

func TestPrivateHelperRejectsMissingControlWithoutRunningGit(t *testing.T) {
	cmd := exec.Command(os.Args[0], privateWorktreeFlag, "/usr/bin/true", "fixture", "false")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != helperFailure || !strings.Contains(string(out), "invalid private cleanup helper") {
		t.Fatalf("private entry not gated: %q %v", out, err)
	}
}

func waitFixtureFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("fixture did not signal %s", filepath.Base(path))
}
func waitProcessStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		out, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("descendant %d remained live after cancellation", pid)
}

func TestPrivateHelperBoundsProtocolAndReportsExecFailure(t *testing.T) {
	for _, tc := range []struct {
		name, header, binary string
		stage                byte
	}{
		{"malformed", "wrong", "/usr/bin/true", worktreeProtocolFailure},
		{"oversized", worktreeControl + "extra", "/usr/bin/true", worktreeProtocolFailure},
		{"missing-exec", worktreeControl, "/nonexistent/fictional-git", worktreeExecFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controlR, controlW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer controlR.Close()
			_, _ = controlW.Write([]byte(tc.header))
			controlW.Close()
			errR, errW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer errR.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], privateWorktreeFlag, tc.binary, "fixture", "false")
			cmd.ExtraFiles = []*os.File{controlR, errW}
			out, runErr := cmd.CombinedOutput()
			errW.Close()
			frame, readErr := io.ReadAll(io.LimitReader(errR, 2*worktreeStatusSize+1))
			if tc.stage == worktreeExecFailure {
				ready := worktreeTestStatus(worktreeReady, 0)
				if len(frame) != 2*worktreeStatusSize || !bytes.Equal(frame[:worktreeStatusSize], ready) {
					t.Fatalf("missing exec acknowledgement: %v", frame)
				}
				frame = frame[worktreeStatusSize:]
			}
			var ee *exec.ExitError
			if !errors.As(runErr, &ee) || ee.ExitCode() != helperFailure || readErr != nil || len(frame) != worktreeStatusSize || string(frame[:8]) != worktreeStatusMagic || frame[8] != tc.stage || len(out) != 0 {
				t.Fatalf("status=%v frame=%v output=%s read=%v", runErr, frame, out, readErr)
			}
			if tc.stage == worktreeExecFailure && syscall.Errno(binary.LittleEndian.Uint32(frame[9:])) != syscall.ENOENT {
				t.Fatal("exec errno lost")
			}
		})
	}
}

func worktreeTestStatus(stage byte, errno syscall.Errno) []byte {
	frame := make([]byte, worktreeStatusSize)
	copy(frame, worktreeStatusMagic)
	frame[8] = stage
	binary.LittleEndian.PutUint32(frame[9:], uint32(errno))
	return frame
}

func TestWorktreeChildStatusSequences(t *testing.T) {
	helperErr := exec.Command(os.Args[0], privateWorktreeFlag).Run()
	nativeErr := exec.Command("/usr/bin/false").Run()
	var ee *exec.ExitError
	if !errors.As(helperErr, &ee) || ee.ExitCode() != helperFailure || nativeErr == nil {
		t.Fatalf("exit fixtures failed: helper=%v native=%v", helperErr, nativeErr)
	}
	ready := worktreeTestStatus(worktreeReady, 0)
	preexec := worktreeTestStatus(worktreePolicyFailure, syscall.EPERM)
	execFailure := worktreeTestStatus(worktreeExecFailure, syscall.ENOENT)
	join := func(frames ...[]byte) []byte { return bytes.Join(frames, nil) }
	for _, tc := range []struct {
		name   string
		status []byte
		runErr error
		ctxErr error
		want   string
	}{
		{"native-success", ready, nil, nil, "native"},
		{"native-failure", ready, nativeErr, nil, "native"},
		{"native-125", ready, helperErr, nil, "native"},
		{"protocol-refusal", worktreeTestStatus(worktreeProtocolFailure, 0), helperErr, nil, "preexec"},
		{"policy-refusal", preexec, helperErr, nil, "preexec"},
		{"exec-failure", join(ready, execFailure), helperErr, nil, "exec"},
		{"empty-success", nil, nil, nil, "ambiguous"},
		{"empty-failure", nil, nativeErr, nil, "ambiguous"},
		{"truncated", ready[:len(ready)-1], nil, nil, "ambiguous"},
		{"trailing-byte", join(ready, []byte{1}), nil, nil, "ambiguous"},
		{"wrong-magic", bytes.Repeat([]byte{0}, worktreeStatusSize), nil, nil, "ambiguous"},
		{"unknown-stage", worktreeTestStatus(99, 0), helperErr, nil, "ambiguous"},
		{"ready-with-errno", worktreeTestStatus(worktreeReady, syscall.EPERM), nil, nil, "ambiguous"},
		{"duplicate-ready", join(ready, ready), nil, nil, "ambiguous"},
		{"duplicate-refusal", join(preexec, preexec), helperErr, nil, "ambiguous"},
		{"refusal-success", preexec, nil, nil, "ambiguous"},
		{"refusal-wrong-exit", preexec, nativeErr, nil, "ambiguous"},
		{"exec-without-ready", execFailure, helperErr, nil, "ambiguous"},
		{"reversed", join(execFailure, ready), helperErr, nil, "ambiguous"},
		{"ready-then-refusal", join(ready, preexec), helperErr, nil, "ambiguous"},
		{"exec-without-errno", join(ready, worktreeTestStatus(worktreeExecFailure, 0)), helperErr, nil, "ambiguous"},
		{"exec-success", join(ready, execFailure), nil, nil, "ambiguous"},
		{"exec-wrong-exit", join(ready, execFailure), nativeErr, nil, "ambiguous"},
		{"extra-frame", join(ready, execFailure, ready), helperErr, nil, "ambiguous"},
		{"success-then-cancelled", ready, nil, context.Canceled, "native"},
		{"ready-cancelled", ready, context.Canceled, context.Canceled, "ambiguous"},
		{"refusal-cancelled", preexec, helperErr, context.Canceled, "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := worktreeChildResult(tc.status, tc.runErr, tc.ctxErr, "/fixture/git")
			var helper *WorktreeHelperError
			switch tc.want {
			case "native":
				if err != tc.runErr {
					t.Fatalf("native result changed: %v", err)
				}
			case "exec":
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) || pathErr.Op != "fork/exec" || pathErr.Path != "/fixture/git" || !errors.Is(err, syscall.ENOENT) {
					t.Fatalf("exec failure changed: %v", err)
				}
			default:
				if !errors.As(err, &helper) || helper.BeforeExec != (tc.want == "preexec") {
					t.Fatalf("want %s: %v", tc.want, err)
				}
			}
		})
	}
}

func TestPrivateHelperDoesNotExecWithoutWritingReady(t *testing.T) {
	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlR.Close()
	_, _ = controlW.Write([]byte(worktreeControl))
	controlW.Close()
	statusR, statusW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	statusR.Close()
	defer statusW.Close()
	marker := filepath.Join(t.TempDir(), "git-started")
	cmd := exec.Command(os.Args[0], privateWorktreeFlag, os.Args[0], "fixture", "false")
	cmd.Env = append(os.Environ(), "LU_WORKTREE_PROBE=1", "LU_WORKTREE_PROBE_MARKER="+marker)
	cmd.ExtraFiles = []*os.File{controlR, statusW}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != helperFailure {
		t.Fatalf("failed READY write did not refuse: %v %s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Git started without a complete READY write")
	}
}

// Read the target's proc_bsdinfo flags, rather than getpriority(nonzero PID):
// XNU get_background_proc reads current_task() for the latter. This standalone
// C fixture is test-only and does not add libproc to the shipped executable.
func buildPolicyObserver(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "observe.c")
	bin := filepath.Join(dir, "observe")
	code := `#include <libproc.h>
#include <sys/proc_info.h>
#include <stdio.h>
#include <stdlib.h>
int main(int argc,char **argv) {
    if(argc!=2)return 2;
    struct proc_bsdinfo info={0};
    int n=proc_pidinfo(atoi(argv[1]),PROC_PIDTBSDINFO,0,&info,sizeof(info));
    if(n!=sizeof(info))return 3;
    printf("%u\n",info.pbi_flags);
    return 0;
}
`
	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/usr/bin/clang", src, "-o", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("compile observer: %v %s", err, out)
	}
	return bin
}
func externalPolicyFlag(t *testing.T, observer string, pid int) bool {
	t.Helper()
	out, err := exec.Command(observer, strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("target policy observer unavailable: %v", err)
	}
	flags, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	// PROC_FLAG_EXT_DARWINBG in XNU task_policy.h (proc_get_darwinbgstate).
	return flags&0x10000 != 0
}
