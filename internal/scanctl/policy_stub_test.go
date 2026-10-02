//go:build !darwin

package scanctl

func currentBackground() (bool, error) { return true, nil }
func setBackground(bool) error         { return nil }
