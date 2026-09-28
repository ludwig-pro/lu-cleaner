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
//   - safe items: always (caches), if not tiny
//   - moderate items: when unused for longer than staleAfter
//   - caution items: never automatically
//
// Providers may force a recommendation with Item.Recommended.
func Recommend(it *Item, now time.Time, staleAfter time.Duration) bool {
	if !it.CanClean() || it.Sizing {
		return false
	}
	if it.Recommended {
		return it.Risk <= RiskModerate || it.Warn == ""
	}
	switch it.Risk {
	case RiskSafe:
		return it.Size >= 1<<20 && it.Warn == ""
	case RiskModerate:
		age := it.Age(now)
		return age > 0 && age >= staleAfter && it.Warn == ""
	}
	return false
}

// TopLevel drops items made redundant by other items of the list: an item is
// dropped when every one of its targets (Path, or Paths for group items) is
// equal to or inside a target of another kept item. On identical targets the
// first item of the list wins. Command items (no targets) are kept, identical
// commands deduplicated. Returned order: by first target path, then commands.
func TopLevel(items []*Item) []*Item {
	type target struct {
		path string
		idx  int
	}
	var targets []target
	for i, it := range items {
		for _, p := range it.Targets() {
			targets = append(targets, target{p, i})
		}
		if len(it.Targets()) == 0 && it.Covers != "" {
			targets = append(targets, target{it.Covers, i})
		}
	}
	sort.SliceStable(targets, func(a, b int) bool {
		if targets[a].path != targets[b].path {
			return targets[a].path < targets[b].path
		}
		return targets[a].idx < targets[b].idx
	})
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
			if t.path == top || strings.HasPrefix(t.path, top+"/") {
				break
			}
			stack = stack[:len(stack)-1]
		}
		covered := false
		for _, a := range stack {
			if a.idx != t.idx {
				covered = true
				break
			}
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
			min := ts[0]
			for _, p := range ts[1:] {
				if p < min {
					min = p
				}
			}
			firstPath[it] = min
		}
	}
	sort.SliceStable(withTargets, func(a, b int) bool { return firstPath[withTargets[a]] < firstPath[withTargets[b]] })
	return append(withTargets, commands...)
}

// Total sums Freed() over the cleanable top-level items (no double counting
// of nested paths; report-only and non-selectable items are left out).
func Total(items []*Item) int64 {
	var cleanable []*Item
	for _, it := range items {
		if it.CanClean() {
			cleanable = append(cleanable, it)
		}
	}
	var n int64
	for _, it := range TopLevel(cleanable) {
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
