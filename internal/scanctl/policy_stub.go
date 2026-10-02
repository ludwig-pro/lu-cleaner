//go:build !darwin

package scanctl

func background() (func() error, error) { return func() error { return nil }, nil }
