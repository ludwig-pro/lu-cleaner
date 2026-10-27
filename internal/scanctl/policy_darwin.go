//go:build darwin

package scanctl

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Public Darwin SDK sys/resource.h; the getter on self reports its internal
// request, independently of an externally imposed background policy.
const prioDarwinProcess = 4
const prioDarwinBG = 0x1000

func background() (func() error, bool, error) {
	noop := func() error { return nil }
	previous, err := unix.Getpriority(prioDarwinProcess, 0)
	if err != nil {
		return noop, false, err
	}
	if previous != 0 {
		return noop, false, nil
	}
	if err := unix.Setpriority(prioDarwinProcess, 0, prioDarwinBG); err != nil {
		return noop, false, err
	}
	restore := func() error { return unix.Setpriority(prioDarwinProcess, 0, 0) }
	current, err := unix.Getpriority(prioDarwinProcess, 0)
	if err != nil {
		return restore, false, err
	}
	if current != 1 {
		return restore, false, fmt.Errorf("cannot verify the process background request")
	}
	return restore, true, nil
}
