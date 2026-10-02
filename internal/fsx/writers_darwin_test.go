//go:build darwin

package fsx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

func TestWriterInspectionCancellationDoesNotReturnPartialPaths(t *testing.T) {
	oldTable, oldInfo := readWriterTable, writerProcInfo
	t.Cleanup(func() { readWriterTable, writerProcInfo = oldTable, oldInfo })
	readWriterTable = func() ([]unix.KinfoProc, error) {
		procs := make([]unix.KinfoProc, 2)
		procs[0].Proc.P_pid, procs[1].Proc.P_pid = 10, 11
		return procs, nil
	}
	c := scanctl.New(scanctl.Limits{IO: 1, Commands: 1, BatchSize: 256})
	defer c.Close()
	ctx, cancel := context.WithCancel(scanctl.With(context.Background(), c))
	defer cancel()
	fdCalls := 0
	writerProcInfo = func(call, _ int, _ int, _ uint64, buf []byte) (int, syscall.Errno) {
		if c.Snapshot().IOActive != 1 {
			t.Error("writer metadata read outside the I/O quota")
		}
		if call == procInfoCallPidInfo {
			if buf != nil {
				for i := range 3 {
					binary.LittleEndian.PutUint32(buf[i*procFdInfoSize:], uint32(i))
					binary.LittleEndian.PutUint32(buf[i*procFdInfoSize+4:], proxFdTypeVnode)
				}
			}
			return 3 * procFdInfoSize, 0
		}
		fdCalls++
		binary.LittleEndian.PutUint32(buf, fWrite)
		copy(buf[vnodeFdPathOff:], "/partial/db\x00")
		cancel()
		return vnodeFdInfoWithPathSize, 0
	}
	paths, ok, err := openForWriting(ctx)
	if len(paths) != 0 || ok || !errors.Is(err, context.Canceled) || fdCalls != 1 {
		t.Fatalf("paths=%v complete=%v fd calls=%d err=%v", paths, ok, fdCalls, err)
	}
	if c.Snapshot().IOActive != 0 {
		t.Fatal("I/O slot leaked on writer cancellation")
	}
}

func TestCancelledWriterInspectionDoesNotReadProcessTable(t *testing.T) {
	old := readWriterTable
	t.Cleanup(func() { readWriterTable = old })
	readWriterTable = func() ([]unix.KinfoProc, error) { t.Fatal("cancelled query inspected processes"); return nil, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if paths, ok, err := openForWriting(ctx); len(paths) != 0 || ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled writer inspection: paths=%v complete=%v err=%v", paths, ok, err)
	}
}

func TestWriterInspectionKeepsFDsAcrossMetadataBatches(t *testing.T) {
	oldTable, oldInfo := readWriterTable, writerProcInfo
	t.Cleanup(func() { readWriterTable, writerProcInfo = oldTable, oldInfo })
	readWriterTable = func() ([]unix.KinfoProc, error) {
		procs := make([]unix.KinfoProc, 3)
		for i := range procs {
			procs[i].Proc.P_pid = int32(i + 1)
		}
		return procs, nil
	}
	c := scanctl.New(scanctl.Limits{IO: 1, Commands: 1, BatchSize: 256})
	defer c.Close()
	ctx := scanctl.With(context.Background(), c)
	writerProcInfo = func(call, pid int, _ int, fd uint64, buf []byte) (int, syscall.Errno) {
		if c.Snapshot().IOActive != 1 {
			t.Error("metadata read outside the I/O quota")
		}
		if call == procInfoCallPidInfo {
			if buf != nil {
				for i := range 300 {
					binary.LittleEndian.PutUint32(buf[i*procFdInfoSize:], uint32(i))
					binary.LittleEndian.PutUint32(buf[i*procFdInfoSize+4:], proxFdTypeVnode)
				}
			}
			return 300 * procFdInfoSize, 0
		}
		clear(buf)
		binary.LittleEndian.PutUint32(buf, fWrite)
		copy(buf[vnodeFdPathOff:], fmt.Sprintf("/pid%d/fd%d", pid, fd))
		return vnodeFdInfoWithPathSize, 0
	}
	paths, ok, err := openForWriting(ctx)
	if !ok || err != nil || len(paths) != 900 {
		t.Fatalf("%d paths complete=%v err=%v", len(paths), ok, err)
	}
	for _, pid := range []int{1, 2, 3} {
		for _, fd := range []int{0, 255, 256, 299} {
			want := fmt.Sprintf("/pid%d/fd%d", pid, fd)
			found := false
			for _, p := range paths {
				if p == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("writer lost at a batch boundary: %s", want)
			}
		}
	}
}
