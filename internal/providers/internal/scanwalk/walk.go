// Package scanwalk provides cancellable discovery walks for providers.
package scanwalk

import (
	"context"
	"io/fs"
	"path/filepath"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// WalkDir preserves filepath.WalkDir's lexical order, symlink policy, callback
// errors and skip semantics, while admitting each directory read through the
// scan controller. Cancellation stops the walk even if fn ignores errors.
func WalkDir(ctx context.Context, root string, fn fs.WalkDirFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := fsx.Lstat(ctx, root)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		err = fn(root, nil, err)
	} else {
		err = walk(ctx, root, fs.FileInfoToDirEntry(info), fn)
	}
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

func walk(ctx context.Context, path string, entry fs.DirEntry, fn fs.WalkDirFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := fn(path, entry, nil)
	if err != nil || !entry.IsDir() {
		if err == fs.SkipDir && entry.IsDir() {
			return nil
		}
		return err
	}
	children, err := fsx.ReadDir(ctx, path)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if err = fn(path, entry, err); err == fs.SkipDir {
			return nil
		}
		return err
	}
	for _, child := range children {
		err := walk(ctx, filepath.Join(path, child.Name()), child, fn)
		if err == fs.SkipDir {
			break // a file's SkipDir skips its following siblings
		}
		if err != nil {
			return err
		}
	}
	return nil
}
