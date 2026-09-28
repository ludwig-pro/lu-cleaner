//go:build darwin

package fsx

import (
	"encoding/binary"
	"os"
	"sync"
	"sync/atomic"
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

	// Extended common attributes (ATTR_CMNEXT_*, sys/attr.h): with
	// FSOPT_ATTR_CMN_EXTENDED they are requested through the forkattr field
	// and packed after the file attributes, in bit order.
	attrCmnExtPrivateSize = 0x00000008 // off_t: bytes shared with no other file (costly: walks the extents)
	attrCmnExtCloneID     = 0x00000100 // u_int64_t: id of the data stream (shared by clones)
	attrCmnExtExtFlags    = 0x00000200 // u_int64_t: EF_* flags (sys/stat.h)
	attrCmnExtCloneRefcnt = 0x00001000 // u_int32_t: inodes sharing that data stream (0 = none)

	efMayShareBlocks  = 0x00000001 // the file was cloned (or is a clone) at some point
	efSharesAllBlocks = 0x00000040 // every block is shared with another file

	fsoptNoFollow        = 0x00000001
	fsoptAttrCmnExtended = 0x00000020

	vREG = 1
	vDIR = 2
	vLNK = 5
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

// bulkAttrsExt adds the cheap APFS clone attributes (see Stats.Reclaim).
// ATTR_CMNEXT_PRIVATESIZE is left out: it more than doubles the walk time,
// and is only needed for the few files that may share part of their blocks
// (privateAt fetches it for the big ones, see privateMinAlloc).
var bulkAttrsExt = attrList{
	bitmapCount: attrBitMapCount,
	commonAttr:  bulkAttrs.commonAttr,
	fileAttr:    bulkAttrs.fileAttr,
	forkAttr:    attrCmnExtCloneID | attrCmnExtExtFlags | attrCmnExtCloneRefcnt,
}

// privateAttrs fetches the private size of one file.
var privateAttrs = attrList{
	bitmapCount: attrBitMapCount,
	commonAttr:  attrCmnReturnedAttrs,
	forkAttr:    attrCmnExtPrivateSize,
}

// shareAttrs fetches every clone attribute of one file (see shareOf).
var shareAttrs = attrList{
	bitmapCount: attrBitMapCount,
	commonAttr:  attrCmnReturnedAttrs,
	fileAttr:    attrFileAllocSize,
	forkAttr:    attrCmnExtPrivateSize | bulkAttrsExt.forkAttr,
}

// useCloneAttrs requests the clone attributes; LU_NO_CLONES=1 disables them
// (sizes then count every clone as fully reclaimable, as du does).
var useCloneAttrs = os.Getenv("LU_NO_CLONES") == ""

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

	// APFS clone information (when the volume returns it).
	hasClone   bool   // cloneID, extFlags and cloneRefs were all returned
	cloneID    uint64 // data stream id
	extFlags   uint64 // EF_* flags
	cloneRefs  uint32 // inodes sharing the data stream (0 = none, e.g. a compressed file)
	hasPrivate bool
	private    int64 // bytes held by no other file
}

// privateMinAlloc is the allocated size below which the private size of a
// file is not fetched (the file then counts fully, as without clone
// support). EF_MAY_SHARE_BLOCKS is sticky: it stays set once every other
// copy is gone, so after the bun cache or pnpm store was emptied, every file
// of a node_modules installed from it carries it while sharing nothing, and
// fetching the private size of each (a walk of its extents) made such walks
// about ten times slower. Big files, where a partly shared clone (a cloned
// simulator or VM image modified afterwards) really matters, are only a few
// percent of the files of such trees. The error is bounded: under 128 KiB
// per partly shared small file, counted as reclaimable.
const privateMinAlloc = 128 << 10

// privateFetches counts privateAt calls (tests).
var privateFetches atomic.Int64

// needsPrivate reports whether the private size must be fetched to know
// what deleting the file frees: a big enough file with its own data stream
// that may share some, not all, of its blocks (a clone modified afterwards,
// or its origin).
func (e *bulkEntry) needsPrivate() bool {
	return e.objType == vREG && e.hasClone && !e.hasPrivate && e.cloneRefs == 1 &&
		e.extFlags&efMayShareBlocks != 0 && e.extFlags&efSharesAllBlocks == 0 &&
		e.alloc >= privateMinAlloc
}

// share converts the clone attributes of a regular file into a fileShare.
func (e *bulkEntry) share() fileShare {
	if !e.hasClone {
		return fileShare{}
	}
	s := fileShare{known: true, cloneID: e.cloneID, refs: e.cloneRefs}
	switch {
	case e.cloneRefs != 1:
		// 0: no data stream (fileShare.exclusive counts the file fully);
		// > 1: a data stream shared with other inodes, resolved per family.
	case e.extFlags&efMayShareBlocks == 0:
		s.private = e.alloc // never cloned: nothing shared
	case e.extFlags&efSharesAllBlocks != 0:
		s.private = 0
	case e.hasPrivate:
		s.private = e.private
	default:
		s.private = e.alloc // unknown: count it, as before clone support
	}
	return s
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
	attrs, opts := &bulkAttrs, uintptr(0)
	if useCloneAttrs {
		attrs, opts = &bulkAttrsExt, fsoptAttrCmnExtended
	}
	started := false
	for {
		n, _, errno := syscall.Syscall6(unix.SYS_GETATTRLISTBULK, uintptr(fd),
			uintptr(unsafe.Pointer(attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), opts, 0)
		if errno == syscall.EINVAL && opts != 0 && !started {
			// This volume does not take the extended attributes: plain request.
			attrs, opts = &bulkAttrs, 0
			continue
		}
		if errno != 0 {
			if !started && (errno == syscall.ENOTSUP || errno == syscall.ENOSYS || errno == syscall.EINVAL) {
				return false, nil
			}
			// Once entries were reported, falling back would count them twice.
			return true, &os.PathError{Op: "getattrlistbulk", Path: dir, Err: errno}
		}
		started = true
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
			if e.needsPrivate() && e.name != "" {
				e.private, e.hasPrivate = privateAt(fd, e.name)
			}
			fn(&e)
			off += length
		}
	}
}

// privateAt returns the private size of the entry name of directory dirfd.
func privateAt(dirfd int, name string) (int64, bool) {
	privateFetches.Add(1)
	p, err := unix.BytePtrFromString(name)
	if err != nil {
		return 0, false
	}
	var buf [64]byte
	_, _, errno := syscall.Syscall6(unix.SYS_GETATTRLISTAT, uintptr(dirfd), uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&privateAttrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		fsoptNoFollow|fsoptAttrCmnExtended)
	if errno != 0 {
		return 0, false
	}
	var e bulkEntry
	if !decodeOne(buf[:], &e) || !e.hasPrivate {
		return 0, false
	}
	return e.private, true
}

// shareOf returns the clone information of one regular file (not following
// a final symlink) through getattrlist(2); the zero value when unavailable.
func shareOf(path string) fileShare {
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return fileShare{}
	}
	var buf [128]byte
	_, _, errno := syscall.Syscall6(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&shareAttrs)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), fsoptNoFollow|fsoptAttrCmnExtended, 0)
	if errno != 0 {
		return fileShare{}
	}
	var e bulkEntry
	if !decodeOne(buf[:], &e) {
		return fileShare{}
	}
	e.objType = vREG
	return e.share()
}

// decodeOne decodes a getattrlist(2) result (a single record).
func decodeOne(buf []byte, e *bulkEntry) bool {
	n := int(binary.LittleEndian.Uint32(buf))
	if n < 24 || n > len(buf) {
		return false
	}
	decodeBulk(buf[:n], e)
	return !e.hasError
}

func decodeBulk(rec []byte, e *bulkEntry) {
	*e = bulkEntry{}
	le := binary.LittleEndian
	p := 0
	// need reports whether n more bytes are available; a truncated record is
	// flagged as an error rather than read out of bounds.
	need := func(n int) bool {
		if p+n > len(rec) {
			e.hasError = true
			return false
		}
		return true
	}
	if !need(24) {
		return
	}
	// u_int32_t length, then attribute_set_t (returned attributes).
	common := le.Uint32(rec[4:])
	fileA := le.Uint32(rec[16:])
	extA := le.Uint32(rec[20:]) // forkattr = ATTR_CMNEXT_* with FSOPT_ATTR_CMN_EXTENDED
	p = 24
	if common&attrCmnError != 0 {
		if !need(4) {
			return
		}
		if le.Uint32(rec[p:]) != 0 {
			e.hasError = true
		}
		p += 4
	}
	if common&attrCmnName != 0 {
		if !need(8) {
			return
		}
		nameOff := int(int32(le.Uint32(rec[p:])))
		nameLen := int(le.Uint32(rec[p+4:]))
		start := p + nameOff
		if nameLen > 0 && start >= 0 && start+nameLen <= len(rec) {
			e.name = string(rec[start : start+nameLen-1]) // drop NUL
		}
		p += 8
	}
	if common&attrCmnDevID != 0 {
		if !need(4) {
			return
		}
		e.dev = int32(le.Uint32(rec[p:]))
		p += 4
	}
	if common&attrCmnObjType != 0 {
		if !need(4) {
			return
		}
		e.objType = le.Uint32(rec[p:])
		p += 4
	}
	if common&attrCmnModTime != 0 {
		if !need(16) {
			return
		}
		sec := int64(le.Uint64(rec[p:]))
		nsec := int64(le.Uint64(rec[p+8:]))
		e.mtimeNs = sec*1e9 + nsec
		p += 16
	}
	if common&attrCmnFileID != 0 {
		if !need(8) {
			return
		}
		e.ino = le.Uint64(rec[p:])
		p += 8
	}
	if fileA&attrFileLinkCount != 0 {
		if !need(4) {
			return
		}
		e.nlink = le.Uint32(rec[p:])
		p += 4
	}
	if fileA&attrFileTotalSize != 0 {
		if !need(8) {
			return
		}
		e.size = int64(le.Uint64(rec[p:]))
		p += 8
	}
	if fileA&attrFileAllocSize != 0 {
		if !need(8) {
			return
		}
		e.alloc = int64(le.Uint64(rec[p:]))
		p += 8
	}
	if extA&attrCmnExtPrivateSize != 0 {
		if !need(8) {
			return
		}
		e.private = int64(le.Uint64(rec[p:]))
		e.hasPrivate = true
		p += 8
	}
	const cloneAttrs = attrCmnExtCloneID | attrCmnExtExtFlags | attrCmnExtCloneRefcnt
	if extA&attrCmnExtCloneID != 0 {
		if !need(8) {
			return
		}
		e.cloneID = le.Uint64(rec[p:])
		p += 8
	}
	if extA&attrCmnExtExtFlags != 0 {
		if !need(8) {
			return
		}
		e.extFlags = le.Uint64(rec[p:])
		p += 8
	}
	if extA&attrCmnExtCloneRefcnt != 0 {
		if !need(4) {
			return
		}
		e.cloneRefs = le.Uint32(rec[p:])
	}
	e.hasClone = extA&cloneAttrs == cloneAttrs
}
