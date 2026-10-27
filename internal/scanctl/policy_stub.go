//go:build !darwin

package scanctl

func background() (func() error, bool, error) { return func() error { return nil }, false, nil }
