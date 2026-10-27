package core

import (
	"context"
	"fmt"
	"sync/atomic"
)

// WorktreeRemoval describes the one command eligible for an owned-background
// child policy. Checks, ordering, force consent and timeouts stay in clean.
type WorktreeRemoval struct {
	Path  string
	Force bool
}

func (r WorktreeRemoval) Args() []string {
	args := []string{"-c", "core.fsmonitor=false", "-c", "status.showUntrackedFiles=all", "worktree", "remove"}
	if r.Force {
		args = append(args, "--force", "--force")
	}
	return append(args, r.Path)
}

// OutputWorktreeRemoval never bypasses a custom Runner, including wrappers
// embedding ExecRunner while overriding Output.
func OutputWorktreeRemoval(ctx context.Context, r Runner, dir string, req WorktreeRemoval) ([]byte, error) {
	switch r.(type) {
	case ExecRunner, *ExecRunner:
		return outputWorktreeRemoval(ctx, dir, req)
	default:
		return r.Output(ctx, dir, "git", req.Args()...)
	}
}

const privateWorktreeFlag = "--internal-worktree-priority-v1"

var worktreeHelperEnabled atomic.Bool

// RunPrivateWorktreeHelper must be called by main before starting the CLI.
// A normal invocation registers this executable's ability to dispatch the
// helper. Library callers whose main lacks this entry point inherit policy.
func RunPrivateWorktreeHelper(args []string) (bool, int) {
	if len(args) == 0 || args[0] != privateWorktreeFlag {
		worktreeHelperEnabled.Store(true)
		return false, 0
	}
	return true, runPrivateWorktreeHelper(args)
}

// WorktreeHelperError distinguishes priority/protocol failures from a Git
// refusal. A verified pre-exec failure did not start Git; ambiguous failures
// must never cause a retry or suggest bypassing the ordinary safety checks.
type WorktreeHelperError struct {
	BeforeExec bool
	Err        error
}

func (e *WorktreeHelperError) Error() string {
	if e.BeforeExec {
		return fmt.Sprintf("cleanup child could not safely prepare its priority; Git was not started: %v", e.Err)
	}
	return fmt.Sprintf("cleanup child did not confirm its outcome; rescan before retrying: %v", e.Err)
}
func (e *WorktreeHelperError) Unwrap() error { return e.Err }
