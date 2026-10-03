package statefile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPrivateFilesAndUnsafeTargets(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "state")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o700 {
		t.Fatal("directory not private")
	}
	if err := os.WriteFile(filepath.Join(path, "old"), []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := d.Open("old", unix.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if st, _ := os.Stat(filepath.Join(path, "old")); st.Mode().Perm() != 0o600 {
		t.Fatal("legacy permissions not secured")
	}
	if err := os.Symlink(filepath.Join(path, "old"), filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(path, "old"), filepath.Join(path, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(path, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "hardlink", "fifo", "../outside"} {
		if f, err := d.Open(name, unix.O_RDONLY); err == nil {
			f.Close()
			t.Fatalf("unsafe file accepted: %s", name)
		}
	}
	if err := os.Symlink(path, filepath.Join(base, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenDir(filepath.Join(base, "linked-dir")); err == nil {
		other.Close()
		t.Fatal("directory symlink accepted")
	}
}

func TestBusyStateLockCannotHangExit(t *testing.T) {
	d, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	unlock, err := d.Lock("held.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := make(chan error, 1)
	go func() {
		release, err := d.Lock("held.lock")
		if release != nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second writer bypassed the state lock")
		}
	case <-time.After(time.Second):
		t.Fatal("private state lock delayed exit indefinitely")
	}
}

func TestAtomicPutDoesNotFollowDestination(t *testing.T) {
	base := t.TempDir()
	d, err := OpenDir(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	target := filepath.Join(base, "external")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "state", "report")); err != nil {
		t.Fatal(err)
	}
	if err := d.Put("report", []byte("sanitized")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "unchanged" {
		t.Fatal("followed symlink")
	}
	data, _ = os.ReadFile(filepath.Join(base, "state", "report"))
	if string(data) != "sanitized" {
		t.Fatal("atomic replacement failed")
	}
}
