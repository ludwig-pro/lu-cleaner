// Package catalog is the data-driven provider: a list of well-known paths
// (caches, logs, tool data) described by Entry values, spread over the
// data_*.go files (one per domain). Dynamic things (worktrees, simulators,
// AVDs, node versions...) live in their own providers.
package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// Mode says how glob matches become items.
type Mode int

const (
	// Group: all matches of the entry form ONE item (e.g. "Metro caches (23)").
	Group Mode = iota
	// Each: one item per match (e.g. one per Gradle wrapper distribution).
	Each
)

// Entry describes one well-known location.
type Entry struct {
	ID       string        // unique, kebab-case; becomes Item.Kind
	Category core.Category //
	Name     string        // human label
	// Paths are globs; "~/" is the home dir and "$TMPDIR" the per-user temp dir.
	// filepath.Glob syntax (no "**").
	Paths []string
	// Exclude drops matches whose base name matches one of these globs.
	Exclude []string
	Risk    core.Risk
	Method  core.Method // default MethodDelete
	// Command is run instead of deleting (MethodCommand). Paths are then only
	// used to measure what the command frees (and to decide whether to show it).
	Command  []string
	Requires string // binary that must be on PATH (commands)
	// ProcessGuard: refuse to clean while one of these processes runs.
	ProcessGuard []string
	Note         string
	Mode         Mode
	// OlderThan only keeps matches whose mtime is older than this.
	OlderThan time.Duration
	// KeepLatest (Each mode) keeps the N most recently modified matches out.
	KeepLatest int
	// AllowGitRepo lets the guard remove a match that is a git repository.
	AllowGitRepo bool
	// Recommended forces "smart select" for this entry.
	Recommended bool
	// MinBytes hides the item when smaller (default: shown if > 0 bytes).
	MinBytes int64
	// Files: matches are files, not directories (e.g. *.sqlite.bak).
	Files bool
}

var entries []Entry

// add registers entries (called from data_*.go init functions).
func add(es ...Entry) { entries = append(entries, es...) }

// Entries returns a copy of the registered entries (sorted by category, id).
func Entries() []Entry {
	out := append([]Entry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Provider evaluates the catalog.
type Provider struct {
	entries []Entry
}

// New returns the catalog provider with every registered entry.
func New() *Provider { return &Provider{entries: Entries()} }

func (p *Provider) ID() string    { return "catalog" }
func (p *Provider) Title() string { return "Known caches & tool data" }
func (p *Provider) Categories() []core.Category {
	seen := map[core.Category]bool{}
	var out []core.Category
	for _, e := range p.entries {
		if !seen[e.Category] {
			seen[e.Category] = true
			out = append(out, e.Category)
		}
	}
	return out
}

// Scan expands every entry concurrently and emits one item per group/match.
func (p *Provider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, e := range p.entries {
		if ctx.Err() != nil {
			break
		}
		if e.Requires != "" && !env.Has(e.Requires) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(e Entry) {
			defer func() { <-sem; wg.Done() }()
			p.scanEntry(ctx, env, e, emit)
		}(e)
	}
	wg.Wait()
	return ctx.Err()
}

type match struct {
	path  string
	mtime time.Time
}

// Expand resolves the entry globs to existing, allowed paths.
func (e *Entry) Expand(env *core.Env) []match {
	seen := map[string]bool{}
	var out []match
	for _, pat := range e.Paths {
		pat = env.Expand(pat)
		found, _ := filepath.Glob(pat)
		for _, m := range found {
			m = filepath.Clean(m)
			if seen[m] || env.Excluded(m) || env.IsProtected(m) {
				continue
			}
			if excludedName(filepath.Base(m), e.Exclude) {
				continue
			}
			fi, err := os.Lstat(m)
			if err != nil {
				continue
			}
			if e.Files == fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
				// directory entry matched a file (or vice versa): ignore
				continue
			}
			if e.OlderThan > 0 && env.Now.Sub(fi.ModTime()) < e.OlderThan {
				continue
			}
			seen[m] = true
			out = append(out, match{path: m, mtime: fi.ModTime()})
		}
	}
	if e.Mode == Each && e.KeepLatest > 0 && len(out) > 0 {
		sort.Slice(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
		if len(out) <= e.KeepLatest {
			return nil
		}
		out = out[e.KeepLatest:]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func excludedName(base string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := filepath.Match(g, base); ok {
			return true
		}
	}
	return false
}

func (p *Provider) scanEntry(ctx context.Context, env *core.Env, e Entry, emit core.Emit) {
	ms := e.Expand(env)
	if len(ms) == 0 {
		return
	}
	switch e.Mode {
	case Each:
		for _, m := range ms {
			if ctx.Err() != nil {
				return
			}
			it := p.baseItem(env, e)
			it.ID = "catalog:" + e.ID + ":" + m.path
			it.Name = e.Name + " · " + filepath.Base(m.path)
			if e.Method == core.MethodCommand {
				it.Location = m.path
			} else {
				it.Path = m.path
			}
			it.LastUsed = m.mtime
			it.Sizing = true
			emit(it.Clone())
			st, _ := fsx.Size(ctx, m.path, nil)
			it.Sizing = false
			it.Size, it.Files = st.Bytes, st.Files
			if st.Reclaim < st.Bytes {
				it.Reclaim = st.Reclaim
			}
			if st.Newest.After(it.LastUsed) {
				it.LastUsed = st.Newest
			}
			if it.Size >= e.MinBytes && it.Size > 0 {
				emit(it)
			} else {
				hidden := it.Clone()
				hidden.Selectable = false
				hidden.Size = 0
				emit(hidden)
			}
		}
	default:
		it := p.baseItem(env, e)
		it.ID = "catalog:" + e.ID
		paths := make([]string, len(ms))
		for i, m := range ms {
			paths[i] = m.path
			if m.mtime.After(it.LastUsed) {
				it.LastUsed = m.mtime
			}
		}
		if e.Method == core.MethodCommand || len(ms) > 1 {
			it.Location = commonLocation(env, paths)
			if e.Method != core.MethodCommand {
				it.Paths = paths
			}
			if len(ms) > 1 {
				it.Name = fmt.Sprintf("%s (%d)", e.Name, len(ms))
			}
		} else {
			it.Path = paths[0]
		}
		it.Sizing = true
		emit(it.Clone())
		var total, reclaim, files int64
		for _, m := range ms {
			st, _ := fsx.Size(ctx, m.path, nil)
			total += st.Bytes
			reclaim += st.Reclaim
			files += st.Files
		}
		it.Sizing = false
		it.Size, it.Files = total, files
		if reclaim < total {
			it.Reclaim = reclaim
		}
		if it.Size < e.MinBytes || it.Size == 0 {
			it.Selectable = false
		}
		emit(it)
	}
}

func (p *Provider) baseItem(env *core.Env, e Entry) *core.Item {
	it := &core.Item{
		Provider:     p.ID(),
		Category:     e.Category,
		Kind:         e.ID,
		Name:         e.Name,
		Risk:         e.Risk,
		Method:       e.Method,
		Command:      e.Command,
		ProcessGuard: e.ProcessGuard,
		Note:         e.Note,
		Selectable:   true,
		AllowGitRepo: e.AllowGitRepo,
		Recommended:  e.Recommended,
	}
	if running := runningGuard(e.ProcessGuard); running != "" {
		it.Warn = running + " is running — quit it before cleaning"
	}
	return it
}

// commonLocation returns the deepest common directory of paths, prettified.
func commonLocation(env *core.Env, paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return paths[0]
	}
	common := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for !fsx.Within(p, common) && common != "/" {
			common = filepath.Dir(common)
		}
	}
	return strings.TrimSuffix(common, "/") + "/…"
}
