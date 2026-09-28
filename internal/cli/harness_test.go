package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"github.com/ludwig-pro/lu-cleaner/internal/tui"
)

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// fakeProvider emits a fixed list of items.
type fakeProvider struct {
	id    string
	cats  []core.Category
	items []*core.Item
	err   error
}

func (p *fakeProvider) ID() string                  { return p.id }
func (p *fakeProvider) Title() string               { return p.id }
func (p *fakeProvider) Categories() []core.Category { return p.cats }
func (p *fakeProvider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	for _, it := range p.items {
		emit(it.Clone())
	}
	return p.err
}

// harness runs the CLI against fakes, in a temporary home.
type harness struct {
	t       *testing.T
	app     *App
	home    string
	cfg     config.Config
	out     bytes.Buffer
	errOut  bytes.Buffer
	provs   []core.Provider
	history []clean.HistoryEntry
	entries []catalog.Entry

	mu       sync.Mutex
	pickers  []tui.PickerOptions
	analyzes []tui.AnalyzeOptions
	cleaned  [][]*core.Item
	cleanOpt []clean.Options
	// failNames makes the fake cleaner fail these items.
	failNames map[string]bool
}

func newHarness(t *testing.T, provs ...core.Provider) *harness {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Never touch the real config / history, whatever happens.
	t.Setenv("LU_CLEANER_CONFIG", filepath.Join(home, ".config", "lu-cleaner", "config.toml"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	h := &harness{t: t, home: home, provs: provs, failNames: map[string]bool{}}
	h.cfg = config.Config{MinSize: "1MB", StaleAfter: "14d", MaxDepth: 8, KeepLatest: 1}
	h.app = &App{
		Version: "test",
		Stdin:   strings.NewReader(""),
		Stdout:  &h.out,
		Stderr:  &h.errOut,
		Width:   func() int { return 0 },
		Getenv:  func(string) string { return "" },
		LoadConfig: func() (*config.Config, error) {
			c := h.cfg
			return &c, nil
		},
		NewEnv: func() *core.Env {
			return &core.Env{
				Home: home, TmpDir: filepath.Join(home, "tmp", "T"), Now: testNow, MaxDepth: 8,
				Runner: core.ExecRunner{}, Logf: func(string, ...any) {},
			}
		},
		Providers: func() []core.Provider { return h.provs },
		Catalog:   func() []catalog.Entry { return h.entries },
		Picker: func(_ context.Context, opt tui.PickerOptions) (*clean.Summary, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.pickers = append(h.pickers, opt)
			return nil, nil
		},
		Analyze: func(_ context.Context, opt tui.AnalyzeOptions) error {
			h.analyzes = append(h.analyzes, opt)
			return nil
		},
		Clean:     h.fakeClean,
		Disk:      func(string) (sysx.Disk, error) { return sysx.Disk{Total: 500e9, Free: 50e9, Used: 450e9}, nil },
		Snapshots: func(context.Context) []string { return nil },
		Running:   func(...string) []string { return nil },
		History:   func() ([]clean.HistoryEntry, error) { return h.history, nil },
	}
	return h
}

func (h *harness) tty() *harness {
	h.app.StdinTTY, h.app.StdoutTTY, h.app.StderrTTY = true, true, false
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.errOut.Reset()
	return h.app.Run(context.Background(), args)
}

func (h *harness) fakeClean(_ context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
	h.mu.Lock()
	h.cleaned = append(h.cleaned, items)
	h.cleanOpt = append(h.cleanOpt, opt)
	h.mu.Unlock()
	sum := &clean.Summary{DryRun: opt.DryRun, Trash: opt.Trash,
		DiskBefore: sysx.Disk{Total: 500e9, Free: 50e9}, DiskAfter: sysx.Disk{Total: 500e9, Free: 50e9}}
	for _, it := range items {
		r := clean.Result{Item: it, Status: clean.StatusDone, Freed: it.Freed()}
		switch {
		case h.failNames[it.Name]:
			r = clean.Result{Item: it, Status: clean.StatusFailed, Error: "permission denied"}
		case opt.DryRun:
			r.Status, r.Message = clean.StatusDryRun, "would delete "+it.Path
		default:
			sum.Estimated += r.Freed
		}
		sum.Results = append(sum.Results, r)
		if progress != nil {
			progress(r)
		}
	}
	return sum
}

// lastClean returns the names passed to the last clean call.
func (h *harness) lastClean() []string {
	h.t.Helper()
	if len(h.cleaned) == 0 {
		h.t.Fatal("clean was not called")
	}
	var names []string
	for _, it := range h.cleaned[len(h.cleaned)-1] {
		names = append(names, it.Name)
	}
	return names
}

// mkItem builds a cleanable item under the harness home.
func (h *harness) mkItem(name string, cat core.Category, kind string, size int64, risk core.Risk, age time.Duration) *core.Item {
	it := &core.Item{
		ID:         "fake:" + kind + ":" + name,
		Category:   cat,
		Kind:       kind,
		Name:       name,
		Path:       filepath.Join(h.home, "dev", name, kind),
		Size:       size,
		Risk:       risk,
		Method:     core.MethodDelete,
		Selectable: true,
	}
	if age > 0 {
		it.LastUsed = testNow.Add(-age)
	}
	return it
}

const (
	day = 24 * time.Hour
	mb  = int64(1e6)
	gb  = int64(1e9)
)
