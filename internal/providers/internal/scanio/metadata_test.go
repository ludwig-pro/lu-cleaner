package scanio

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

func TestMetadataBatchesPreserveMissingFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	file, link, missing := filepath.Join(root, "file"), filepath.Join(root, "link"), filepath.Join(root, "missing")
	if err := os.WriteFile(file, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	paths := []string{file, link, missing}
	infos, err := Lstats(context.Background(), paths)
	if err != nil || len(infos) != len(paths) {
		t.Fatalf("infos=%v error=%v", infos, err)
	}
	if !infos[0].File.Mode().IsRegular() || infos[1].File.Mode()&os.ModeSymlink == 0 || !errors.Is(infos[2].Err, fs.ErrNotExist) {
		t.Fatalf("metadata did not preserve file types/errors: %+v", infos)
	}
	stats, err := UnixLstats(context.Background(), paths)
	if err != nil || stats[1].File.Mode&unix.S_IFMT != unix.S_IFLNK || !errors.Is(stats[2].Err, fs.ErrNotExist) {
		t.Fatalf("native metadata did not preserve file types/errors: %+v, %v", stats, err)
	}
}

func TestMetadataAdmissionCancelsWithoutPartialInventory(t *testing.T) {
	limits, _, _ := scanctl.Resolve("fast", "1")
	controller := scanctl.New(limits)
	owner := scanctl.With(context.Background(), controller)
	release, err := controller.AcquireIO(owner)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(owner, 20*time.Millisecond)
	defer cancel()
	infos, err := Lstats(ctx, []string{t.TempDir()})
	if !errors.Is(err, context.DeadlineExceeded) || len(infos) != 0 {
		t.Fatalf("blocked metadata returned a complete-looking inventory: %+v, %v", infos, err)
	}
}
