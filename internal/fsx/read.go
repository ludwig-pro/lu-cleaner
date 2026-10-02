package fsx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

const scanBatch = 256

// ReadDir preserves os.ReadDir's sorted, complete name set while admitting
// only bounded reads. The permit is never held while a caller classifies
// entries, recurses or computes sizes.
func ReadDir(ctx context.Context, path string) ([]os.DirEntry, error) {
	f, err := openDir(ctx, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []os.DirEntry
	for {
		var batch []os.DirEntry
		err := scanctl.DoIO(ctx, func() error {
			var err error
			batch, err = f.ReadDir(scanBatch)
			return err
		})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// ReadNames returns the complete directory name set without unbounded reads.
func ReadNames(ctx context.Context, path string) ([]string, error) {
	entries, err := ReadDir(ctx, path)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names[i] = e.Name()
	}
	return names, nil
}

func openDir(ctx context.Context, path string) (f *os.File, err error) {
	if AppDataProtected(path) {
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNeedsFullDiskAccess}
	}
	err = scanctl.DoIO(ctx, func() error { f, err = os.Open(path); return err })
	return f, err
}

// Lstat is an admitted metadata probe; symlinks are never followed.
func Lstat(ctx context.Context, path string) (info os.FileInfo, err error) {
	err = scanctl.DoIO(ctx, func() error { info, err = os.Lstat(path); return err })
	return info, err
}

// ReadFile preserves the complete-file result without admitting an unbounded
// read. Parsing occurs after all permits have been released. As with os.ReadFile
// the final allocation depends on the file size; this is not a format size cap.
func ReadFile(ctx context.Context, path string) ([]byte, error) {
	f, err := openDir(ctx, path) // os.Open also accepts regular files
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var out []byte
	for {
		var n int
		err := scanctl.DoIO(ctx, func() error {
			var err error
			n, err = f.Read(buf)
			return err
		})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		out = append(out, buf[:n]...)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// Glob matches filepath.Glob's sorted results and ignored filesystem errors,
// with bounded, cancellable discovery rather than its internal ReadDir(-1).
func Glob(ctx context.Context, pattern string) ([]string, error) {
	return globDepth(ctx, pattern, 0)
}

func globDepth(ctx context.Context, pattern string, depth int) ([]string, error) {
	if depth > 10000 {
		return nil, filepath.ErrBadPattern
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.ContainsAny(pattern, "*?[\\") {
		if _, err := Lstat(ctx, pattern); err == nil {
			return []string{pattern}, nil
		}
		return nil, ctx.Err()
	}
	dir, file := filepath.Split(pattern)
	if dir == "" {
		dir = "."
	} else {
		// As in filepath.cleanGlobPath, only remove the final separator.
		// Cleaning would erase unresolved wildcard/.. components.
		if dir != string(filepath.Separator) {
			dir = dir[:len(dir)-1]
		}
	}
	var dirs []string
	if strings.ContainsAny(dir, "*?[\\") {
		var err error
		if dir == pattern {
			return nil, filepath.ErrBadPattern
		}
		dirs, err = globDepth(ctx, dir, depth+1)
		if err != nil {
			return nil, err
		}
	} else {
		dirs = []string{dir}
	}
	var matches []string
	for _, d := range dirs {
		names, err := ReadNames(ctx, d)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			continue
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if ok, _ := filepath.Match(file, name); ok {
				matches = append(matches, filepath.Join(d, name))
			}
		}
	}
	sort.Strings(matches)
	return matches, nil
}
