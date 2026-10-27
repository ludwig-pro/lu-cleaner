//go:build darwin && cgo

package core

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

const worktreeControl = "LU-WT-PRIORITY1\n"
const worktreeStatusMagic = "LUWTSTA1"
const worktreeStatusSize = 13
const helperFailure = 125

const (
	worktreeReady byte = iota
	worktreeProtocolFailure
	worktreePolicyFailure
	worktreeExecFailure
)

func outputWorktreeRemoval(ctx context.Context, dir string, req WorktreeRemoval) ([]byte, error) {
	if !worktreeHelperEnabled.Load() {
		return (ExecRunner{}).Output(ctx, dir, "git", req.Args()...)
	}
	out, attempted, err := scanctl.WithOwnedBackground(ctx, func() ([]byte, error) {
		return runOwnedWorktreeChild(ctx, dir, req)
	})
	if attempted {
		return out, err
	}
	return (ExecRunner{}).Output(ctx, dir, "git", req.Args()...)
}

func runOwnedWorktreeChild(ctx context.Context, dir string, req WorktreeRemoval) ([]byte, error) {
	args := req.Args()
	direct := execCommand(ctx, dir, "git", args...)
	directOutput := func() ([]byte, error) {
		start := time.Now()
		out, err := direct.Output()
		fsx.Trace("cmd", "git "+strings.Join(args, " "), start, dir)
		return out, err
	}
	// Preserve LookPath / ErrDot behavior before any helper starts.
	if direct.Err != nil {
		return directOutput()
	}
	executable, err := os.Executable()
	if err != nil {
		return directOutput()
	}
	controlR, controlW, err := os.Pipe()
	if err != nil {
		return directOutput()
	}
	defer controlR.Close()
	if _, err = controlW.Write([]byte(worktreeControl)); err != nil {
		controlW.Close()
		return directOutput()
	}
	controlW.Close()
	errorR, errorW, err := os.Pipe()
	if err != nil {
		return directOutput()
	}
	defer errorR.Close()
	defer errorW.Close()
	helper := execCommand(ctx, dir, executable, privateWorktreeFlag, direct.Path, req.Path, strconv.FormatBool(req.Force))
	helper.Env = direct.Env
	helper.ExtraFiles = []*os.File{controlR, errorW}
	start := time.Now()
	out, runErr := helper.Output()
	// Only a failure proven to precede process creation may use the direct
	// inherited path. A started helper may have executed Git: never retry it.
	if helper.Process == nil && ctx.Err() == nil {
		return directOutput()
	}
	fsx.Trace("cmd", "git "+strings.Join(args, " "), start, dir)
	errorW.Close()
	// WaitDelay only covers stdout/stderr. A replaced or incompatible helper
	// could leak its status descriptor into an escaped descendant as well.
	if err := errorR.SetReadDeadline(time.Now().Add(waitDelay)); err != nil {
		return out, &WorktreeHelperError{Err: err}
	}
	status, readErr := io.ReadAll(io.LimitReader(errorR, 2*worktreeStatusSize+1))
	if readErr != nil {
		return out, &WorktreeHelperError{Err: readErr}
	}
	return out, worktreeChildResult(status, runErr, ctx.Err(), direct.Path)
}

func worktreeChildResult(status []byte, runErr, ctxErr error, gitPath string) error {
	if len(status) == 0 {
		if runErr != nil {
			return &WorktreeHelperError{Err: runErr}
		}
		return &WorktreeHelperError{Err: errors.New("missing helper acknowledgement")}
	}
	if len(status) != worktreeStatusSize && len(status) != 2*worktreeStatusSize {
		return &WorktreeHelperError{Err: errors.New("malformed helper status")}
	}
	for offset := 0; offset < len(status); offset += worktreeStatusSize {
		if string(status[offset:offset+8]) != worktreeStatusMagic {
			return &WorktreeHelperError{Err: errors.New("malformed helper status")}
		}
	}
	var ee *exec.ExitError
	interrupted := runErr != nil && ctxErr != nil
	helperFailed := false
	if errors.As(runErr, &ee) {
		helperFailed = ee.ExitCode() == helperFailure
		if state, ok := ee.ProcessState.Sys().(syscall.WaitStatus); ok {
			interrupted = interrupted || state.Signaled()
		}
	}
	if interrupted {
		return &WorktreeHelperError{Err: runErr}
	}
	stage := status[8]
	errno := syscall.Errno(binary.LittleEndian.Uint32(status[9:13]))
	if stage == worktreeReady && errno == 0 {
		if len(status) == worktreeStatusSize {
			return runErr
		}
		stage = status[worktreeStatusSize+8]
		errno = syscall.Errno(binary.LittleEndian.Uint32(status[worktreeStatusSize+9:]))
		if stage == worktreeExecFailure && errno != 0 && helperFailed {
			return &os.PathError{Op: "fork/exec", Path: gitPath, Err: errno}
		}
	} else if len(status) == worktreeStatusSize && helperFailed && (stage == worktreeProtocolFailure || stage == worktreePolicyFailure) {
		reason := error(errors.New("policy or helper protocol verification failed"))
		if errno != 0 {
			reason = errno
		}
		return &WorktreeHelperError{BeforeExec: true, Err: reason}
	}
	return &WorktreeHelperError{Err: errors.New("inconsistent helper status")}
}

// A fixed, prefilled anonymous pipe gates the private entry. Nonblocking raw
// reads keep malformed invocations bounded; the marker is not authentication
// against another process owned by the same user.
func privatePipe(fd int) bool {
	var st unix.Stat_t
	return unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFIFO && st.Nlink == 0
}

func runPrivateWorktreeHelper(args []string) int {
	if !privatePipe(3) || !privatePipe(4) {
		fmt.Fprintln(os.Stderr, "invalid private cleanup helper invocation")
		return helperFailure
	}
	if err := unix.SetNonblock(3, true); err != nil {
		return reportWorktreeFailure(worktreeProtocolFailure, err)
	}
	b := make([]byte, len(worktreeControl)+1)
	n, err := unix.Read(3, b)
	unix.Close(3)
	if err != nil || n != len(worktreeControl) || string(b[:n]) != worktreeControl || len(args) != 4 || args[1] == "" {
		return reportWorktreeFailure(worktreeProtocolFailure, errors.New("invalid helper protocol"))
	}
	force, err := strconv.ParseBool(args[3])
	if err != nil || (args[3] != "true" && args[3] != "false") {
		return reportWorktreeFailure(worktreeProtocolFailure, errors.New("invalid force field"))
	}
	unix.CloseOnExec(4)
	flags, err := unix.FcntlInt(4, unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return reportWorktreeFailure(worktreeProtocolFailure, errors.New("status descriptor is not close-on-exec"))
	}
	if _, err := prepareWorktreeChild(nativeChildPolicy{}); err != nil {
		return reportWorktreeFailure(worktreePolicyFailure, err)
	}
	req := WorktreeRemoval{Path: args[2], Force: force}
	argv := append([]string{"git"}, req.Args()...)
	env := os.Environ()
	if err := writeWorktreeStatus(worktreeReady, nil); err != nil {
		return reportWorktreeFailure(worktreeProtocolFailure, err)
	}
	return reportWorktreeFailure(worktreeExecFailure, syscall.Exec(args[1], argv, env))
}

func writeWorktreeStatus(stage byte, err error) error {
	frame := make([]byte, worktreeStatusSize)
	copy(frame, worktreeStatusMagic)
	frame[8] = stage
	var errno syscall.Errno
	if errors.As(err, &errno) {
		binary.LittleEndian.PutUint32(frame[9:], uint32(errno))
	}
	n, writeErr := unix.Write(4, frame)
	if writeErr != nil {
		return writeErr
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	return nil
}

func reportWorktreeFailure(stage byte, err error) int {
	_ = writeWorktreeStatus(stage, err)
	return helperFailure
}
