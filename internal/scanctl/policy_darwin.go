//go:build darwin

package scanctl

import "golang.org/x/sys/unix"

// Public Darwin SDK sys/resource.h; setpriority(2) permits a process to set
// and revoke its own background state, without elevated privileges.
const prioDarwinProcess = 4
const prioDarwinBG = 0x1000

func background() (func() error, error) {
	previous, err := unix.Getpriority(prioDarwinProcess, 0)
	if err != nil {
		return func() error { return nil }, err
	}
	if previous != 0 {
		return func() error { return nil }, nil
	}
	if err := unix.Setpriority(prioDarwinProcess, 0, prioDarwinBG); err != nil {
		return func() error { return nil }, err
	}
	return func() error { return unix.Setpriority(prioDarwinProcess, 0, 0) }, nil
}
