//go:build darwin

package sysx

import (
	"bytes"
	"context"
	"encoding/binary"
	"syscall"
	"unsafe"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
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
	processBatchSize  = 256
)

var (
	readPidTable = func() ([]unix.KinfoProc, error) { return unix.SysctlKinfoProcSlice("kern.proc.all") }
	callProcInfo = procInfo
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
func allPids(ctx context.Context) ([]int, bool, error) {
	var procs []unix.KinfoProc
	err := scanctl.DoIO(ctx, func() error {
		var err error
		procs, err = readPidTable()
		return err
	})
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if err != nil || len(procs) == 0 {
		return nil, false, nil
	}
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if pid := int(p.Proc.P_pid); pid > 0 {
			pids = append(pids, pid)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return pids, true, nil
}

// nativeCwds returns the current directory of every inspectable process.
func nativeCwds(ctx context.Context) (cwds []procPath, ok bool, err error) {
	pids, ok, err := allPids(ctx)
	if !ok || err != nil {
		return nil, false, err
	}
	vbuf := make([]byte, procVnodePathInfoSize)
	for first := 0; first < len(pids); first += processBatchSize {
		err := scanctl.DoIO(ctx, func() error {
			for _, pid := range pids[first:min(first+processBatchSize, len(pids))] {
				if err := ctx.Err(); err != nil {
					return err
				}
				if n, ok := callProcInfo(pid, procPidVnodePathInfo, 0, vbuf); ok && n >= vnodeInfoPathSize {
					if cwd := cstring(vbuf[vnodeInfoSize:vnodeInfoPathSize]); cwd != "" {
						cwds = append(cwds, procPath{pid: itoa(pid), path: cwd})
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return cwds, true, nil
}

// nativeExecs returns, for every inspectable process, its main executable
// and every file mapped in its address space: loaded libraries and native
// addons (.node, .so, .dylib), mmapped files. This is what `lsof -d txt`
// reports.
func nativeExecs(ctx context.Context) (execs []procPath, ok bool, err error) {
	pids, ok, err := allPids(ctx)
	if !ok || err != nil {
		return nil, false, err
	}
	pbuf := make([]byte, procPidPathInfoSize)
	rbuf := make([]byte, regionWithPathInfoSize)
	seen := map[string]bool{}
	spid := ""
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			execs = append(execs, procPath{pid: spid, path: p})
		}
	}
	var addr uint64
	pidIndex, regions := 0, 0
	pathRead, incomplete := false, false
	for pidIndex < len(pids) {
		err := scanctl.DoIO(ctx, func() error {
			for work := 0; work < processBatchSize && pidIndex < len(pids); work++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				pid := pids[pidIndex]
				if !pathRead {
					clear(seen)
					spid, addr, regions = itoa(pid), 0, 0
					pbuf[0] = 0
					if _, ok := callProcInfo(pid, procPidPathInfo, 0, pbuf); ok {
						add(cstring(pbuf))
					}
					pathRead = true
					continue
				}
				regions++
				m, ok := callProcInfo(pid, procPidRegionPathInfo2, addr, rbuf)
				if !ok || m < regionWithPathInfoSize {
					pidIndex++ // end of the address space, or not allowed to inspect
					pathRead = false
					continue
				}
				add(cstring(rbuf[regionPathOff : regionPathOff+maxPathLen]))
				start := binary.LittleEndian.Uint64(rbuf[regionAddressOff:])
				size := binary.LittleEndian.Uint64(rbuf[regionSizeOff:])
				next := start + size
				if size == 0 || next <= addr {
					pidIndex++
					pathRead = false
					continue
				}
				if regions >= maxRegionsPerProc {
					incomplete = true
					return nil
				}
				addr = next
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
			return nil, false, nil // bounded walk was incomplete: use the fallback
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return execs, true, nil
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
