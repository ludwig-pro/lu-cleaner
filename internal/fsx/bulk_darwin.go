//go:build darwin

package fsx

import (
	"encoding/binary"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// getattrlistbulk(2) returns the attributes of many directory entries per
// system call, which is much cheaper than one lstat per entry on APFS.

const (
	attrBitMapCount = 5

	attrCmnName          = 0x00000001
	attrCmnDevID         = 0x00000002
	attrCmnObjType       = 0x00000008
	attrCmnModTime       = 0x00000400
	attrCmnFileID        = 0x02000000
	attrCmnError         = 0x20000000
	attrCmnReturnedAttrs = 0x80000000

	attrFileLinkCount = 0x00000001
	attrFileTotalSize = 0x00000002
	attrFileAllocSize = 0x00000004

	vREG = 1
	vDIR = 2
	vLNK = 5

	sysGetattrlistbulk = 461
)

type attrList struct {
	bitmapCount uint16
	reserved    uint16
	commonAttr  uint32
	volAttr     uint32
	dirAttr     uint32
	fileAttr    uint32
	forkAttr    uint32
}

var bulkAttrs = attrList{
	bitmapCount: attrBitMapCount,
	commonAttr:  attrCmnReturnedAttrs | attrCmnName | attrCmnDevID | attrCmnObjType | attrCmnModTime | attrCmnFileID | attrCmnError,
	fileAttr:    attrFileLinkCount | attrFileTotalSize | attrFileAllocSize,
}

var bulkBufPool = sync.Pool{New: func() any { b := make([]byte, 128<<10); return &b }}

// bulkEntry is one decoded directory entry.
type bulkEntry struct {
	name     string
	objType  uint32
	dev      int32
	ino      uint64
	mtimeNs  int64
	nlink    uint32
	size     int64 // logical size (files)
	alloc    int64 // allocated bytes (files)
	hasError bool
}

// readDirBulk lists dir with getattrlistbulk. ok=false means the call is
// unsupported for this directory (the caller falls back to lstat).
func readDirBulk(dir string, fn func(e *bulkEntry)) (ok bool, err error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return true, err
	}
	defer unix.Close(fd)
	bp := bulkBufPool.Get().(*[]byte)
	defer bulkBufPool.Put(bp)
	buf := *bp
	var e bulkEntry
	for {
		n, _, errno := syscall.Syscall6(sysGetattrlistbulk, uintptr(fd),
			uintptr(unsafe.Pointer(&bulkAttrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		if errno != 0 {
			if errno == syscall.ENOTSUP || errno == syscall.ENOSYS || errno == syscall.EINVAL {
				return false, nil
			}
			return true, &os.PathError{Op: "getattrlistbulk", Path: dir, Err: errno}
		}
		if n == 0 {
			return true, nil
		}
		off := 0
		for i := 0; i < int(n); i++ {
			if off+4 > len(buf) {
				return true, nil
			}
			length := int(binary.LittleEndian.Uint32(buf[off:]))
			if length <= 0 || off+length > len(buf) {
				return true, nil
			}
			rec := buf[off : off+length]
			decodeBulk(rec, &e)
			fn(&e)
			off += length
		}
	}
}

func decodeBulk(rec []byte, e *bulkEntry) {
	*e = bulkEntry{}
	p := 4 // length
	le := binary.LittleEndian
	common := le.Uint32(rec[p:])
	fileA := le.Uint32(rec[p+12:])
	p += 20 // attribute_set_t
	if common&attrCmnError != 0 {
		if le.Uint32(rec[p:]) != 0 {
			e.hasError = true
		}
		p += 4
	}
	if common&attrCmnName != 0 {
		nameOff := int(int32(le.Uint32(rec[p:])))
		nameLen := int(le.Uint32(rec[p+4:]))
		start := p + nameOff
		if nameLen > 0 && start >= 0 && start+nameLen <= len(rec) {
			e.name = string(rec[start : start+nameLen-1]) // drop NUL
		}
		p += 8
	}
	if common&attrCmnDevID != 0 {
		e.dev = int32(le.Uint32(rec[p:]))
		p += 4
	}
	if common&attrCmnObjType != 0 {
		e.objType = le.Uint32(rec[p:])
		p += 4
	}
	if common&attrCmnModTime != 0 {
		sec := int64(le.Uint64(rec[p:]))
		nsec := int64(le.Uint64(rec[p+8:]))
		e.mtimeNs = sec*1e9 + nsec
		p += 16
	}
	if common&attrCmnFileID != 0 {
		e.ino = le.Uint64(rec[p:])
		p += 8
	}
	if fileA&attrFileLinkCount != 0 {
		e.nlink = le.Uint32(rec[p:])
		p += 4
	}
	if fileA&attrFileTotalSize != 0 {
		e.size = int64(le.Uint64(rec[p:]))
		p += 8
	}
	if fileA&attrFileAllocSize != 0 {
		e.alloc = int64(le.Uint64(rec[p:]))
	}
}
