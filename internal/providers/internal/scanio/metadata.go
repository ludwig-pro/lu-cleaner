// Package scanio admits metadata batches used by provider discovery loops.
package scanio

import (
	"context"
	"os"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

type Info struct {
	File os.FileInfo
	Err  error
}

type Stat struct {
	File unix.Stat_t
	Err  error
}

// Lstats returns one result per path, preserving individual filesystem errors.
// A cancelled batch returns no partial array that could be mistaken for a
// complete inventory. Permits are released before the caller classifies files.
func Lstats(ctx context.Context, paths []string) ([]Info, error) {
	out := make([]Info, len(paths))
	err := batches(ctx, len(paths), func(i int) { out[i].File, out[i].Err = os.Lstat(paths[i]) })
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UnixLstats preserves the native allocation, device, inode and link metadata
// required for reclaim and safety decisions. It never follows symlinks.
func UnixLstats(ctx context.Context, paths []string) ([]Stat, error) {
	out := make([]Stat, len(paths))
	err := batches(ctx, len(paths), func(i int) { out[i].Err = unix.Lstat(paths[i], &out[i].File) })
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fn is internal and does only a single native metadata operation. It must not
// recurse or invoke another admitted operation.
func batches(ctx context.Context, n int, fn func(int)) error {
	batchSize := 256
	if c := scanctl.From(ctx); c != nil {
		batchSize = min(batchSize, c.Limits().BatchSize)
	}
	for start := 0; start < n; start += batchSize {
		end := min(start+batchSize, n)
		if err := scanctl.DoIO(ctx, func() error {
			for i := start; i < end; i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				fn(i)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return ctx.Err()
}
