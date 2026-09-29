//go:build !darwin || !cgo

package fsevents

import (
	"context"
	"time"
)

const supported = false

func currentEventID() uint64        { return 0 }
func volumeUUID(path string) string { return "" }

func replay(ctx context.Context, roots []string, since uint64, timeout time.Duration, fn func(Event) bool) bool {
	return false
}
