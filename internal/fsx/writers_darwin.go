//go:build darwin

package fsx

import (
	"bytes"
	"encoding/binary"
	"strings"
	"syscall"
	"unsafe"

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
// was opened by). ok is false when the process table cannot be read.
func openForWriting() (paths []string, ok bool) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil || len(procs) == 0 {
		return nil, false
	}
	var fds []byte
	info := make([]byte, vnodeFdInfoWithPathSize)
	seen := map[string]bool{}
	for _, kp := range procs {
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		// NULL buffer: the size the fd table needs now.
		need, errno := procInfoCall(procInfoCallPidInfo, pid, procPidListFds, 0, nil)
		if errno != 0 || need <= 0 {
			continue // not ours to inspect, or exited
		}
		need = min(need+64*procFdInfoSize, maxFdsPerProc*procFdInfoSize)
		if len(fds) < need {
			fds = make([]byte, need)
		}
		n, errno := procInfoCall(procInfoCallPidInfo, pid, procPidListFds, 0, fds[:need])
		if errno != 0 {
			continue
		}
		for i := 0; i+procFdInfoSize <= n; i += procFdInfoSize {
			if binary.LittleEndian.Uint32(fds[i+4:]) != proxFdTypeVnode {
				continue
			}
			fd := int32(binary.LittleEndian.Uint32(fds[i:]))
			m, errno := procInfoCall(procInfoCallPidFdInfo, pid, procPidFdVnodePathInfo, uint64(fd), info)
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
	}
	return paths, true
}
