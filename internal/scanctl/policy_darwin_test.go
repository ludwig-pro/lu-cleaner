//go:build darwin

package scanctl

import "golang.org/x/sys/unix"

func currentBackground() (bool, error) {
	p, e := unix.Getpriority(prioDarwinProcess, 0)
	return p != 0, e
}
func setBackground(on bool) error {
	p := 0
	if on {
		p = prioDarwinBG
	}
	return unix.Setpriority(prioDarwinProcess, 0, p)
}
