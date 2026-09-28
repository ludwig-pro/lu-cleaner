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
	MaxRisk    Risk          // highest risk allowed (RiskCaution = everything cleanable)
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
	if f.MinSize > 0 && it.Size < f.MinSize {
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

// TopLevel drops items nested inside another item of the list (same path
// prefix), keeping the outermost one. Items without a path are kept, with
// identical commands deduplicated. Order: by path.
func TopLevel(items []*Item) []*Item {
	var withPath, noPath []*Item
	for _, it := range items {
		if it.Path == "" {
			noPath = append(noPath, it)
		} else {
			withPath = append(withPath, it)
		}
	}
	sort.SliceStable(withPath, func(i, j int) bool { return withPath[i].Path < withPath[j].Path })
	var out []*Item
	var last string
	for _, it := range withPath {
		if last != "" && (it.Path == last || strings.HasPrefix(it.Path, last+"/")) {
			continue
		}
		out = append(out, it)
		last = it.Path
	}
	seen := map[string]bool{}
	for _, it := range noPath {
		k := strings.Join(it.Command, "\x00")
		if k != "" && seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, it)
	}
	return out
}

// Total sums Freed() over the top-level items (no double counting of nested paths).
func Total(items []*Item) int64 {
	var n int64
	for _, it := range TopLevel(items) {
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
