//go:build darwin

package sysx

import (
	"bytes"
	"encoding/binary"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Native process inspection through proc_info(2) (what libproc's
// proc_pidinfo / proc_pidpath use): the current directory, the main
// executable and the mapped files (libraries, .node addons, mmapped data) of
// every process we are allowed to inspect, without spawning lsof.

const (
	sysProcInfo            = 336
	procInfoCallPidInfo    = 2
	procPidVnodePathInfo   = 9
	procPidPathInfo        = 11
	procPidRegionPathInfo2 = 22 // PROC_PIDREGIONPATHINFO2: next region backed by a vnode (what lsof uses)
	maxPathLen             = 1024
	vnodeInfoSize          = 152 // struct vnode_info
	vnodeInfoPathSize      = vnodeInfoSize + maxPathLen
	procVnodePathInfoSize  = 2 * vnodeInfoPathSize // cdir + rdir
	procPidPathInfoSize    = 4 * maxPathLen

	// struct proc_regionwithpathinfo = struct proc_regioninfo (96 bytes,
	// pri_address at 80, pri_size at 88) + struct vnode_info_path.
	regionInfoSize         = 96
	regionAddressOff       = 80
	regionSizeOff          = 88
	regionWithPathInfoSize = regionInfoSize + vnodeInfoPathSize
	regionPathOff          = regionInfoSize + vnodeInfoSize

	// maxRegionsPerProc bounds the walk of one address space (a process
	// maps a few hundred files at most; this only guards against a loop).
	maxRegionsPerProc = 20000
)

func procInfo(pid int, flavor int, arg uint64, buf []byte) (int, bool) {
	n, _, errno := syscall.Syscall6(sysProcInfo, procInfoCallPidInfo, uintptr(pid), uintptr(flavor), uintptr(arg),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return 0, false
	}
	return int(n), true // PROC_PIDPATHINFO returns 0 on success
}

func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// allPids lists the pids of every process; ok is false when the process
// table cannot be read.
func allPids() ([]int, bool) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil || len(procs) == 0 {
		return nil, false
	}
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		if pid := int(p.Proc.P_pid); pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, true
}

// nativeCwds returns the current directory of every inspectable process.
func nativeCwds() (cwds []procPath, ok bool) {
	pids, ok := allPids()
	if !ok {
		return nil, false
	}
	vbuf := make([]byte, procVnodePathInfoSize)
	for _, pid := range pids {
		if n, ok := procInfo(pid, procPidVnodePathInfo, 0, vbuf); ok && n >= vnodeInfoPathSize {
			if cwd := cstring(vbuf[vnodeInfoSize:vnodeInfoPathSize]); cwd != "" {
				cwds = append(cwds, procPath{pid: itoa(pid), path: cwd})
			}
		}
	}
	return cwds, true
}

// nativeExecs returns, for every inspectable process, its main executable
// and every file mapped in its address space: loaded libraries and native
// addons (.node, .so, .dylib), mmapped files. This is what `lsof -d txt`
// reports.
func nativeExecs() (execs []procPath, ok bool) {
	pids, ok := allPids()
	if !ok {
		return nil, false
	}
	pbuf := make([]byte, procPidPathInfoSize)
	rbuf := make([]byte, regionWithPathInfoSize)
	seen := map[string]bool{}
	for _, pid := range pids {
		spid := itoa(pid)
		clear(seen)
		add := func(p string) {
			if p != "" && !seen[p] {
				seen[p] = true
				execs = append(execs, procPath{pid: spid, path: p})
			}
		}
		pbuf[0] = 0
		if _, ok := procInfo(pid, procPidPathInfo, 0, pbuf); ok {
			add(cstring(pbuf))
		}
		var addr uint64
		for i := 0; i < maxRegionsPerProc; i++ {
			n, ok := procInfo(pid, procPidRegionPathInfo2, addr, rbuf)
			if !ok || n < regionWithPathInfoSize {
				break // end of the address space, or not allowed to inspect
			}
			add(cstring(rbuf[regionPathOff : regionPathOff+maxPathLen]))
			start := binary.LittleEndian.Uint64(rbuf[regionAddressOff:])
			size := binary.LittleEndian.Uint64(rbuf[regionSizeOff:])
			next := start + size
			if size == 0 || next <= addr {
				break
			}
			addr = next
		}
	}
	return execs, true
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if i == len(b) {
		return "0"
	}
	return string(b[i:])
}
