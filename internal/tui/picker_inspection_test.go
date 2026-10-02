package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func pickerWithGuardedSelection(t *testing.T) *pickerModel {
	t.Helper()
	m := newTestPicker(t, PickerOptions{Flat: true})
	it := mkItem("guarded", core.CatXcode, 1e9, core.RiskSafe, 0)
	it.ProcessGuard = []string{"Xcode"}
	m.items[it.ID] = it
	m.order = append(m.order, it.ID)
	m.selected[it.ID] = true
	m.dirtyData, m.dirtySel = true, true
	return m
}

func TestPickerCloseJoinsConfirmationInspectionWithoutCmd(t *testing.T) {
	m := pickerWithGuardedSelection(t)
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	controller := scanctl.From(m.ctx)
	m.runningFn = func(ctx context.Context, _ ...string) ([]string, error) {
		if controller == nil || scanctl.From(ctx) != controller {
			t.Error("confirmation inspection lost the invocation controller")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		close(finished)
		return nil, ctx.Err()
	}
	// Bubble Tea may abandon this Cmd on exit. The task must still be joined.
	if m.openConfirm() == nil {
		t.Fatal("confirmation did not start an inspection")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("inspection never started")
	}
	closed := make(chan struct{})
	go func() { m.close(); close(closed) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel the inspection")
	}
	select {
	case <-closed:
		t.Fatal("close returned before the inspection finished")
	default:
	}
	release <- struct{}{}
	select {
	case <-closed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("close did not join the canceled inspection within 100 ms")
	}
	select {
	case <-finished:
	default:
		t.Fatal("inspection survived model closure")
	}
}

func TestPickerAbandonedConfirmationCannotReplaceFreshInspection(t *testing.T) {
	m := pickerWithGuardedSelection(t)
	started := make(chan struct{})
	m.runningFn = func(ctx context.Context, _ ...string) ([]string, error) {
		close(started)
		<-ctx.Done()
		return []string{"stale"}, nil
	}
	old := m.openConfirm()
	<-started
	m.cancelConfirm()
	m.runningFn = func(context.Context, ...string) ([]string, error) {
		return nil, errors.New("process list unavailable")
	}
	fresh := m.openConfirm()
	m.Update(fresh())
	m.Update(old())
	if m.confirm.checking || len(m.confirm.running) != 0 || m.confirm.runningErr == nil || m.confirm.runningErr.Error() != "process list unavailable" {
		t.Fatalf("stale result replaced current inspection: %+v", m.confirm)
	}
	if !strings.Contains(m.View(), "unknown — process inspection incomplete:") {
		t.Fatalf("inspection failure was hidden: %s", m.View())
	}
}
