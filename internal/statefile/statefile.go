// Package statefile stores private, bounded application state. Final directory
// and file symlinks, foreign owners, special files and hardlinks are refused.
package statefile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type Dir struct{ f *os.File }

func OpenDir(path string) (*Dir, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err == nil && st.Uid != uint32(os.Getuid()) {
		err = fmt.Errorf("state directory has another owner")
	}
	if err == nil {
		err = unix.Fchmod(fd, 0o700)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Dir{f: f}, nil
}

func (d *Dir) Close() error { return d.f.Close() }

func nameOK(name string) bool { return name != "." && name != ".." && name == filepath.Base(name) }

func (d *Dir) Open(name string, flags int) (*os.File, error) {
	if !nameOK(name) {
		return nil, fmt.Errorf("invalid state filename")
	}
	fd, err := unix.Openat(int(d.f.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err == nil {
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
			err = fmt.Errorf("state file must be a regular, privately owned file without hardlinks")
		}
	}
	if err == nil {
		err = unix.Fchmod(fd, 0o600)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Lock serializes readers, appends and rotations across CLI invocations.
func (d *Dir) Lock(name string) (func(), error) {
	f, err := d.Open(name, unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			f.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("private state is busy")
		}
		timer := time.NewTimer(5 * time.Millisecond)
		<-timer.C
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}

func (d *Dir) Remove(name string) error {
	if !nameOK(name) {
		return fmt.Errorf("invalid state filename")
	}
	err := unix.Unlinkat(int(d.f.Fd()), name, 0)
	if err == unix.ENOENT {
		return nil
	}
	return err
}

func (d *Dir) Rename(from, to string) error {
	if !nameOK(from) || !nameOK(to) {
		return fmt.Errorf("invalid state filename")
	}
	return unix.Renameat(int(d.f.Fd()), from, int(d.f.Fd()), to)
}

// Put replaces a file atomically, without following an existing destination.
func (d *Dir) Put(name string, data []byte) error {
	if !nameOK(name) {
		return fmt.Errorf("invalid state filename")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	tmp := ".tmp-" + hex.EncodeToString(id[:])
	f, err := d.Open(tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
	if err != nil {
		return err
	}
	defer d.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return d.Rename(tmp, name)
}
