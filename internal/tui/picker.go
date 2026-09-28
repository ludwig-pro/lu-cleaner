package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/engine"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

type screen int

const (
	scrCategories screen = iota
	scrItems
)

type pickMode int

const (
	modeBrowse pickMode = iota
	modeHelp
	modeConfirm
	modeCleaning
	modeSummary
)

type itemSort int

const (
	sortSize itemSort = iota
	sortAge
	sortName
)

func (s itemSort) String() string {
	return [...]string{"size ↓", "age (oldest first)", "name"}[s]
}

func (s itemSort) key() string { return [...]string{"size", "age", "name"}[s] }

type statusKind int

const (
	stInfo statusKind = iota
	stOK
	stWarn
	stErr
)

// catRow is one line of the category overview.
type catRow struct {
	info     core.CategoryInfo
	items    []*core.Item // visible items of the category
	total    int64
	cleanN   int // cleanable visible items (measured and verified)
	selN     int // selected visible items
	selTotal int64
	// selected items of the category hidden by the text filter: they are
	// cleaned too, so the row says so instead of pretending they are visible
	selHidden      int
	selHiddenTotal int64
	sizing         bool
}

// pickerModel is the Bubble Tea model of the picker. It is used through a
// pointer so that caches can be refreshed in place.
type pickerModel struct {
	opt   PickerOptions
	env   *core.Env
	now   time.Time
	stale time.Duration

	// scan
	ctx        context.Context
	scanCancel context.CancelFunc
	events     <-chan engine.Event
	scanning   bool
	scanStart  time.Time
	scanTook   time.Duration
	provDone   map[string]bool
	provCats   map[string][]core.Category
	provErrs   []string

	// data (upserts by ID, insertion order kept)
	items   map[string]*core.Item
	order   []string
	removed map[string]bool

	// selection
	selected  map[string]bool
	touched   map[string]bool // items the user selected/unselected explicitly
	smartDone bool

	// ui state
	screen      screen
	mode        pickMode
	cat         core.Category
	catCursor   int
	catOffset   int
	catCursorID core.Category
	cursor      int
	offset      int
	cursorID    string
	catBySize   bool
	sort        itemSort
	showAll     bool
	detailsFlip bool
	filter      lineInput
	trash       bool
	status      string
	statusKind  statusKind
	w, h        int
	spin        spinner.Model
	spinning    bool
	disk        sysx.Disk
	diskErr     error
	quitting    bool

	// caches, recomputed by refresh() when dirty
	dirtyData, dirtySel bool
	base                []*core.Item // opt.Filter matches (selection universe), insertion order
	visible             []*core.Item // base ∩ show-all ∩ text filter
	visTotal            int64        // reclaimable bytes among visible items
	cats                []catRow
	list                []*core.Item // current item list, sorted
	selItems            []*core.Item
	selTotal            int64

	confirm     *confirmState
	run         *cleanRun
	lastSummary *clean.Summary

	// hooks, replaced in tests
	diskFn    func(string) (sysx.Disk, error)
	runningFn func(...string) []string
	revealFn  func(string) error
	cleanFn   func(context.Context, []*core.Item, clean.Options, func(clean.Result)) *clean.Summary
}

// ------------------------------------------------------------------ messages

type scanBatchMsg struct {
	events []engine.Event
	closed bool
}

// waitEvents reads the engine channel. Events are coalesced for a few
// milliseconds so that thousands of upserts cost a handful of re-renders.
func waitEvents(ch <-chan engine.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return scanBatchMsg{closed: true}
		}
		batch := []engine.Event{ev}
		deadline := time.NewTimer(40 * time.Millisecond)
		defer deadline.Stop()
		for len(batch) < 4096 {
			select {
			case ev, ok := <-ch:
				if !ok {
					return scanBatchMsg{events: batch, closed: true}
				}
				batch = append(batch, ev)
			case <-deadline.C:
				return scanBatchMsg{events: batch}
			}
		}
		return scanBatchMsg{events: batch}
	}
}

type runningMsg struct{ names []string }

// ------------------------------------------------------------------ setup

func newPicker(ctx context.Context, opt PickerOptions) *pickerModel {
	if opt.Env == nil {
		opt.Env = core.NewEnv()
	}
	env := opt.Env
	if opt.Title == "" {
		opt.Title = "lu-cleaner"
	}
	if opt.Clean.Home == "" {
		opt.Clean.Home = env.Home
	}
	if opt.Clean.Runner == nil {
		opt.Clean.Runner = env.Runner
	}
	if opt.Clean.Guard == nil {
		// Never clean without a guard.
		roots := append(append([]string(nil), env.Roots...), env.WorktreeRoots...)
		opt.Clean.Guard = safety.New(env.Home, env.TmpDir, roots, env.Exclude)
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	stale := opt.StaleAfter
	if stale <= 0 {
		stale = 14 * 24 * time.Hour
	}
	m := &pickerModel{
		opt:       opt,
		env:       env,
		now:       now,
		stale:     stale,
		ctx:       ctx,
		provDone:  map[string]bool{},
		provCats:  map[string][]core.Category{},
		items:     map[string]*core.Item{},
		removed:   map[string]bool{},
		selected:  map[string]bool{},
		touched:   map[string]bool{},
		trash:     opt.Clean.Trash,
		w:         100,
		h:         30,
		spin:      spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(sAccent)),
		diskFn:    sysx.DiskOf,
		runningFn: sysx.Running,
		revealFn:  revealInFinder,
		cleanFn:   clean.Run,
		dirtyData: true,
	}
	if opt.Flat {
		m.screen = scrItems
	}
	for _, p := range opt.Providers {
		m.provCats[p.ID()] = p.Categories()
	}
	return m
}

func revealInFinder(path string) error {
	cmd := exec.Command("open", "-R", path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (m *pickerModel) Init() tea.Cmd {
	m.refreshDisk()
	scanCtx, cancel := context.WithCancel(m.ctx)
	m.scanCancel = cancel
	m.scanning = true
	m.scanStart = time.Now()
	m.events = engine.Run(scanCtx, m.env, m.opt.Providers)
	return tea.Batch(waitEvents(m.events), m.ensureSpin())
}

func (m *pickerModel) refreshDisk() {
	if m.diskFn != nil {
		m.disk, m.diskErr = m.diskFn(m.env.Home)
	}
}

func (m *pickerModel) ensureSpin() tea.Cmd {
	if m.spinning {
		return nil
	}
	m.spinning = true
	return m.spin.Tick
}

func (m *pickerModel) needsSpin() bool {
	if m.scanning || m.mode == modeCleaning {
		return true
	}
	for _, it := range m.visible {
		if it.Sizing {
			return true
		}
	}
	return false
}

func (m *pickerModel) setStatus(k statusKind, format string, a ...any) {
	m.statusKind = k
	m.status = fmt.Sprintf(format, a...)
}

// ------------------------------------------------------------------ update

func (m *pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case scanBatchMsg:
		m.applyEvents(msg.events)
		if msg.closed {
			m.scanFinished()
		} else {
			cmds = append(cmds, waitEvents(m.events))
		}
	case spinner.TickMsg:
		if m.needsSpin() {
			var c tea.Cmd
			m.spin, c = m.spin.Update(msg)
			cmds = append(cmds, c)
		} else {
			m.spinning = false
		}
	case runningMsg:
		if m.confirm != nil {
			m.confirm.running = msg.names
			m.confirm.checking = false
		}
	case cleanBatchMsg:
		cmds = append(cmds, m.applyClean(msg))
	case tea.KeyMsg:
		cmds = append(cmds, m.handleKey(msg))
	}
	m.refresh()
	m.fixScroll()
	return m, tea.Batch(cmds...)
}

func (m *pickerModel) applyEvents(events []engine.Event) {
	for _, ev := range events {
		if ev.Done {
			m.provDone[ev.Provider] = true
			if ev.Err != nil && !errors.Is(ev.Err, context.Canceled) {
				m.provErrs = append(m.provErrs, ev.Provider+": "+firstLine(ev.Err.Error()))
			}
			continue
		}
		it := ev.Item
		if it == nil || it.ID == "" || m.removed[it.ID] {
			continue
		}
		if _, ok := m.items[it.ID]; !ok {
			m.order = append(m.order, it.ID)
		}
		m.items[it.ID] = it
		m.dirtyData = true
	}
}

func (m *pickerModel) scanFinished() {
	if !m.scanning {
		return
	}
	m.scanning = false
	m.scanTook = time.Since(m.scanStart)
	m.refreshDisk()
	m.dirtyData = true
	m.refresh()
	if m.opt.Smart && !m.smartDone {
		m.smartDone = true
		n := 0
		for _, it := range m.base {
			if m.touched[it.ID] || m.selected[it.ID] {
				continue
			}
			if core.Recommend(it, m.now, m.stale) {
				m.selected[it.ID] = true
				n++
			}
		}
		if n > 0 {
			m.dirtySel = true
			m.refresh()
			m.setStatus(stOK, "Smart selection: %s preselected (%s) — review, then press d to clean", plural(n, "item"), fsx.Bytes(m.selTotal))
		}
	}
}

// ------------------------------------------------------------------ caches

// passBase applies opt.Filter. Items still being measured are not hidden by
// the minimum size yet (they show "…").
func (m *pickerModel) passBase(it *core.Item) bool {
	f := &m.opt.Filter
	if it.Sizing && f.MinSize > 0 {
		g := *f
		g.MinSize = 0
		return g.Match(it, m.now)
	}
	return f.Match(it, m.now)
}

func (m *pickerModel) refresh() {
	if m.dirtyData {
		m.dirtyData = false
		m.dirtySel = true
		m.base = m.base[:0]
		m.visible = m.visible[:0]
		q := strings.ToLower(strings.TrimSpace(m.filter.String()))
		for _, id := range m.order {
			it := m.items[id]
			if it == nil || !m.passBase(it) {
				continue
			}
			m.base = append(m.base, it)
			if !m.showAll && it.Size == 0 && !it.Selectable && !it.Sizing {
				continue
			}
			if q != "" {
				hay := strings.ToLower(it.Name + " " + it.Where() + " " + it.Kind + " " + it.Project + " " + it.Provider)
				if !strings.Contains(hay, q) {
					continue
				}
			}
			m.visible = append(m.visible, it)
		}
		var cleanable []*core.Item
		for _, it := range m.visible {
			if it.CanClean() {
				cleanable = append(cleanable, it)
			}
		}
		m.visTotal = core.Total(cleanable)
		m.buildCats()
		m.buildList()
	}
	if m.dirtySel {
		m.dirtySel = false
		m.selItems = m.selItems[:0]
		for _, it := range m.base {
			// an item still being measured may still be rejected by its
			// provider (e.g. node_modules tracked by git): never clean it yet
			if m.selected[it.ID] && it.CanClean() && !it.Sizing {
				m.selItems = append(m.selItems, it)
			}
		}
		m.selTotal = core.Total(m.selItems)
		m.buildCatSelection()
	}
}

func (m *pickerModel) buildCats() {
	groups := map[core.Category][]*core.Item{}
	for _, it := range m.visible {
		groups[it.Category] = append(groups[it.Category], it)
	}
	running := map[core.Category]bool{}
	if m.scanning {
		for id, cats := range m.provCats {
			if !m.provDone[id] {
				for _, c := range cats {
					running[c] = true
				}
			}
		}
	}
	rows := make([]catRow, 0, len(groups))
	known := map[core.Category]bool{}
	add := func(info core.CategoryInfo) {
		items := groups[info.ID]
		if len(items) == 0 {
			return
		}
		r := catRow{info: info, items: items, total: core.Total(items), sizing: running[info.ID]}
		for _, it := range items {
			if it.CanClean() && !it.Sizing {
				r.cleanN++
			}
			if it.Sizing {
				r.sizing = true
			}
		}
		rows = append(rows, r)
	}
	for _, c := range core.Categories {
		known[c.ID] = true
		add(c)
	}
	var extra []core.Category
	for c := range groups {
		if !known[c] {
			extra = append(extra, c)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, c := range extra {
		add(core.LookupCategory(c))
	}
	if m.catBySize {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].total > rows[j].total })
	}
	m.cats = rows
	// keep the cursor on the same category
	m.catCursor = clamp(m.catCursor, 0, len(rows)-1)
	if m.catCursorID != "" {
		for i, r := range rows {
			if r.info.ID == m.catCursorID {
				m.catCursor = i
				break
			}
		}
	}
	if len(rows) > 0 {
		m.catCursorID = rows[m.catCursor].info.ID
	}
}

// buildCatSelection counts the selection of each category row. Only visible
// items count as the row's selection (its check box and the category toggle
// act on them); selected items hidden by the text filter are counted apart.
func (m *pickerModel) buildCatSelection() {
	vis := make(map[string]bool, len(m.visible))
	for _, it := range m.visible {
		vis[it.ID] = true
	}
	for i := range m.cats {
		r := &m.cats[i]
		var sel, hidden []*core.Item
		for _, it := range m.selItems {
			if it.Category != r.info.ID {
				continue
			}
			if vis[it.ID] {
				sel = append(sel, it)
			} else {
				hidden = append(hidden, it)
			}
		}
		r.selN, r.selTotal = len(sel), core.Total(sel)
		r.selHidden, r.selHiddenTotal = len(hidden), core.Total(hidden)
	}
}

func (m *pickerModel) buildList() {
	var list []*core.Item
	if m.opt.Flat {
		list = append(list, m.visible...)
	} else {
		for _, it := range m.visible {
			if it.Category == m.cat {
				list = append(list, it)
			}
		}
	}
	core.SortBy(list, m.sort.key(), m.now)
	m.list = list
	m.cursor = clamp(m.cursor, 0, len(list)-1)
	if m.cursorID != "" {
		for i, it := range list {
			if it.ID == m.cursorID {
				m.cursor = i
				break
			}
		}
	}
	if len(list) > 0 {
		m.cursorID = list[m.cursor].ID
	} else {
		m.cursorID = ""
	}
}

// ------------------------------------------------------------------ layout

const detailRowsWanted = 9

// layout returns the number of list rows and detail rows for the current size.
func (m *pickerModel) layout() (listRows, detailRows int) {
	avail := m.h - 6 // header 2, breadcrumb 1, column header 1, status 1, footer 1
	if m.showDetails() {
		d := min(detailRowsWanted, avail-4)
		if d >= 4 {
			return avail - d, d
		}
	}
	return max(1, avail), 0
}

func (m *pickerModel) tall() bool { return m.h >= 32 }

func (m *pickerModel) showDetails() bool {
	return m.screen == scrItems && len(m.list) > 0 && m.tall() != m.detailsFlip
}

func (m *pickerModel) fixScroll() {
	rows, _ := m.layout()
	if m.screen == scrCategories {
		m.catOffset = scrollTo(m.catCursor, m.catOffset, rows, len(m.cats))
	} else {
		m.offset = scrollTo(m.cursor, m.offset, rows, len(m.list))
	}
}

// scrollTo returns an offset that keeps cursor inside a window of rows lines.
func scrollTo(cursor, offset, rows, n int) int {
	if rows <= 0 || n == 0 {
		return 0
	}
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+rows {
		offset = cursor - rows + 1
	}
	return clamp(offset, 0, max(0, n-rows))
}

// ------------------------------------------------------------------ keys

func (m *pickerModel) quit() tea.Cmd {
	m.quitting = true
	if m.scanCancel != nil {
		m.scanCancel()
	}
	return tea.Quit
}

func (m *pickerModel) handleKey(k tea.KeyMsg) tea.Cmd {
	if cmds, ok := splitRunes(k, m.handleKey); ok {
		return cmds
	}
	key := k.String()
	switch m.mode {
	case modeHelp:
		if key == "ctrl+c" {
			return m.quit()
		}
		m.mode = modeBrowse
		return nil
	case modeConfirm:
		return m.confirmKey(k)
	case modeCleaning:
		if key == "ctrl+c" && m.run != nil && !m.run.cancelled {
			m.run.cancelled = true
			m.run.cancel()
			m.setStatus(stWarn, "Cancelling… finishing the current items")
		}
		return nil
	case modeSummary:
		if key == "ctrl+c" {
			m.finishClean()
			return m.quit()
		}
		m.finishClean()
		return nil
	}

	if m.filter.active {
		return m.filterKey(k)
	}

	m.status = ""
	m.refresh()
	switch key {
	case "ctrl+c", "q":
		return m.quit()
	case "?":
		m.mode = modeHelp
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+b":
		rows, _ := m.layout()
		m.move(-max(1, rows-1))
	case "pgdown", "ctrl+f", "ctrl+d":
		rows, _ := m.layout()
		m.move(max(1, rows-1))
	case "home", "g":
		m.move(-1 << 30)
	case "end", "G":
		m.move(1 << 30)
	case "right", "l", "enter":
		if m.screen == scrCategories {
			m.openCategory()
		} else if len(m.list) > 0 {
			m.detailsFlip = !m.detailsFlip
		}
	case "left", "h", "esc", "backspace":
		if m.screen == scrItems && !m.opt.Flat {
			m.screen = scrCategories
			return nil
		}
		if key == "esc" && m.filter.String() != "" {
			m.filter.reset()
			m.dirtyData = true
		}
	case "tab":
		m.switchCategory(1)
	case "shift+tab":
		m.switchCategory(-1)
	case " ":
		m.toggleCurrent()
	case "a":
		m.smartSelect()
	case "A":
		m.selectScope(func(*core.Item) bool { return true }, "Selected")
	case "n":
		m.selectScope(func(*core.Item) bool { return false }, "Unselected")
	case "i":
		m.selectScope(func(it *core.Item) bool { return !m.selected[it.ID] }, "Inverted")
	case "s":
		if m.screen == scrCategories {
			m.catBySize = !m.catBySize
			if m.catBySize {
				m.setStatus(stInfo, "Categories sorted by size")
			} else {
				m.setStatus(stInfo, "Categories in default order")
			}
		} else {
			m.sort = (m.sort + 1) % 3
			m.setStatus(stInfo, "Sorted by %s", m.sort)
		}
		m.dirtyData = true
	case "/":
		m.filter.active = true
	case "H", ".":
		m.showAll = !m.showAll
		m.dirtyData = true
		if m.showAll {
			m.setStatus(stInfo, "Showing everything, including empty and report-only entries")
		} else {
			m.setStatus(stInfo, "Hiding empty entries")
		}
	case "t":
		m.trash = !m.trash
		if m.trash {
			m.setStatus(stWarn, "Trash mode: items are moved to ~/.Trash — space is NOT freed until you empty the Trash")
		} else {
			m.setStatus(stInfo, "Delete mode: items are removed permanently and space is freed immediately")
		}
	case "o":
		m.reveal()
	case "d", "x":
		return m.openConfirm()
	}
	return nil
}

func (m *pickerModel) filterKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.filter.reset()
		m.filter.active = false
		m.dirtyData = true
	case "enter":
		m.filter.active = false
	case "up":
		m.move(-1)
	case "down":
		m.move(1)
	default:
		if m.filter.handle(k) {
			m.dirtyData = true
			m.cursor, m.cursorID = 0, ""
		}
	}
	return nil
}

func (m *pickerModel) move(delta int) {
	if m.screen == scrCategories {
		if len(m.cats) == 0 {
			return
		}
		m.catCursor = clamp(m.catCursor+delta, 0, len(m.cats)-1)
		m.catCursorID = m.cats[m.catCursor].info.ID
		return
	}
	if len(m.list) == 0 {
		return
	}
	m.cursor = clamp(m.cursor+delta, 0, len(m.list)-1)
	m.cursorID = m.list[m.cursor].ID
}

func (m *pickerModel) openCategory() {
	if len(m.cats) == 0 {
		return
	}
	m.cat = m.cats[m.catCursor].info.ID
	m.screen = scrItems
	m.cursor, m.offset, m.cursorID = 0, 0, ""
	m.dirtyData = true
}

func (m *pickerModel) switchCategory(delta int) {
	if m.screen != scrItems || m.opt.Flat || len(m.cats) == 0 {
		return
	}
	idx := -1
	for i, r := range m.cats {
		if r.info.ID == m.cat {
			idx = i
		}
	}
	idx = ((idx+delta)%len(m.cats) + len(m.cats)) % len(m.cats)
	m.catCursor = idx
	m.catCursorID = m.cats[idx].info.ID
	m.cat = m.cats[idx].info.ID
	m.cursor, m.offset, m.cursorID = 0, 0, ""
	m.dirtyData = true
}

func (m *pickerModel) currentItem() *core.Item {
	if m.screen != scrItems || m.cursor < 0 || m.cursor >= len(m.list) {
		return nil
	}
	return m.list[m.cursor]
}

// scope is what bulk selection keys act on: the current list in the item
// view, every visible item in the category overview.
func (m *pickerModel) scope() []*core.Item {
	if m.screen == scrItems {
		return m.list
	}
	return m.visible
}

func (m *pickerModel) setSel(it *core.Item, on bool) {
	m.touched[it.ID] = true
	if on {
		m.selected[it.ID] = true
	} else {
		delete(m.selected, it.ID)
	}
	m.dirtySel = true
}

func (m *pickerModel) toggleCurrent() {
	if m.screen == scrCategories {
		m.toggleCategory()
		return
	}
	it := m.currentItem()
	if it == nil {
		return
	}
	if !it.CanClean() {
		why := "report only"
		if it.Method.Cleanable() && it.Risk != core.RiskNever {
			why = "not selectable"
		}
		if it.Note != "" {
			why += " — " + it.Note
		}
		m.setStatus(stInfo, "%s cannot be cleaned (%s)", it.Name, why)
		return
	}
	if it.Sizing && !m.selected[it.ID] {
		m.setStatus(stInfo, "%s is still being measured and verified — select it once its size is shown", it.Name)
		return
	}
	on := !m.selected[it.ID]
	m.setSel(it, on)
	if on && it.Risk >= core.RiskCaution {
		msg := it.Warn
		if msg == "" {
			msg = it.Note
		}
		if msg == "" {
			msg = "may hold data you care about"
		}
		m.setStatus(stWarn, "⚠ caution: %s — %s", it.Name, msg)
	} else if on && it.Warn != "" {
		m.setStatus(stWarn, "⚠ %s", it.Warn)
	}
}

// toggleCategory selects every cleanable item of the category under the
// cursor except caution ones (those must be picked one by one), or clears the
// category when something is already selected in it.
func (m *pickerModel) toggleCategory() {
	if len(m.cats) == 0 {
		return
	}
	r := m.cats[m.catCursor]
	if r.selN > 0 {
		for _, it := range r.items {
			if m.selected[it.ID] {
				m.setSel(it, false)
			}
		}
		m.setStatus(stInfo, "Unselected %s", r.info.Title)
		return
	}
	n, skipped, sizing := 0, 0, 0
	for _, it := range r.items {
		if !it.CanClean() {
			continue
		}
		if it.Sizing {
			sizing++
			continue
		}
		if it.Risk >= core.RiskCaution {
			skipped++
			continue
		}
		m.setSel(it, true)
		n++
	}
	var left []string
	if skipped > 0 {
		left = append(left, plural(skipped, "caution item")+" left unselected (open the category to pick them)")
	}
	if sizing > 0 {
		left = append(left, plural(sizing, "item")+" still being verified")
	}
	if len(left) > 0 {
		m.setStatus(stWarn, "Selected %s in %s — %s", plural(n, "item"), r.info.Title, strings.Join(left, " · "))
	} else {
		m.setStatus(stInfo, "Selected %s in %s", plural(n, "item"), r.info.Title)
	}
}

func (m *pickerModel) smartSelect() {
	n := 0
	for _, it := range m.scope() {
		if !it.CanClean() {
			continue
		}
		on := core.Recommend(it, m.now, m.stale)
		m.setSel(it, on)
		if on {
			n++
		}
	}
	m.refresh()
	m.setStatus(stOK, "Smart selection: %s (safe caches + moderate items unused for %s)", plural(n, "item"), fsx.Age(m.stale))
}

func (m *pickerModel) selectScope(pred func(*core.Item) bool, verb string) {
	n := 0
	for _, it := range m.scope() {
		if !it.CanClean() {
			continue
		}
		on := pred(it) && !it.Sizing // never select what is still being verified
		m.setSel(it, on)
		if on {
			n++
		}
	}
	m.refresh()
	m.setStatus(stInfo, "%s — %s selected in view", verb, plural(n, "item"))
}

func (m *pickerModel) reveal() {
	it := m.currentItem()
	if it == nil {
		return
	}
	p := it.Path
	if p == "" && len(it.Paths) > 0 {
		p = it.Paths[0]
	}
	if p == "" && it.Location != "" {
		loc := strings.TrimSuffix(strings.TrimSuffix(it.Location, "…"), "/")
		if strings.HasPrefix(loc, "~") {
			loc = m.env.Expand(loc)
		}
		if filepath.IsAbs(loc) {
			p = loc
		}
	}
	if p == "" {
		m.setStatus(stInfo, "Nothing to reveal for %s", it.Name)
		return
	}
	if _, err := os.Lstat(p); err != nil {
		m.setStatus(stWarn, "%s does not exist anymore", m.env.Pretty(p))
		return
	}
	if err := m.revealFn(p); err != nil {
		m.setStatus(stErr, "open -R failed: %v", err)
		return
	}
	m.setStatus(stInfo, "Revealed %s in Finder", m.env.Pretty(p))
}
