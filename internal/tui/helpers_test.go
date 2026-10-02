package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

// fakeProvider emits a fixed list of items. When gate is non-nil it waits for
// it to be closed before returning (to observe the "scanning" state).
type fakeProvider struct {
	id    string
	cats  []core.Category
	items []*core.Item
	gate  chan struct{}
	err   error
}

func (p *fakeProvider) ID() string                  { return p.id }
func (p *fakeProvider) Title() string               { return "fake " + p.id }
func (p *fakeProvider) Categories() []core.Category { return p.cats }
func (p *fakeProvider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	for _, it := range p.items {
		emit(it.Clone())
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.err
}

// mkItem builds a cleanable item.
func mkItem(id string, cat core.Category, size int64, risk core.Risk, age time.Duration) *core.Item {
	it := &core.Item{
		ID: id, Provider: "fake", Category: cat, Kind: "test", Name: id,
		Path: "/nonexistent/" + id, Size: size, Risk: risk, Method: core.MethodDelete, Selectable: true,
	}
	if age > 0 {
		it.LastUsed = testNow.Add(-age)
	}
	return it
}

func tempHome(t *testing.T) string {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(h, ".state"))
	return h
}

func mkdirFiles(t *testing.T, dir string, files map[string]int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, n := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func testEnv(home string) *core.Env {
	return &core.Env{Home: home, Now: testNow, Runner: core.ExecRunner{}, Logf: func(string, ...any) {}}
}

func testCleanOpts(home string) clean.Options {
	return clean.Options{Guard: safety.New(home, "", nil, nil), Home: home, NoHistory: true, Runner: core.ExecRunner{}}
}

func fakeDisk(string) (sysx.Disk, error) {
	return sysx.Disk{Total: 500e9, Free: 50e9, Used: 450e9}, nil
}

// newTestPicker builds a picker with side-effect free hooks.
func newTestPicker(t *testing.T, opt PickerOptions) *pickerModel {
	t.Helper()
	if opt.Env == nil {
		opt.Env = testEnv(tempHome(t))
	}
	if opt.Clean.Guard == nil {
		opt.Clean = testCleanOpts(opt.Env.Home)
	}
	if opt.Filter.MaxRisk == core.RiskSafe {
		opt.Filter.MaxRisk = core.RiskNever
	}
	m := newPicker(context.Background(), opt)
	t.Cleanup(m.close)
	m.diskFn = fakeDisk
	m.runningFn = func(context.Context, ...string) ([]string, error) { return nil, nil }
	m.revealFn = func(string) error { return nil }
	m.w, m.h = 120, 40
	return m
}

// driver is a tiny synchronous Bubble Tea runtime for model tests: commands
// run in goroutines, their messages are fed back to Update on the test
// goroutine only.
type driver struct {
	t     *testing.T
	m     tea.Model
	msgs  chan tea.Msg
	quit  bool
	stop  chan struct{}
	views int
}

func newDriver(t *testing.T, m tea.Model) *driver {
	d := &driver{t: t, m: m, msgs: make(chan tea.Msg, 1024), stop: make(chan struct{})}
	t.Cleanup(func() { close(d.stop) })
	d.exec(m.Init())
	return d
}

func (d *driver) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if msg == nil {
			return
		}
		select {
		case d.msgs <- msg:
		case <-d.stop:
		}
	}()
}

func (d *driver) handle(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			d.exec(c)
		}
		return
	case tea.QuitMsg:
		d.quit = true
		return
	}
	var cmd tea.Cmd
	d.m, cmd = d.m.Update(msg)
	_ = d.m.View() // every update is rendered, like the real runtime
	d.views++
	d.exec(cmd)
}

// send delivers msg synchronously.
func (d *driver) send(msgs ...tea.Msg) {
	for _, m := range msgs {
		d.handle(m)
	}
}

// keys sends key presses ("enter", "esc", "space", "ctrl+c", "up", runes...).
func (d *driver) keys(ks ...string) {
	for _, k := range ks {
		d.handle(keyMsg(k))
	}
}

// typeText sends each rune as a key press.
func (d *driver) typeText(s string) {
	for _, r := range s {
		d.handle(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// until processes messages until cond holds (or fails the test after 10s).
func (d *driver) until(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case msg := <-d.msgs:
			d.handle(msg)
		case <-deadline:
			d.t.Fatalf("timeout waiting for %s", what)
		}
	}
}

func keyMsg(k string) tea.KeyMsg {
	types := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "space": tea.KeySpace, "up": tea.KeyUp,
		"down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "tab": tea.KeyTab,
		"shift+tab": tea.KeyShiftTab, "backspace": tea.KeyBackspace, "ctrl+c": tea.KeyCtrlC,
		"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "home": tea.KeyHome, "end": tea.KeyEnd,
		"ctrl+u": tea.KeyCtrlU,
	}
	if t, ok := types[k]; ok {
		if t == tea.KeySpace {
			return tea.KeyMsg{Type: t, Runes: []rune{' '}}
		}
		return tea.KeyMsg{Type: t}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func teaSize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }

// checkFrame asserts a rendered frame fits the terminal.
func checkFrame(t *testing.T, name, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Errorf("%s: %d lines > height %d", name, len(lines), h)
	}
	for i, l := range lines {
		if lw := width(l); lw > w {
			t.Errorf("%s: line %d is %d cells wide > %d: %q", name, i, lw, w, l)
			break
		}
	}
}

// fakeWorktree lays out, without git, a linked worktree wt of the repository
// main as git does: main/.git/worktrees/<name>/{gitdir,commondir,HEAD} and a
// wt/.git file pointing to that admin dir. It returns the admin dir.
func fakeWorktree(t *testing.T, main, wt string) string {
	t.Helper()
	common := filepath.Join(main, ".git")
	admin := filepath.Join(common, "worktrees", filepath.Base(wt))
	for _, d := range []string{filepath.Join(common, "objects"), filepath.Join(common, "refs", "heads"), admin, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(common, "HEAD"):     "ref: refs/heads/main\n",
		filepath.Join(admin, "HEAD"):      "ref: refs/heads/" + filepath.Base(wt) + "\n",
		filepath.Join(admin, "commondir"): "../..\n",
		filepath.Join(admin, "gitdir"):    filepath.Join(wt, ".git") + "\n",
		filepath.Join(wt, ".git"):         "gitdir: " + admin + "\n",
	}
	for p, content := range files {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return admin
}
