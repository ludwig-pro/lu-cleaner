//go:build darwin && cgo

package core

/*
#include <sys/resource.h>
#include <errno.h>

static int lu_get_disk_policy(int scope, int *value) {
    errno = 0;
    *value = getiopolicy_np(IOPOL_TYPE_DISK, scope);
    return *value == -1 ? errno : 0;
}
static int lu_set_disk_policy(int value) {
    errno = 0;
    return setiopolicy_np(IOPOL_TYPE_DISK, IOPOL_SCOPE_PROCESS, value) == -1 ? errno : 0;
}
*/
import "C"

import (
	"golang.org/x/sys/unix"
	"syscall"
)

type nativeChildPolicy struct{}

func (nativeChildPolicy) read() (childPolicyState, error) {
	bg, err := unix.Getpriority(C.PRIO_DARWIN_PROCESS, 0)
	if err != nil {
		return childPolicyState{}, err
	}
	var process, background C.int
	if errno := C.lu_get_disk_policy(C.IOPOL_SCOPE_PROCESS, &process); errno != 0 {
		return childPolicyState{}, syscall.Errno(errno)
	}
	if errno := C.lu_get_disk_policy(C.IOPOL_SCOPE_DARWIN_BG, &background); errno != 0 {
		return childPolicyState{}, syscall.Errno(errno)
	}
	return childPolicyState{background: bg, processIO: int(process), backgroundIO: int(background)}, nil
}
func (nativeChildPolicy) setProcessIO(value int) error {
	if errno := C.lu_set_disk_policy(C.int(value)); errno != 0 {
		return syscall.Errno(errno)
	}
	return nil
}
func (nativeChildPolicy) clearBackground() error {
	return unix.Setpriority(C.PRIO_DARWIN_PROCESS, 0, 0)
}
