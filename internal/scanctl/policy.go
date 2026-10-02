package scanctl

import (
	"runtime"
	"sync"
)

var applyBackground = background

// Activate changes only this process, until the command returns. Keeping
// process policy outside Controller makes library scans and tests pure.
func Activate(l Limits) (func() error, error) {
	if l.Mode != Eco {
		return func() error { return nil }, nil
	}
	previous := runtime.GOMAXPROCS(0)
	runtime.GOMAXPROCS(min(previous, 2))
	restorePriority, err := applyBackground()
	var once sync.Once
	var restoreErr error
	return func() error {
		once.Do(func() {
			restoreErr = restorePriority()
			runtime.GOMAXPROCS(previous)
		})
		return restoreErr
	}, err
}
