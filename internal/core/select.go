package core

import (
	"sort"
	"strings"
	"time"
)

// Filter selects items on the command line and in the TUI.
type Filter struct {
	Categories []Category // empty = all
	Kinds      []string   // empty = all (matches Item.Kind or Provider)
	MinSize    int64
	OlderThan  time.Duration // only items whose LastUsed is older (unknown age never matches when > 0)
	MaxRisk    Risk          // highest risk allowed — the zero value (RiskSafe) hides moderate+ items: set it explicitly (RiskNever = everything)
	Query      string        // case-insensitive substring over name, path, kind, project
}

// Match reports whether it passes the filter.
func (f *Filter) Match(it *Item, now time.Time) bool {
	if len(f.Categories) > 0 {
		ok := false
		for _, c := range f.Categories {
			if it.Category == c {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.Kinds) > 0 {
		ok := false
		for _, k := range f.Kinds {
			if strings.EqualFold(k, it.Kind) || strings.EqualFold(k, it.Provider) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.MinSize > 0 && it.Size < f.MinSize && !it.AlwaysShow && it.Method != MethodCommand && !it.Sizing {
		return false
	}
	if f.OlderThan > 0 {
		if it.LastUsed.IsZero() || now.Sub(it.LastUsed) < f.OlderThan {
			return false
		}
	}
	if it.Risk > f.MaxRisk {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		hay := strings.ToLower(it.Name + " " + it.Where() + " " + it.Kind + " " + it.Project)
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

// Recommend is the "smart select" policy: what a sensible user would clean
// without thinking twice.
//   - never: caution/never items, items with a warning (Warn), items being sized
//   - provider veto (Item.NoRecommend): never, whatever the risk and age
//     (Item.Recommended=false only means "no opinion": it cannot veto)
//   - project artifacts of a project active in the last 24 hours: never
//   - provider-forced (Item.Recommended): yes
//   - safe items: yes, if not tiny
//   - moderate items: when unused for longer than staleAfter
func Recommend(it *Item, now time.Time, staleAfter time.Duration) bool {
	if !it.CanClean() || it.Sizing || it.NoRecommend || it.Warn != "" || it.Risk > RiskModerate {
		return false
	}
	if it.Category == CatArtifacts && !it.LastUsed.IsZero() && now.Sub(it.LastUsed) < 24*time.Hour {
		return false // don't wipe the build outputs of the project you are working on
	}
	if it.Recommended {
		return true
	}
	switch it.Risk {
	case RiskSafe:
		return it.Size >= 1<<20
	case RiskModerate:
		age := it.Age(now)
		return age > 0 && age >= staleAfter
	}
	return false
}

// TopLevel drops items made redundant by other items of the list: an item is
// dropped when every one of its targets (Path, or Paths for group items) is
// equal to or inside a target of another kept item. On identical targets the
// first item of the list wins, except that a path item always wins over a
// command's Covers (see below). Returned order: by first target path, then
// commands.
//
// Command items (no targets) are kept, identical commands deduplicated. A
// command with Covers (the directory it removes entirely) takes part as that
// directory: the items inside it are dropped, and the command itself is
// dropped when Covers is equal to or inside a path item's target. A path item
// is never dropped because of a Covers equal to its own path, so the result
// does not depend on the order of the list.
//
// A worktree item (MethodWorktree) strictly inside another worktree item is
// kept: git must remove the inner worktree first with its own checks, and
// the executor (clean.Run) orders worktrees deepest first and keeps the outer
// one when an inner one is not removed. A worktree inside a non-worktree item
// is dropped as usual. Total, which sums sizes, still counts such nested
// worktrees once.
func TopLevel(items []*Item) []*Item { return topLevel(items, true) }

// topLevel is TopLevel; nestedWorktrees=false also drops worktree items
// nested in another worktree item (for size totals).
func topLevel(items []*Item, nestedWorktrees bool) []*Item {
	type target struct {
		path  string
		idx   int
		cover bool // a command's Covers, not a path the item removes itself
	}
	var targets []target
	for i, it := range items {
		ts := it.Targets()
		for _, p := range ts {
			targets = append(targets, target{path: p, idx: i})
		}
		if len(ts) == 0 && it.Covers != "" {
			targets = append(targets, target{path: it.Covers, idx: i, cover: true})
		}
	}
	// Sort by path components: with a plain string order, siblings such as
	// "/a/app-web" or "/a/app 2" ('-' and ' ' sort before '/') would land
	// between "/a/app" and "/a/app/node_modules" and pop the parent off the
	// ancestor stack below, keeping both parent and child. On identical
	// paths, real targets come before Covers (a path item beats a command
	// covering the same directory, whatever the list order), then list order.
	sort.SliceStable(targets, func(a, b int) bool {
		ta, tb := targets[a], targets[b]
		if ta.path != tb.path {
			return pathLess(ta.path, tb.path)
		}
		if ta.cover != tb.cover {
			return !ta.cover
		}
		return ta.idx < tb.idx
	})
	worktree := func(i int) bool {
		return nestedWorktrees && items[i].Method == MethodWorktree && items[i].Path != ""
	}
	// A target is covered when an ancestor-or-equal target of another item precedes it.
	uncovered := make([]int, len(items))
	for i, it := range items {
		uncovered[i] = len(it.Targets())
		if uncovered[i] == 0 && it.Covers != "" {
			uncovered[i] = 1
		}
	}
	var stack []target // chain of ancestor targets
	for _, t := range targets {
		for len(stack) > 0 {
			top := stack[len(stack)-1].path
			if within(t.path, top) {
				break
			}
			stack = stack[:len(stack)-1]
		}
		covered := false
		for _, a := range stack {
			if a.idx == t.idx {
				continue
			}
			if a.path != t.path && worktree(t.idx) && worktree(a.idx) {
				continue // nested worktree: removed first, on its own
			}
			covered = true
			break
		}
		if covered {
			uncovered[t.idx]--
		} else {
			stack = append(stack, t)
		}
	}
	var withTargets []*Item
	var firstPath = map[*Item]string{}
	seen := map[string]bool{}
	var commands []*Item
	for i, it := range items {
		ts := it.Targets()
		if len(ts) == 0 {
			if it.Covers != "" && uncovered[i] == 0 {
				continue // covered by a path item or another command
			}
			k := strings.Join(it.Command, "\x00")
			if k != "" && seen[k] {
				continue
			}
			seen[k] = true
			commands = append(commands, it)
			continue
		}
		if uncovered[i] > 0 {
			withTargets = append(withTargets, it)
			first := ts[0]
			for _, p := range ts[1:] {
				if pathLess(p, first) {
					first = p
				}
			}
			firstPath[it] = first
		}
	}
	sort.SliceStable(withTargets, func(a, b int) bool { return pathLess(firstPath[withTargets[a]], firstPath[withTargets[b]]) })
	return append(withTargets, commands...)
}

// pathLess orders paths component by component ('/' sorts before every other
// byte), so that a directory is always immediately followed by its
// descendants: "/a/app" < "/a/app/x" < "/a/app-web" < "/a/app.old".
func pathLess(a, b string) bool {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ca == '/' {
			return true
		}
		if cb == '/' {
			return false
		}
		return ca < cb
	}
	return len(a) < len(b)
}

// within reports whether p equals dir or is below it.
func within(p, dir string) bool {
	if p == dir {
		return true
	}
	if dir == "/" {
		return strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, dir+"/")
}

// Total sums Freed() over the cleanable top-level items (no double counting
// of nested paths, worktrees nested in a worktree included; report-only and
// non-selectable items are left out).
func Total(items []*Item) int64 {
	var cleanable []*Item
	for _, it := range items {
		if it.CanClean() {
			cleanable = append(cleanable, it)
		}
	}
	var n int64
	for _, it := range topLevel(cleanable, false) {
		n += it.Freed()
	}
	return n
}

// SortBy sorts items in place. key: "size" (default, desc), "age" (oldest first), "name", "path".
func SortBy(items []*Item, key string, now time.Time) {
	switch key {
	case "age":
		sort.SliceStable(items, func(i, j int) bool {
			ai, aj := items[i].Age(now), items[j].Age(now)
			if ai != aj {
				return ai > aj
			}
			return items[i].Size > items[j].Size
		})
	case "name":
		sort.SliceStable(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	case "path":
		sort.SliceStable(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	default:
		sort.SliceStable(items, func(i, j int) bool { return items[i].Size > items[j].Size })
	}
}
