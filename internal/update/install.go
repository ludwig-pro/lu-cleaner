package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxArchive = 128 << 20
const maxBinary = 96 << 20

type target struct {
	path             string
	parent, original *os.File
	info             os.FileInfo
	digest           [32]byte
}

func inspectTarget(path string) (*target, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(dir, filepath.Base(path))
	for _, part := range strings.Split(strings.ToLower(path), string(filepath.Separator)) {
		switch part {
		case "cellar", "caskroom", ".nix-profile", "nix", "homebrew", "macports":
			return nil, fmt.Errorf("package-managed executable")
		}
	}
	if strings.HasPrefix(path, "/opt/local/") {
		return nil, fmt.Errorf("package-managed executable")
	}
	pfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	t := &target{path: path, parent: os.NewFile(uintptr(pfd), dir)}
	if err := t.checkParent(); err != nil {
		t.close()
		return nil, err
	}
	fd, err := unix.Openat(pfd, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.close()
		return nil, err
	}
	t.original = os.NewFile(uintptr(fd), path)
	t.info, err = t.original.Stat()
	if err == nil {
		err = statSafe(t.original, false)
	}
	if err == nil {
		err = safeExecutable(t.info)
	}
	if err == nil {
		t.digest, err = fileDigest(t.original, t.info.Size())
	}
	if err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}

func (t *target) close() {
	if t.original != nil {
		t.original.Close()
	}
	if t.parent != nil {
		t.parent.Close()
	}
}

func safeExecutable(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Size() <= 0 || info.Size() > maxBinary {
		return fmt.Errorf("unsupported executable")
	}
	return nil
}

func statSafe(f *os.File, directory bool) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("foreign executable owner")
	}
	if directory {
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0o022 != 0 {
			return fmt.Errorf("unsafe executable directory")
		}
		if err := unix.Faccessat(int(f.Fd()), ".", unix.W_OK, unix.AT_EACCESS); err != nil {
			return err
		}
	} else if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return fmt.Errorf("unsafe executable links or file type")
	}
	return nil
}

func (t *target) checkParent() error {
	if err := statSafe(t.parent, true); err != nil {
		return err
	}
	opened, err := t.parent.Stat()
	if err != nil {
		return err
	}
	current, err := os.Stat(filepath.Dir(t.path))
	if err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf("executable directory changed")
	}
	return nil
}

func fileDigest(f *os.File, size int64) ([32]byte, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.NewSectionReader(f, 0, size+1))
	if err != nil {
		return [32]byte{}, err
	}
	if n != size {
		return [32]byte{}, fmt.Errorf("executable size changed")
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func (t *target) unchanged() error {
	if err := t.checkParent(); err != nil {
		return err
	}
	if err := statSafe(t.original, false); err != nil {
		return err
	}
	current, err := os.Lstat(t.path)
	if err != nil {
		return err
	}
	if !os.SameFile(t.info, current) || current.Size() != t.info.Size() || current.Mode() != t.info.Mode() {
		return fmt.Errorf("executable changed")
	}
	sum, err := fileDigest(t.original, t.info.Size())
	if err != nil {
		return err
	}
	if sum != t.digest {
		return fmt.Errorf("executable content changed")
	}
	return nil
}

func (t *target) create(prefix string) (*os.File, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	name := prefix + hex.EncodeToString(id[:])
	fd, err := unix.Openat(int(t.parent.Fd()), name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(filepath.Dir(t.path), name)), nil
}

func (t *target) remove(f *os.File) {
	f.Close()
	_ = unix.Unlinkat(int(t.parent.Fd()), filepath.Base(f.Name()), 0)
}

func stageVerified(ctx context.Context, client *http.Client, r release, t *target) (_ *os.File, err error) {
	data, err := fetchBounded(ctx, client, r.sumsURL, maxMetadata)
	if err != nil {
		return nil, err
	}
	want, err := checksum(data)
	if err != nil {
		return nil, err
	}
	archive, err := t.create(".lu-cleaner-archive-")
	if err != nil {
		return nil, err
	}
	defer t.remove(archive)
	resp, err := response(ctx, client, r.archiveURL)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(archive, h), io.LimitReader(resp.Body, maxArchive+1))
	closeErr := resp.Body.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if n > maxArchive {
		return nil, fmt.Errorf("archive too large")
	}
	if hex.EncodeToString(h.Sum(nil)) != hex.EncodeToString(want[:]) {
		return nil, fmt.Errorf("archive checksum mismatch")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	candidate, err := t.create(".lu-cleaner-update-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			t.remove(candidate)
		}
	}()
	if err = extractBinary(ctx, archive, candidate); err != nil {
		return nil, err
	}
	if err = validateBinary(candidate); err != nil {
		return nil, err
	}
	if err = candidate.Chmod(0o755); err != nil {
		return nil, err
	}
	if err = candidate.Sync(); err != nil {
		return nil, err
	}
	return candidate, nil
}

func extractBinary(ctx context.Context, archive io.Reader, candidate io.Writer) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: maxArchive + 1}
	tr := tar.NewReader(limited)
	found := false
	seen := make(map[string]bool)
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if count >= 16 || seen[hdr.Name] || hdr.Size < 0 || (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA) {
			return fmt.Errorf("unsupported archive entry")
		}
		seen[hdr.Name] = true
		switch hdr.Name {
		case "lu-cleaner":
			if hdr.Size <= 0 || hdr.Size > maxBinary || hdr.Mode&0o111 == 0 {
				return fmt.Errorf("unsupported binary entry")
			}
			if _, err := io.Copy(candidate, tr); err != nil {
				return err
			}
			found = true
		case "README.md", "LICENSE":
			if hdr.Size > maxMetadata {
				return fmt.Errorf("archive documentation too large")
			}
		default:
			return fmt.Errorf("unexpected archive path")
		}
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N <= 0 {
		return fmt.Errorf("uncompressed archive too large")
	}
	if !found {
		return fmt.Errorf("archive binary missing")
	}
	return ctx.Err()
}

func validateBinary(f *os.File) error {
	var header [8]byte
	if _, err := f.ReadAt(header[:], 0); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(header[:4]) != macho.MagicFat || binary.BigEndian.Uint32(header[4:]) != 2 {
		return fmt.Errorf("unsupported universal executable header")
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	fat, err := macho.NewFatFile(f)
	if err != nil {
		return fmt.Errorf("invalid universal executable: %w", err)
	}
	if len(fat.Arches) != 2 {
		return fmt.Errorf("unsupported executable architectures")
	}
	seen := make(map[macho.Cpu]bool)
	for i, arch := range fat.Arches {
		start, end := uint64(arch.Offset), uint64(arch.Offset)+uint64(arch.Size)
		if start < 48 || arch.Size < 32 || end > uint64(info.Size()) || arch.Align > 31 || start%(uint64(1)<<arch.Align) != 0 {
			return fmt.Errorf("invalid executable slice bounds")
		}
		if i > 0 {
			previous := fat.Arches[0]
			if start < uint64(previous.Offset)+uint64(previous.Size) && uint64(previous.Offset) < end {
				return fmt.Errorf("overlapping executable slices")
			}
		}
		if arch.File.Magic != macho.Magic64 || arch.File.Cpu != arch.Cpu || arch.File.SubCpu != arch.SubCpu || arch.File.Type != macho.TypeExec || seen[arch.Cpu] || (arch.Cpu != macho.CpuAmd64 && arch.Cpu != macho.CpuArm64) {
			return fmt.Errorf("unsupported executable architecture or type")
		}
		seen[arch.Cpu] = true
	}
	return nil
}

func commit(ctx context.Context, t *target, candidate *os.File) (backupPath string, committed bool, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if err = t.unchanged(); err != nil {
		return
	}
	backup, err := t.create(".lu-cleaner-backup-")
	if err != nil {
		return "", false, err
	}
	backupPath = backup.Name()
	defer backup.Close()
	completeBackup := false
	defer func() {
		if !completeBackup {
			t.remove(backup)
			backupPath = ""
		}
	}()
	if _, err = io.Copy(backup, io.NewSectionReader(t.original, 0, t.info.Size())); err != nil {
		return
	}
	if err = backup.Chmod(0o700); err != nil {
		return
	}
	if err = backup.Sync(); err != nil {
		return
	}
	var saved [32]byte
	if saved, err = fileDigest(backup, t.info.Size()); err != nil {
		return
	}
	if saved != t.digest {
		err = fmt.Errorf("backup verification failed")
		return
	}
	completeBackup = true
	if err = t.unchanged(); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if err = candidate.Close(); err != nil {
		return
	}
	err = unix.Renameat(int(t.parent.Fd()), filepath.Base(candidate.Name()), int(t.parent.Fd()), filepath.Base(t.path))
	if err != nil {
		return
	}
	committed = true
	err = t.parent.Sync()
	return
}
