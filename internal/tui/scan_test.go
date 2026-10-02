package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func TestSizePoolCloseJoinsActiveAndIdleWorkers(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "active"}[active], func(t *testing.T) {
			p := newSizePool(context.Background(), 1)
			t.Cleanup(p.close)
			started := make(chan string, 2)
			finished := make(chan struct{})
			p.sizeFn = func(ctx context.Context, path string) (fsx.Stats, error) {
				started <- path
				<-ctx.Done()
				close(finished)
				return fsx.Stats{}, ctx.Err()
			}
			if active {
				p.push("active", "queued")
				select {
				case path := <-started:
					if path != "active" {
						t.Fatalf("first path=%s", path)
					}
				case <-time.After(time.Second):
					t.Fatal("worker did not start")
				}
			}
			done := make(chan struct{})
			go func() { p.close(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("close did not join the pool")
			}
			if active {
				select {
				case <-finished:
				default:
					t.Fatal("close returned before the active worker finished")
				}
			}
			p.push("after close")
			select {
			case path := <-started:
				t.Fatalf("unexpected job after cancellation: %s", path)
			default:
			}
			for range p.out {
			}
			if msg := waitSizes(context.Background(), p)(); msg != nil {
				t.Fatalf("closed output produced a message: %#v", msg)
			}
		})
	}
}

func TestAnalyzerUsesSharedProfileForPrefetch(t *testing.T) {
	for _, mode := range []scanctl.Mode{scanctl.Eco, scanctl.Fast} {
		t.Run(string(mode), func(t *testing.T) {
			limits, _, err := scanctl.Resolve(string(mode), "")
			if err != nil {
				t.Fatal(err)
			}
			ctrl := scanctl.New(limits)
			t.Cleanup(ctrl.Close)
			ctx := scanctl.With(context.Background(), ctrl)
			env := testEnv(t.TempDir())
			env.ScanLimits = limits
			m := newAnalyzer(ctx, AnalyzeOptions{Env: env, Root: env.Home})
			t.Cleanup(m.close)
			if scanctl.From(m.ctx) != ctrl || !strings.Contains(m.viewTotal(nil, 100), string(mode)) {
				t.Fatal("analyzer lost the controller or profile label")
			}
			started := make(chan struct{}, 8)
			var active, peak atomic.Int64
			m.pool.sizeFn = func(ctx context.Context, path string) (fsx.Stats, error) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				started <- struct{}{}
				<-ctx.Done()
				return fsx.Stats{}, ctx.Err()
			}
			m.pool.push("one", "two", "three", "four", "five")
			for range limits.Prefetch {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("profile workers did not start")
				}
			}
			m.cancel()
			m.pool.close()
			if n := peak.Load(); n != int64(limits.Prefetch) || active.Load() != 0 {
				t.Fatalf("peak=%d active=%d profile prefetch=%d", n, active.Load(), limits.Prefetch)
			}
		})
	}
}

func TestAnalyzerListingCancellationWhileWaitingForIO(t *testing.T) {
	limits, _, _ := scanctl.Resolve("fast", "1")
	ctrl := scanctl.New(limits)
	t.Cleanup(ctrl.Close)
	ctx, cancel := context.WithCancel(scanctl.With(context.Background(), ctrl))
	defer cancel()
	occupied := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		_ = scanctl.DoIO(ctx, func() error { close(occupied); <-release; return nil })
		close(holderDone)
	}()
	<-occupied
	listed := make(chan dirListedMsg, 1)
	go func() { listed <- listDirContext(ctx, t.TempDir()).(dirListedMsg) }()
	cancel()
	select {
	case msg := <-listed:
		if !errors.Is(msg.err, context.Canceled) {
			t.Errorf("listing error=%v", msg.err)
		}
	case <-time.After(time.Second):
		t.Error("listing kept waiting after cancellation")
	}
	close(release)
	<-holderDone
}

func TestPickerKeepsControllerFromContext(t *testing.T) {
	limits, _, _ := scanctl.Resolve("eco", "")
	ctrl := scanctl.New(limits)
	t.Cleanup(ctrl.Close)
	ctx := scanctl.With(context.Background(), ctrl)
	m := newPicker(ctx, PickerOptions{Env: testEnv(t.TempDir()), Filter: core.Filter{MaxRisk: core.RiskNever}})
	m.scanning = true
	if scanctl.From(m.ctx) != ctrl || !strings.Contains(m.viewHeader(120), "eco") {
		t.Fatal("picker lost the controller or profile label")
	}
}

func TestAnalyzerRescanKeepsSharedController(t *testing.T) {
	f := newAnFixture(t)
	limits, _, _ := scanctl.Resolve("eco", "1")
	ctrl := scanctl.New(limits)
	t.Cleanup(ctrl.Close)
	env := testEnv(f.home)
	env.ScanLimits = limits
	m := newAnalyzer(scanctl.With(context.Background(), ctrl), AnalyzeOptions{Env: env, Root: f.root})
	t.Cleanup(m.close)
	m.diskFn = fakeDisk
	seen := make(chan *scanctl.Controller, 32)
	sizeFn := m.pool.sizeFn
	m.pool.sizeFn = func(ctx context.Context, path string) (fsx.Stats, error) {
		seen <- scanctl.From(ctx)
		return sizeFn(ctx, path)
	}
	d := newDriver(t, m)
	d.until("initial sizes", settled(m))
	firstCalls := len(seen)
	mkdirFiles(t, filepath.Join(f.root, "src"), map[string]int{"new.bin": 200000})
	mkdirFiles(t, f.root, map[string]int{"added.txt": 10})
	d.keys("r")
	d.until("rescan sizes", func() bool { return settled(m)() && m.cur().byName["added.txt"] != nil })
	if len(seen) <= firstCalls {
		t.Fatal("rescan did not queue fresh sizing work")
	}
	for len(seen) > 0 {
		if got := <-seen; got != ctrl {
			t.Fatal("rescan used another resource controller")
		}
	}
	if got := ctrl.Snapshot().IOMax; got != 1 {
		t.Fatalf("shared I/O peak=%d, want 1", got)
	}
}

type pickerScanProbe struct {
	fakeProvider
	seen chan *scanctl.Controller
}

func (p *pickerScanProbe) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	p.seen <- scanctl.From(ctx)
	return p.fakeProvider.Scan(ctx, env, emit)
}

func TestPickerCleanDuringScanSharesController(t *testing.T) {
	limits, _, _ := scanctl.Resolve("eco", "")
	ctrl := scanctl.New(limits)
	t.Cleanup(ctrl.Close)
	home := tempHome(t)
	env := testEnv(home)
	env.ScanLimits = limits
	gate := make(chan struct{})
	seen := make(chan *scanctl.Controller, 2)
	provider := &pickerScanProbe{
		fakeProvider: fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}, gate: gate,
			items: []*core.Item{mkItem("ready", core.CatArtifacts, 1000, core.RiskSafe, 0)}},
		seen: seen,
	}
	m := newPicker(scanctl.With(context.Background(), ctrl), PickerOptions{
		Env: env, Providers: []core.Provider{provider}, Flat: true,
		Filter: core.Filter{MaxRisk: core.RiskNever}, Clean: testCleanOpts(home),
	})
	m.diskFn = fakeDisk
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		seen <- scanctl.From(ctx)
		sum := &clean.Summary{DryRun: true}
		for _, it := range items {
			r := clean.Result{Item: it, Status: clean.StatusDryRun, Freed: it.Freed()}
			progress(r)
			sum.Results = append(sum.Results, r)
		}
		return sum
	}
	t.Cleanup(func() {
		if m.scanCancel != nil {
			m.scanCancel()
		}
		if m.events != nil {
			for range m.events {
			}
		}
		m.waitCleanFinished()
	})
	d := newDriver(t, m)
	d.until("first item", func() bool { return len(m.list) == 1 })
	if !m.scanning {
		t.Fatal("fixture scan already completed")
	}
	d.keys("space", "d", "y")
	d.until("clean summary while scanning", func() bool { return m.mode == modeSummary })
	if !m.scanning || m.lastSummary.Count(clean.StatusDryRun) != 1 {
		t.Fatal("clean did not finish while the scan was still active")
	}
	for range 2 {
		if got := <-seen; got != ctrl {
			t.Fatal("scan and clean did not share the invocation controller")
		}
	}
	close(gate)
	d.until("scan completion", scanDone(m))
}

func TestAnalyzerQuitJoinsSizePool(t *testing.T) {
	limits, _, _ := scanctl.Resolve("eco", "")
	ctrl := scanctl.New(limits)
	t.Cleanup(ctrl.Close)
	env := testEnv(t.TempDir())
	m := newAnalyzer(scanctl.With(context.Background(), ctrl), AnalyzeOptions{Env: env, Root: env.Home})
	t.Cleanup(m.close)
	started := make(chan struct{})
	finished := make(chan struct{})
	m.pool.sizeFn = func(ctx context.Context, path string) (fsx.Stats, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return fsx.Stats{}, ctx.Err()
	}
	m.pool.push("active", "queued")
	<-started
	_ = m.quit()
	m.close()
	select {
	case <-finished:
	default:
		t.Fatal("analyzer quit left active sizing work")
	}
	if !m.quitting || m.ctx.Err() != context.Canceled {
		t.Fatal("analyzer quit did not cancel its context")
	}
}
