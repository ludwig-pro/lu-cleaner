//go:build darwin

package fsx

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"syscall"
	"unsafe"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

// Files open for writing: a process writing to a file it keeps open (a VM
// disk, a database and its -wal file, a log) changes its size without any
// FSEvents event until it closes it (FSE_CONTENT_MODIFIED is sent on
// close; only ftruncate reports earlier). The size cache must not trust a
// tree holding such a file: openForWriting lists them through proc_info(2),
// like lsof, for every process we may inspect (the user's own processes;
// root daemons are not visible).

const (
	sysProcInfo            = 336
	procInfoCallPidInfo    = 2
	procInfoCallPidFdInfo  = 3
	procPidListFds         = 1 // PROC_PIDLISTFDS: struct proc_fdinfo[]
	procPidFdVnodePathInfo = 2 // PROC_PIDFDVNODEPATHINFO: struct vnode_fdinfowithpath
	proxFdTypeVnode        = 1
	procFdInfoSize         = 8 // struct proc_fdinfo {int32 fd; uint32 type}
	// struct vnode_fdinfowithpath = struct proc_fileinfo (24 bytes,
	// fi_openflags first) + struct vnode_info (152) + char path[1024].
	vnodeFdInfoWithPathSize = 24 + 152 + 1024
	vnodeFdPathOff          = 24 + 152
	fWrite                  = 0x0002 // FWRITE in fi_openflags
	maxFdsPerProc           = 1 << 20
	writerBatchSize         = 256
)

var (
	readWriterTable = func() ([]unix.KinfoProc, error) { return unix.SysctlKinfoProcSlice("kern.proc.all") }
	writerProcInfo  = procInfoCall
)

func procInfoCall(call, pid, flavor int, arg uint64, buf []byte) (int, syscall.Errno) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	n, _, errno := syscall.Syscall6(sysProcInfo, uintptr(call), uintptr(pid), uintptr(flavor), uintptr(arg), uintptr(p), uintptr(len(buf)))
	return int(n), errno
}

// openForWriting returns the paths of the files that inspectable processes
// hold open for writing (for a file with several hard links, the path it
// was opened by). ok is false when the process table cannot be read or an fd
// table was truncated. Cancellation returns an error and no partial paths.
func openForWriting(ctx context.Context) (paths []string, ok bool, err error) {
	var procs []unix.KinfoProc
	err = scanctl.DoIO(ctx, func() error {
		var err error
		procs, err = readWriterTable()
		return err
	})
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if err != nil || len(procs) == 0 {
		return nil, false, nil
	}
	var fds []byte
	info := make([]byte, vnodeFdInfoWithPathSize)
	seen := map[string]bool{}
	pidIndex, fdOffset, fdBytes := 0, 0, 0
	listed, incomplete := false, false
	for pidIndex < len(procs) {
		err := scanctl.DoIO(ctx, func() error {
			for work := 0; work < writerBatchSize && pidIndex < len(procs); {
				if err := ctx.Err(); err != nil {
					return err
				}
				pid := int(procs[pidIndex].Proc.P_pid)
				if pid <= 0 {
					pidIndex++
					continue
				}
				if !listed {
					// NULL buffer: the size the fd table needs now.
					work++
					need, errno := writerProcInfo(procInfoCallPidInfo, pid, procPidListFds, 0, nil)
					if errno != 0 || need <= 0 {
						pidIndex++ // not ours to inspect, or exited
						continue
					}
					if err := ctx.Err(); err != nil {
						return err
					}
					if need > maxFdsPerProc*procFdInfoSize {
						incomplete = true
						return nil
					}
					need = min(need+64*procFdInfoSize, maxFdsPerProc*procFdInfoSize)
					if len(fds) < need {
						fds = make([]byte, need)
					}
					work++
					n, errno := writerProcInfo(procInfoCallPidInfo, pid, procPidListFds, 0, fds[:need])
					if errno != 0 || n <= 0 {
						pidIndex++
						continue
					}
					if n >= need || n%procFdInfoSize != 0 {
						incomplete = true
						return nil
					}
					fdOffset, fdBytes, listed = 0, n, true
					continue
				}
				work++
				i := fdOffset
				fdOffset += procFdInfoSize
				if fdOffset == fdBytes {
					pidIndex++
					listed = false
				}
				if binary.LittleEndian.Uint32(fds[i+4:]) != proxFdTypeVnode {
					continue
				}
				fd := int32(binary.LittleEndian.Uint32(fds[i:]))
				m, errno := writerProcInfo(procInfoCallPidFdInfo, pid, procPidFdVnodePathInfo, uint64(fd), info)
				if errno != 0 || m < vnodeFdInfoWithPathSize || binary.LittleEndian.Uint32(info)&fWrite == 0 {
					continue
				}
				p := info[vnodeFdPathOff:]
				if j := bytes.IndexByte(p, 0); j >= 0 {
					p = p[:j]
				}
				if len(p) == 0 || p[0] != '/' || seen[string(p)] {
					continue
				}
				s := string(p)
				seen[s] = true
				paths = append(paths, s)
				// the data volume may be reported through its mount point
				if rest, found := strings.CutPrefix(s, "/System/Volumes/Data/"); found {
					paths = append(paths, "/"+rest)
				}
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if incomplete {
			return nil, false, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return paths, true, nil
}
