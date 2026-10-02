//go:build darwin

package sysx

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

func fakePidTable(t *testing.T, n int) {
	t.Helper()
	old := readPidTable
	readPidTable = func() ([]unix.KinfoProc, error) {
		procs := make([]unix.KinfoProc, n)
		for i := range procs {
			procs[i].Proc.P_pid = int32(i + 1)
		}
		return procs, nil
	}
	t.Cleanup(func() { readPidTable = old })
}

func TestNativeCwdCancellationDoesNotReturnPartialPaths(t *testing.T) {
	fakePidTable(t, 3)
	old := callProcInfo
	t.Cleanup(func() { callProcInfo = old })
	c := scanctl.New(scanctl.Limits{IO: 1, Commands: 1, BatchSize: 256})
	defer c.Close()
	ctx, cancel := context.WithCancel(scanctl.With(context.Background(), c))
	defer cancel()
	calls := 0
	callProcInfo = func(_ int, _ int, _ uint64, buf []byte) (int, bool) {
		calls++
		if c.Snapshot().IOActive != 1 {
			t.Error("process metadata read outside the I/O quota")
		}
		copy(buf[vnodeInfoSize:], "/partial\x00")
		cancel()
		return procVnodePathInfoSize, true
	}
	paths, ok, err := nativeCwds(ctx)
	if len(paths) != 0 || ok || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("paths=%v complete=%v calls=%d err=%v", paths, ok, calls, err)
	}
	if c.Snapshot().IOActive != 0 {
		t.Fatal("I/O slot leaked on cancellation")
	}
}

func TestNativeRegionCancellationDoesNotReturnPartialPaths(t *testing.T) {
	fakePidTable(t, 1)
	old := callProcInfo
	t.Cleanup(func() { callProcInfo = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	regions := 0
	callProcInfo = func(_ int, flavor int, addr uint64, buf []byte) (int, bool) {
		switch flavor {
		case procPidPathInfo:
			copy(buf, "/complete/app\x00")
			return 0, true
		case procPidRegionPathInfo2:
			regions++
			copy(buf[regionPathOff:], "/partial/addon.node\x00")
			binary.LittleEndian.PutUint64(buf[regionAddressOff:], addr)
			binary.LittleEndian.PutUint64(buf[regionSizeOff:], 4096)
			cancel()
			return regionWithPathInfoSize, true
		}
		return 0, false
	}
	paths, ok, err := nativeExecs(ctx)
	if len(paths) != 0 || ok || !errors.Is(err, context.Canceled) || regions != 1 {
		t.Fatalf("paths=%v complete=%v regions=%d err=%v", paths, ok, regions, err)
	}
}

func TestCancelledNativeProcessTableIsNotInspected(t *testing.T) {
	old := readPidTable
	t.Cleanup(func() { readPidTable = old })
	readPidTable = func() ([]unix.KinfoProc, error) {
		t.Fatal("cancelled query inspected the process table")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if paths, ok, err := nativeExecs(ctx); len(paths) != 0 || ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled native inspection: paths=%v complete=%v err=%v", paths, ok, err)
	}
}

func TestNativeExecsKeepsEachPIDAcrossMetadataBatches(t *testing.T) {
	fakePidTable(t, 2)
	old := callProcInfo
	t.Cleanup(func() { callProcInfo = old })
	c := scanctl.New(scanctl.Limits{IO: 1, Commands: 1, BatchSize: 256})
	defer c.Close()
	ctx := scanctl.With(context.Background(), c)
	regions := map[int]int{}
	callProcInfo = func(pid, flavor int, addr uint64, buf []byte) (int, bool) {
		if c.Snapshot().IOActive != 1 {
			t.Error("metadata read outside the I/O quota")
		}
		if flavor == procPidPathInfo {
			copy(buf, "/app"+strconv.Itoa(pid)+"\x00")
			return 0, true
		}
		if regions[pid] == 600 {
			return 0, false
		}
		regions[pid]++
		copy(buf[regionPathOff:], "/shared/library.dylib\x00")
		binary.LittleEndian.PutUint64(buf[regionAddressOff:], addr)
		binary.LittleEndian.PutUint64(buf[regionSizeOff:], 4096)
		return regionWithPathInfoSize, true
	}
	paths, ok, err := nativeExecs(ctx)
	if !ok || err != nil || len(paths) != 4 || regions[1] != 600 || regions[2] != 600 {
		t.Fatalf("paths=%v regions=%v complete=%v err=%v", paths, regions, ok, err)
	}
	seen := map[string]int{}
	for _, p := range paths {
		seen[p.pid]++
	}
	if seen["1"] != 2 || seen["2"] != 2 {
		t.Fatalf("paths assigned to wrong PID: %v", paths)
	}
}
