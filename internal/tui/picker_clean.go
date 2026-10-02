package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// confirmState backs the "Clean N items — X GB?" modal.
type confirmState struct {
	items       []*core.Item // top-level selected items (what clean.Run will process)
	all         []*core.Item // the whole selection, handed to clean.Run (it plans nested items itself)
	total       int64
	byRisk      [4]riskCount
	commands    []string
	guards      []string
	running     []string
	runningErr  error
	cancelCheck context.CancelFunc
	checking    bool
	caution     int          // caution items selected or inside a selected item: "type yes" needed
	nested      []*core.Item // caution items inside a selected item (not selected themselves)
	selected    int          // selected items (top-level ones plus those nested in them)
	hidden      int          // selected items hidden by the text filter
	trashN      int          // items skipped because Trash mode cannot handle them
	forced      []*core.Item // items that need --force (RequireForce), cleaned because of --force or Trash mode
	largest     []*core.Item
	input       lineInput
	hint        string
	// sig identifies what the dialog promised to clean; startClean refuses to
	// run when the selection no longer matches it (scan upserts, rejections).
	sig string
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
	freed      int64 // really freed
	trashed    int64 // moved to the Trash (freed once it is emptied)
	trash      bool
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
	c := m.buildConfirm()
	if c == nil {
		m.setStatus(stInfo, "Nothing selected — space selects an item, a does a smart selection")
		return nil
	}
	m.confirm = c
	m.mode = modeConfirm
	if len(c.guards) == 0 || m.runningFn == nil {
		return nil
	}
	c.checking = true
	guards, fn := append([]string(nil), c.guards...), m.runningFn
	ctx, cancel := context.WithCancel(m.ctx)
	c.cancelCheck = cancel
	return m.tasks.run(func() tea.Msg {
		defer cancel()
		names, err := fn(ctx, guards...)
		return runningMsg{confirm: c, names: names, err: err}
	})
}

// buildConfirm computes the confirmation dialog for the current selection
// (nil when nothing is selected). It has no side effect, so startClean can
// rebuild it to check that nothing changed while the dialog was open.
func (m *pickerModel) buildConfirm() *confirmState {
	if len(m.selItems) == 0 {
		return nil
	}
	keep, skip := splitTrash(m.selItems, m.trash)
	top := core.TopLevel(keep)
	c := &confirmState{items: top, all: append([]*core.Item(nil), m.selItems...),
		total: core.Total(keep), selected: len(m.selItems), trashN: len(skip)}
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

	// Caution content: every selected caution item, and every known caution
	// item lying inside a selected item (cleaning the parent wipes it too).
	// Items that need --force are treated like caution items: typing yes.
	caution := map[string]bool{}
	for _, it := range keep {
		if it.Risk >= core.RiskCaution || it.RequireForce {
			caution[it.ID] = true
		}
		if it.RequireForce {
			c.forced = append(c.forced, it)
		}
	}
	var roots []string
	for _, it := range top {
		roots = append(roots, it.Targets()...)
		if len(it.Targets()) == 0 && it.Covers != "" {
			roots = append(roots, it.Covers)
		}
	}
	if len(roots) > 0 {
		keys := make([]string, len(roots))
		for i, r := range roots {
			keys[i] = safety.Key(r)
		}
		for _, id := range m.order {
			it := m.items[id]
			if it == nil || it.Risk < core.RiskCaution || caution[id] || !allWithin(it.Targets(), keys) {
				continue
			}
			caution[id] = true
			c.nested = append(c.nested, it)
		}
	}
	sort.SliceStable(c.nested, func(i, j int) bool { return c.nested[i].Freed() > c.nested[j].Freed() })
	c.caution = len(caution)

	vis := make(map[string]bool, len(m.visible))
	for _, it := range m.visible {
		vis[it.ID] = true
	}
	for _, it := range m.selItems {
		if !vis[it.ID] {
			c.hidden++
		}
	}

	c.largest = append([]*core.Item(nil), top...)
	sort.SliceStable(c.largest, func(i, j int) bool { return c.largest[i].Freed() > c.largest[j].Freed() })
	if len(c.largest) > 5 {
		c.largest = c.largest[:5]
	}
	c.input.active = c.caution > 0

	// signature: what is cleaned, how, and what needs typing yes
	var sig []string
	for _, it := range append(append([]*core.Item(nil), top...), skip...) {
		sig = append(sig, strings.Join([]string{it.ID, it.Risk.String(), it.Method.String(),
			strings.Join(it.Targets(), "\x00"), it.Covers, strings.Join(it.Command, "\x00")}, "\x01"))
	}
	ids := make([]string, 0, len(caution))
	for id := range caution {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sig = append(sig, "caution:"+strings.Join(ids, ","), fmt.Sprintf("trash:%v", m.trash))
	c.sig = strings.Join(sig, "\n")
	return c
}

// allWithin reports whether ts is not empty and each path lies inside (or
// is) one of rootKeys (safety.Key forms), comparing paths the way APFS does
// (case and Unicode normalization insensitive).
func allWithin(ts, rootKeys []string) bool {
	if len(ts) == 0 {
		return false
	}
	for _, p := range ts {
		k, in := safety.Key(p), false
		for _, r := range rootKeys {
			if k == r || strings.HasPrefix(k, r+"/") || r == "/" {
				in = true
				break
			}
		}
		if !in {
			return false
		}
	}
	return true
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
	m.cancelConfirmCheck()
	m.confirm = nil
	m.mode = modeBrowse
	m.setStatus(stInfo, "Cancelled — nothing was touched")
}

func (m *pickerModel) cancelConfirmCheck() {
	if m.confirm != nil && m.confirm.cancelCheck != nil {
		m.confirm.cancelCheck()
	}
}

func (m *pickerModel) cleanOptions() clean.Options {
	o := m.opt.Clean
	o.Trash = m.trash
	return o
}

func (m *pickerModel) startClean() tea.Cmd {
	m.cancelConfirmCheck()
	// The scan keeps running behind the dialog: an item may have been
	// rejected, re-classified or removed since it opened. Rebuild the dialog
	// from the current items and only clean what the user saw.
	m.refresh()
	c := m.buildConfirm()
	if c == nil || c.sig != m.confirm.sig {
		m.confirm = nil
		m.mode = modeBrowse
		m.setStatus(stWarn, "Selection changed while confirming — nothing was touched; review it and press d again")
		return nil
	}
	items := c.all // current values (fresh inode snapshots)
	opts := m.cleanOptions()
	ctx, cancel := context.WithCancel(m.ctx)
	run := &cleanRun{
		cancel:     cancel,
		ch:         make(chan tea.Msg, len(items)+2), // at most one result per item: never blocks
		done:       make(chan struct{}),
		total:      len(c.items) + c.trashN,
		totalBytes: c.total,
		start:      time.Now(),
		trash:      opts.Trash,
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
		run.total = max(run.total, run.processed) // nested worktrees get their own result
		if r.Item != nil && !(r.Status == clean.StatusSkipped && r.Message == trashSkipMsg) {
			run.procBytes += r.Item.Freed() // Trash-mode skips are not part of totalBytes
		}
		switch {
		case movedToTrash(r, run.trash):
			run.trashed += max(r.Trashed, r.Freed)
		case r.Status == clean.StatusDone:
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
		// a command that removes a whole directory (simctl delete <udid>)
		// takes the items inside it along
		if r.Item.Method == core.MethodCommand && r.Item.Covers != "" {
			if _, err := os.Lstat(r.Item.Covers); errors.Is(err, fs.ErrNotExist) {
				gone[r.Item.Covers] = true
			}
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
		done := "cleaned"
		if run.trash {
			done = "moved to the Trash" // Trash mode only ever moves: nothing freed
		}
		m.setStatus(stWarn, "%d %s, %d skipped, %d failed — remaining items are still listed", st[clean.StatusDone], done, st[clean.StatusSkipped], st[clean.StatusFailed])
	case run.trashed > 0 && run.freed > 0:
		m.setStatus(stOK, "%d cleaned · %s freed · %s moved to the Trash (not freed until you empty it)", st[clean.StatusDone], fsx.Bytes(run.freed), fsx.Bytes(run.trashed))
	case run.trashed > 0:
		m.setStatus(stOK, "%d moved to the Trash · %s (not freed until you empty it)", st[clean.StatusDone], fsx.Bytes(run.trashed))
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

// trashSkipMsg is why Trash mode leaves worktree and command items alone.
const trashSkipMsg = "not possible in Trash mode (it would delete permanently)"

// splitTrash separates, in Trash mode, the items that cannot be moved to the
// Trash: `git worktree remove` and commands delete permanently, so they are
// skipped rather than run behind a "move to Trash" promise.
func splitTrash(items []*core.Item, trash bool) (keep, skip []*core.Item) {
	if !trash {
		return items, nil
	}
	for _, it := range items {
		if it.Method == core.MethodWorktree || it.Method == core.MethodCommand {
			skip = append(skip, it)
		} else {
			keep = append(keep, it)
		}
	}
	return keep, skip
}

// movedToTrash reports a done result whose item went to the Trash, which
// frees nothing until the Trash is emptied.
func movedToTrash(r clean.Result, trash bool) bool {
	if r.Status != clean.StatusDone || r.Item == nil {
		return false
	}
	return r.Method == core.MethodTrash || r.Trashed > 0 || r.Item.Method == core.MethodTrash ||
		trash && r.Item.Method == core.MethodDelete
}

// trashSplit sums the done results of s: bytes really freed, and bytes only
// moved to the Trash (never reported as freed).
func trashSplit(s *clean.Summary) (freed, moved int64, nMoved int) {
	for _, r := range s.Results {
		switch {
		case movedToTrash(r, s.Trash):
			moved += max(r.Trashed, r.Freed)
			nMoved++
		case r.Status == clean.StatusDone:
			freed += r.Freed
		}
	}
	return freed, moved, nMoved
}

// safeClean runs fn and turns a panic into failed results, so that a bug in
// the executor never leaves the terminal in raw mode. progress is only called
// for items that were not reported yet. In Trash mode, worktree and command
// items are not handed to fn: they are reported as skipped (trashSkipMsg).
func safeClean(fn func(context.Context, []*core.Item, clean.Options, func(clean.Result)) *clean.Summary,
	ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) (sum *clean.Summary) {
	items, skipped := splitTrash(items, opt.Trash)
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
		if sum == nil {
			sum = &clean.Summary{Trash: opt.Trash, DryRun: opt.DryRun}
		}
		for _, it := range skipped {
			r := clean.Result{Item: it, Status: clean.StatusSkipped, Message: trashSkipMsg}
			sum.Results = append(sum.Results, r)
			if progress != nil {
				progress(r)
			}
		}
	}()
	return fn(ctx, items, opt, report)
}
