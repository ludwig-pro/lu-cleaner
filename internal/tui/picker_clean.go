package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// confirmState backs the "Clean N items — X GB?" modal.
type confirmState struct {
	items    []*core.Item // top-level selected items (what clean.Run will process)
	total    int64
	byRisk   [4]riskCount
	commands []string
	guards   []string
	running  []string
	checking bool
	caution  int
	largest  []*core.Item
	input    lineInput
	hint     string
}

type riskCount struct {
	n    int
	size int64
}

// cleanRun tracks an execution of clean.Run in the background.
type cleanRun struct {
	cancel     context.CancelFunc
	cancelled  bool
	ch         chan tea.Msg
	done       chan struct{} // closed once clean.Run returned
	summary    *clean.Summary
	total      int
	totalBytes int64
	processed  int
	procBytes  int64
	freed      int64
	results    []clean.Result
	start      time.Time
	finished   bool
}

type cleanResultMsg struct{ r clean.Result }
type cleanDoneMsg struct{ sum *clean.Summary }

// cleanBatchMsg carries every progress message available at once.
type cleanBatchMsg struct {
	results []clean.Result
	sum     *clean.Summary
	done    bool
}

func waitClean(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		var b cleanBatchMsg
		add := func(msg tea.Msg) {
			switch msg := msg.(type) {
			case cleanResultMsg:
				b.results = append(b.results, msg.r)
			case cleanDoneMsg:
				b.sum, b.done = msg.sum, true
			}
		}
		msg, ok := <-ch
		if !ok {
			b.done = true
			return b
		}
		add(msg)
		for !b.done {
			select {
			case msg, ok := <-ch:
				if !ok {
					b.done = true
					return b
				}
				add(msg)
			default:
				return b
			}
		}
		return b
	}
}

func (m *pickerModel) openConfirm() tea.Cmd {
	m.refresh()
	if len(m.selItems) == 0 {
		m.setStatus(stInfo, "Nothing selected — space selects an item, a does a smart selection")
		return nil
	}
	top := core.TopLevel(m.selItems)
	c := &confirmState{items: top, total: core.Total(m.selItems)}
	seenCmd := map[string]bool{}
	seenGuard := map[string]bool{}
	addCmd := func(argv []string) {
		if len(argv) == 0 {
			return
		}
		s := strings.Join(argv, " ")
		if !seenCmd[s] {
			seenCmd[s] = true
			c.commands = append(c.commands, s)
		}
	}
	for _, it := range top {
		r := min(max(it.Risk, core.RiskSafe), core.RiskNever)
		c.byRisk[r].n++
		c.byRisk[r].size += it.Freed()
		if it.Risk >= core.RiskCaution {
			c.caution++
		}
		switch it.Method {
		case core.MethodCommand:
			addCmd(it.Command)
		case core.MethodWorktree:
			addCmd([]string{"git", "worktree", "remove", "…"})
		}
		for _, pc := range it.PostCommands {
			addCmd(pc)
		}
		for _, g := range it.ProcessGuard {
			if !seenGuard[g] {
				seenGuard[g] = true
				c.guards = append(c.guards, g)
			}
		}
	}
	c.largest = append([]*core.Item(nil), top...)
	sort.SliceStable(c.largest, func(i, j int) bool { return c.largest[i].Freed() > c.largest[j].Freed() })
	if len(c.largest) > 5 {
		c.largest = c.largest[:5]
	}
	c.input.active = c.caution > 0
	m.confirm = c
	m.mode = modeConfirm
	if len(c.guards) == 0 || m.runningFn == nil {
		return nil
	}
	c.checking = true
	guards, fn := append([]string(nil), c.guards...), m.runningFn
	return func() tea.Msg { return runningMsg{names: fn(guards...)} }
}

func (m *pickerModel) confirmKey(k tea.KeyMsg) tea.Cmd {
	c := m.confirm
	key := k.String()
	if key == "ctrl+c" {
		return m.quit()
	}
	if c.caution > 0 {
		switch key {
		case "esc":
			m.cancelConfirm()
		case "enter":
			if strings.EqualFold(strings.TrimSpace(c.input.String()), "yes") {
				return m.startClean()
			}
			c.hint = "type yes (then enter) to confirm, esc to cancel"
		default:
			c.input.handle(k)
			c.hint = ""
		}
		return nil
	}
	switch key {
	case "y", "Y", "enter":
		return m.startClean()
	case "n", "N", "esc", "q":
		m.cancelConfirm()
	}
	return nil
}

func (m *pickerModel) cancelConfirm() {
	m.confirm = nil
	m.mode = modeBrowse
	m.setStatus(stInfo, "Cancelled — nothing was touched")
}

func (m *pickerModel) cleanOptions() clean.Options {
	o := m.opt.Clean
	o.Trash = m.trash
	return o
}

func (m *pickerModel) startClean() tea.Cmd {
	c := m.confirm
	items := c.items
	opts := m.cleanOptions()
	ctx, cancel := context.WithCancel(m.ctx)
	run := &cleanRun{
		cancel:     cancel,
		ch:         make(chan tea.Msg, len(items)+2), // one result per top-level item: never blocks
		done:       make(chan struct{}),
		total:      len(items),
		totalBytes: c.total,
		start:      time.Now(),
	}
	m.run = run
	m.confirm = nil
	m.mode = modeCleaning
	m.status = ""
	fn := m.cleanFn
	go func() {
		defer close(run.done)
		defer cancel()
		sum := safeClean(fn, ctx, items, opts, func(r clean.Result) { run.ch <- cleanResultMsg{r} })
		run.summary = sum
		run.ch <- cleanDoneMsg{sum}
	}()
	return tea.Batch(waitClean(run.ch), m.ensureSpin())
}

func (m *pickerModel) applyClean(b cleanBatchMsg) tea.Cmd {
	run := m.run
	if run == nil {
		return nil
	}
	for _, r := range b.results {
		run.processed++
		if r.Item != nil {
			run.procBytes += r.Item.Freed()
		}
		if r.Status == clean.StatusDone {
			run.freed += r.Freed
		}
		run.results = append(run.results, r)
	}
	if !b.done {
		return waitClean(run.ch)
	}
	<-run.done
	run.finished = true
	if b.sum != nil {
		m.lastSummary = b.sum
	} else {
		m.lastSummary = run.summary
	}
	m.mode = modeSummary
	m.refreshDisk()
	return nil
}

// finishClean leaves the summary: cleaned items (and everything nested in
// them) disappear from the lists; skipped, failed and dry-run items stay.
func (m *pickerModel) finishClean() {
	run := m.run
	m.mode = modeBrowse
	if run == nil {
		return
	}
	m.run = nil
	gone := map[string]bool{}
	doneIDs := map[string]bool{}
	var st [4]int
	for _, r := range run.results {
		if int(r.Status) < len(st) {
			st[r.Status]++
		}
		if r.Status != clean.StatusDone || r.Item == nil {
			continue
		}
		doneIDs[r.Item.ID] = true
		for _, p := range r.Item.Targets() {
			gone[p] = true
		}
	}
	if len(doneIDs) > 0 {
		keep := m.order[:0]
		for _, id := range m.order {
			it := m.items[id]
			if it == nil || doneIDs[id] || insideAny(it, gone) {
				delete(m.items, id)
				delete(m.selected, id)
				m.removed[id] = true
				continue
			}
			keep = append(keep, id)
		}
		m.order = keep
		m.dirtyData = true
	}
	m.refresh()
	if m.screen == scrItems && !m.opt.Flat && len(m.list) == 0 {
		m.screen = scrCategories
	}
	switch {
	case m.lastSummary != nil && m.lastSummary.DryRun:
		m.setStatus(stInfo, "Dry-run finished: %d item(s) would be cleaned — nothing was touched", st[clean.StatusDryRun])
	case st[clean.StatusFailed]+st[clean.StatusSkipped] > 0:
		m.setStatus(stWarn, "%d cleaned, %d skipped, %d failed — remaining items are still listed", st[clean.StatusDone], st[clean.StatusSkipped], st[clean.StatusFailed])
	default:
		m.setStatus(stOK, "%d cleaned · %s freed", st[clean.StatusDone], fsx.Bytes(run.freed))
	}
}

// insideAny reports whether every target of it lies inside a removed path.
func insideAny(it *core.Item, gone map[string]bool) bool {
	ts := it.Targets()
	if len(ts) == 0 {
		return false
	}
	for _, p := range ts {
		found := false
		for q := p; ; q = filepath.Dir(q) {
			if gone[q] {
				found = true
				break
			}
			if q == "/" || q == "." || q == filepath.Dir(q) {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// waitCleanFinished blocks until a running clean ends (used when the program
// exits abruptly) and records its summary.
func (m *pickerModel) waitCleanFinished() {
	if m.run == nil || m.run.finished {
		return
	}
	m.run.cancel()
	<-m.run.done
	if m.run.summary != nil {
		m.lastSummary = m.run.summary
	}
}

// safeClean runs fn and turns a panic into failed results, so that a bug in
// the executor never leaves the terminal in raw mode. progress is only called
// for items that were not reported yet.
func safeClean(fn func(context.Context, []*core.Item, clean.Options, func(clean.Result)) *clean.Summary,
	ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) (sum *clean.Summary) {
	var mu sync.Mutex
	reported := map[*core.Item]bool{}
	report := func(r clean.Result) {
		mu.Lock()
		reported[r.Item] = true
		mu.Unlock()
		if progress != nil {
			progress(r)
		}
	}
	defer func() {
		if p := recover(); p != nil {
			sum = &clean.Summary{Trash: opt.Trash, DryRun: opt.DryRun}
			mu.Lock()
			defer mu.Unlock()
			for _, it := range core.TopLevel(items) {
				r := clean.Result{Item: it, Status: clean.StatusFailed, Error: fmt.Sprintf("internal error: %v", p)}
				sum.Results = append(sum.Results, r)
				if !reported[it] && progress != nil {
					progress(r)
				}
			}
		}
	}()
	return fn(ctx, items, opt, report)
}
