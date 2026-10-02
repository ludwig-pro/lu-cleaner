package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func TestAnalyzerCloseJoinsListingAndNestedChecks(t *testing.T) {
	for _, kind := range []string{"listing", "nested"} {
		for _, executeCmd := range []bool{false, true} {
			name := kind + "/abandoned-cmd"
			if executeCmd {
				name = kind + "/running-cmd"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				root := mkdirFiles(t, filepath.Join(home, "root"), map[string]int{"plain/file": 1})
				m := newTestAnalyzer(t, root, home)
				started, canceled := make(chan struct{}), make(chan struct{})
				release, finished := make(chan struct{}), make(chan struct{})
				// Always release a failed test before the model cleanup joins it.
				t.Cleanup(func() { close(release) })
				wait := func(ctx context.Context) {
					close(started)
					<-ctx.Done()
					close(canceled)
					<-release
					close(finished)
				}
				var cmd tea.Cmd
				if kind == "listing" {
					m.listFn = func(ctx context.Context, path string) tea.Msg {
						wait(ctx)
						return dirListedMsg{path: path, err: ctx.Err()}
					}
					cmd = m.enter(root, "")
				} else {
					path := filepath.Join(root, "plain")
					m.marked[path] = &anEntry{name: "plain", path: path, isDir: true}
					m.nestedFn = func(string) (string, error) { wait(m.ctx); return "", m.ctx.Err() }
					cmd = m.openConfirm()
				}
				if cmd == nil {
					t.Fatal("scan task was not admitted")
				}
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("scan work depended on executing its Cmd")
				}
				var cmdDone chan struct{}
				if executeCmd {
					cmdDone = make(chan struct{})
					go func() { cmd(); close(cmdDone) }()
				}
				closed := make(chan struct{})
				go func() { m.close(); close(closed) }()
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("close did not cancel scan work")
				}
				select {
				case <-closed:
					t.Fatal("close returned before the scan task finished")
				case <-time.After(20 * time.Millisecond):
				}
				// Releasing on cleanup avoids double-close if an assertion fails.
				release <- struct{}{}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("close did not join completed scan work")
				}
				select {
				case <-finished:
				default:
					t.Fatal("scan work survived model close")
				}
				if cmdDone != nil {
					select {
					case <-cmdDone:
					case <-time.After(time.Second):
						t.Fatal("Cmd kept waiting for a completed scan task")
					}
				}
				if got := m.tasks.run(func() tea.Msg { t.Error("scan admitted after close"); return nil }); got != nil {
					t.Fatal("closed group returned another scan command")
				}
			})
		}
	}
}

func TestScanTaskGroupClosedBeforeAdmission(t *testing.T) {
	g := newScanTaskGroup(context.Background())
	g.close()
	if cmd := g.run(func() tea.Msg { t.Error("work started after close"); return nil }); cmd != nil {
		t.Fatal("closed group accepted work")
	}
}

func TestScanTaskGroupPreservesCmdPanic(t *testing.T) {
	g := newScanTaskGroup(context.Background())
	cmd := g.run(func() tea.Msg { panic("scan panic") })
	g.close()
	defer func() {
		if got := recover(); got != "scan panic" {
			t.Fatalf("Cmd panic = %v", got)
		}
	}()
	cmd()
}

func TestAnalyzerRescanJoinsOldPoolAndRejectsOldResults(t *testing.T) {
	home := t.TempDir()
	root := mkdirFiles(t, filepath.Join(home, "root"), map[string]int{"child/file": 1})
	limits, _, _ := scanctl.Resolve("eco", "1")
	ctrl := scanctl.New(limits)
	t.Cleanup(ctrl.Close)
	m := newAnalyzer(scanctl.With(context.Background(), ctrl), AnalyzeOptions{Env: testEnv(home), Root: root})
	t.Cleanup(m.close)
	m.diskFn = fakeDisk
	child, active, queued := filepath.Join(root, "child"), filepath.Join(root, "active"), filepath.Join(root, "queued")
	old := m.pool
	started, canceled := make(chan struct{}), make(chan struct{})
	release, finished := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	called := make(chan string, 8)
	old.sizeFn = func(ctx context.Context, path string) (fsx.Stats, error) {
		called <- path
		if scanctl.From(ctx) != ctrl {
			t.Error("pool lost the invocation controller")
		}
		if path == active {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			close(finished)
			return fsx.Stats{Bytes: 999}, ctx.Err()
		}
		return fsx.Stats{Bytes: 7, Files: 1}, nil
	}
	m.listFn = func(_ context.Context, path string) tea.Msg {
		return dirListedMsg{path: path, entries: []*anEntry{{name: "child", path: child, isDir: true}}}
	}
	m.dirs[root] = &anDir{path: root, loaded: true}
	m.pending[active], m.pending[queued] = true, true
	old.push(active, queued)
	<-started
	// This batch has already reached Bubble Tea's queue before the rescan.
	old.out <- sizeResult{path: child, st: fsx.Stats{Bytes: 999}}
	stale := waitSizes(m.ctx, old)().(sizeBatchMsg)
	cmds := make(chan tea.Cmd, 1)
	go func() { cmds <- m.rescan() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("rescan did not cancel the old pool")
	}
	select {
	case <-cmds:
		t.Fatal("rescan returned before the old worker finished")
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	var cmd tea.Cmd
	select {
	case cmd = <-cmds:
	case <-time.After(time.Second):
		t.Fatal("rescan did not join the old pool")
	}
	select {
	case <-finished:
	default:
		t.Fatal("old sizing work survived the rescan")
	}
	if m.pool == old || scanctl.From(m.pool.ctx) != ctrl {
		t.Fatal("rescan did not replace the pool while retaining its controller")
	}
	if len(called) != 1 || <-called != active {
		t.Fatal("rescan ran an abandoned queued job")
	}
	if len(m.pending) != 0 {
		t.Fatal("rescan retained old pending jobs")
	}
	m.Update(stale)
	if _, ok := m.sizes[child]; ok {
		t.Fatal("old queued result populated the cleared size cache")
	}
	batch := cmd().(tea.BatchMsg)
	m.Update(batch[0]()) // the new listing queues child in the new pool
	if !m.pending[child] {
		t.Fatal("fresh listing did not queue a new measurement")
	}
	m.Update(stale)
	if !m.pending[child] {
		t.Fatal("stale result cleared the new measurement's pending state")
	}
	m.Update(batch[1]())
	if m.sizes[child].Bytes != 7 || len(m.pending) != 0 {
		t.Fatalf("fresh size=%+v pending=%v", m.sizes[child], m.pending)
	}
	m.Update(stale)
	if m.sizes[child].Bytes != 7 {
		t.Fatal("stale result replaced the completed rescan")
	}
}

func TestAnalyzerRescanCancelsReplacedListing(t *testing.T) {
	home := t.TempDir()
	root := mkdirFiles(t, filepath.Join(home, "root"), map[string]int{"fresh/file": 1})
	m := newTestAnalyzer(t, root, home)
	started, canceled := make(chan struct{}), make(chan struct{})
	m.listFn = func(ctx context.Context, path string) tea.Msg {
		close(started)
		<-ctx.Done()
		close(canceled)
		return dirListedMsg{path: path, entries: []*anEntry{{name: "stale", path: filepath.Join(path, "stale"), sized: true}}}
	}
	oldCmd := m.enter(root, "")
	<-started
	m.listFn = func(_ context.Context, path string) tea.Msg {
		return dirListedMsg{path: path, entries: []*anEntry{{name: "fresh", path: filepath.Join(path, "fresh"), sized: true}}}
	}
	cmd := m.rescan()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("rescan did not cancel the replaced listing context")
	}
	if m.ctx.Err() != nil {
		t.Fatal("rescan canceled the analyzer owner context")
	}
	batch := cmd().(tea.BatchMsg)
	m.Update(batch[0]())
	m.Update(oldCmd())
	if m.cur().byName["fresh"] == nil || m.cur().byName["stale"] != nil {
		t.Fatal("old listing replaced the rescan's results")
	}
}
