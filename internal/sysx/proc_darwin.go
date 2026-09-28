//go:build darwin

package sysx

import (
	"bytes"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Native process inspection through proc_info(2) (what libproc's
// proc_pidinfo / proc_pidpath use): the current directory and the main
// executable of every process we are allowed to inspect, in a few
// milliseconds and without spawning lsof.

const (
	sysProcInfo           = 336
	procInfoCallPidInfo   = 2
	procPidVnodePathInfo  = 9
	procPidPathInfo       = 11
	maxPathLen            = 1024
	vnodeInfoSize         = 152 // struct vnode_info
	vnodeInfoPathSize     = vnodeInfoSize + maxPathLen
	procVnodePathInfoSize = 2 * vnodeInfoPathSize // cdir + rdir
	procPidPathInfoSize   = 4 * maxPathLen
)

func procInfo(pid int, flavor int, buf []byte) (int, bool) {
	n, _, errno := syscall.Syscall6(sysProcInfo, procInfoCallPidInfo, uintptr(pid), uintptr(flavor), 0,
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

// nativeProcPaths returns (cwd, exec) path lists for all inspectable
// processes; ok is false when the process table cannot be read.
func nativeProcPaths() (cwds, execs []procPath, ok bool) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil || len(procs) == 0 {
		return nil, nil, false
	}
	vbuf := make([]byte, procVnodePathInfoSize)
	pbuf := make([]byte, procPidPathInfoSize)
	for _, p := range procs {
		pid := int(p.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		spid := itoa(pid)
		if n, ok := procInfo(pid, procPidVnodePathInfo, vbuf); ok && n >= vnodeInfoPathSize {
			if cwd := cstring(vbuf[vnodeInfoSize:vnodeInfoPathSize]); cwd != "" {
				cwds = append(cwds, procPath{pid: spid, path: cwd})
			}
		}
		pbuf[0] = 0
		if _, ok := procInfo(pid, procPidPathInfo, pbuf); ok {
			if exe := cstring(pbuf); exe != "" {
				execs = append(execs, procPath{pid: spid, path: exe})
			}
		}
	}
	return cwds, execs, true
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
