package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func TestDoctorNoScanStopsAfterCanceledInspection(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.app.Snapshots = func(context.Context) []string {
		cancel()
		return nil
	}
	if code := h.app.Run(ctx, []string{"doctor", "--no-scan", "--json"}); code != ExitInterrupted {
		t.Fatalf("canceled doctor returned %d, stdout=%s, stderr=%s", code, &h.out, &h.errOut)
	}
	if h.out.Len() != 0 {
		t.Fatalf("published a report after cancellation: %s", &h.out)
	}
}

func TestDoctorSharesControllerWithProcessChecksAndCollect(t *testing.T) {
	var controller *scanctl.Controller
	check := func(ctx context.Context) {
		t.Helper()
		if got := scanctl.From(ctx); got == nil || got != controller {
			t.Fatalf("inspection received a different controller: got %p, want %p", got, controller)
		}
	}
	p := &scanProbeProvider{fakeProvider: fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}}}
	providerCalled := false
	p.scan = func(ctx context.Context, _ *core.Env, _ core.Emit) error {
		check(ctx)
		providerCalled = true
		return nil
	}
	h := newHarness(t, p)
	h.app.Snapshots = func(ctx context.Context) []string {
		controller = scanctl.From(ctx)
		return nil
	}
	calls := 0
	h.app.Running = func(ctx context.Context, _ ...string) ([]string, error) {
		check(ctx)
		calls++
		return nil, nil
	}
	if code := h.run("doctor", "--json"); code != ExitOK || !providerCalled || calls != len(blockers) {
		t.Fatalf("exit=%d provider=%v process checks=%d stderr=%s", code, providerCalled, calls, &h.errOut)
	}
}

func TestDoctorCancelsProcessInspection(t *testing.T) {
	for _, noScan := range []bool{false, true} {
		name := "with scan"
		if noScan {
			name = "without scan"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			h.app.Running = func(ctx context.Context, _ ...string) ([]string, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			args := []string{"doctor", "--json"}
			if noScan {
				args = append(args, "--no-scan")
			}
			done := make(chan int, 1)
			go func() { done <- h.app.Run(ctx, args) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("process inspection never started")
			}
			cancel()
			select {
			case code := <-done:
				if code != ExitInterrupted || h.out.Len() != 0 {
					t.Fatalf("canceled inspection: exit=%d stdout=%s stderr=%s", code, &h.out, &h.errOut)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("doctor did not join the canceled inspection within 100 ms")
			}
		})
	}
}

func TestDoctorReportsIncompleteProcessInspection(t *testing.T) {
	p := &fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}, err: errors.New("provider unavailable")}
	h := newHarness(t, p)
	h.app.Running = func(context.Context, ...string) ([]string, error) {
		return nil, errors.New("process list unavailable")
	}
	if code := h.run("doctor", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, &h.errOut)
	}
	var report doctorReport
	if err := json.Unmarshal(h.out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Errors["processes"] != "process list unavailable" || report.Errors["probe"] != "provider unavailable" {
		t.Fatalf("lost inspection or provider failure: %+v", report.Errors)
	}
	if code := h.run("doctor", "--no-scan"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, &h.errOut)
	}
	if !strings.Contains(h.out.String(), "unknown — process inspection incomplete: process list unavailable") {
		t.Fatalf("incomplete inspection presented as absence of apps: %s", &h.out)
	}
}
