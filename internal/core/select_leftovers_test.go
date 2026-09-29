package core

import (
	"slices"
	"sort"
	"testing"
)

func topIDs(items []*Item) []string {
	var ids []string
	for _, it := range TopLevel(items) {
		ids = append(ids, it.ID)
	}
	sort.Strings(ids)
	return ids
}

// permutations returns every order of items (small lists only).
func permutations(items []*Item) [][]*Item {
	if len(items) <= 1 {
		return [][]*Item{append([]*Item(nil), items...)}
	}
	var out [][]*Item
	for i := range items {
		rest := append(append([]*Item(nil), items[:i]...), items[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]*Item{items[i]}, p...))
		}
	}
	return out
}

// K1: a worktree nested in another selected worktree must reach the
// executor, which removes it first (with its own checks) and keeps the
// outer one when the inner one stays. Dropping it made the executor refuse
// the outer worktree ("contains another registered worktree").
func TestTopLevelKeepsNestedWorktrees(t *testing.T) {
	outer := &Item{ID: "outer", Path: "/h/wt/a", Method: MethodWorktree, Size: 100, Selectable: true}
	inner := &Item{ID: "inner", Path: "/h/wt/a/.claude/worktrees/b", Method: MethodWorktree, Size: 40, Selectable: true}
	deepest := &Item{ID: "deepest", Path: "/h/wt/a/.claude/worktrees/b/sub/c", Method: MethodWorktree, Size: 10, Selectable: true}
	innerNM := &Item{ID: "innerNM", Path: "/h/wt/a/.claude/worktrees/b/node_modules", Size: 20, Selectable: true}
	outerNM := &Item{ID: "outerNM", Path: "/h/wt/a/node_modules", Size: 30, Selectable: true}
	for _, order := range permutations([]*Item{outer, inner, deepest, innerNM, outerNM}) {
		if got, want := topIDs(order), []string{"deepest", "inner", "outer"}; !slices.Equal(got, want) {
			t.Fatalf("TopLevel = %v, want %v (order %v)", got, want, ids(order))
		}
		// Sizes: the nested worktrees are inside the outer one's size.
		if n := Total(order); n != 100 {
			t.Fatalf("Total = %d, want 100 (nested worktrees counted twice)", n)
		}
	}

	// A duplicate of the inner worktree is still deduplicated.
	dup := &Item{ID: "dup", Path: inner.Path, Method: MethodWorktree, Size: 40, Selectable: true}
	if got, want := topIDs([]*Item{outer, inner, dup}), []string{"inner", "outer"}; !slices.Equal(got, want) {
		t.Errorf("duplicate nested worktree: %v, want %v", got, want)
	}

	// A worktree inside a plain (non-worktree) item is dropped as before,
	// and so is a worktree nested in a worktree that is itself dropped.
	folder := &Item{ID: "folder", Path: "/h/wt", Method: MethodDelete, Size: 500, Selectable: true}
	for _, order := range permutations([]*Item{folder, outer, inner}) {
		if got, want := topIDs(order), []string{"folder"}; !slices.Equal(got, want) {
			t.Errorf("worktrees inside a deleted folder: %v, want %v", got, want)
		}
	}
	// A plain item nested in a worktree does not protect a worktree below it.
	sub := &Item{ID: "sub", Path: "/h/wt/a/.claude", Method: MethodDelete, Size: 50, Selectable: true}
	for _, order := range permutations([]*Item{outer, sub, inner}) {
		if got, want := topIDs(order), []string{"inner", "outer"}; !slices.Equal(got, want) {
			t.Errorf("plain item between two worktrees: %v, want %v", got, want)
		}
	}
	// Non-worktree items nested in each other keep the old rule.
	a := &Item{ID: "a", Path: "/h/x", Method: MethodDelete, Selectable: true}
	b := &Item{ID: "b", Path: "/h/x/y", Method: MethodWorktree, Selectable: true}
	if got, want := topIDs([]*Item{b, a}), []string{"a"}; !slices.Equal(got, want) {
		t.Errorf("worktree inside a delete item: %v, want %v", got, want)
	}
}

// K2: a path item and a command whose Covers is the same directory: the
// path item wins whatever the list order (it is never dropped because of a
// Covers equal to its path), the command is dropped.
func TestTopLevelCoversTieIsOrderIndependent(t *testing.T) {
	dir := &Item{ID: "dir", Path: "/h/Devices/U1", Method: MethodDelete, Size: 10, Selectable: true}
	cmd := &Item{ID: "cmd", Command: []string{"xcrun", "simctl", "delete", "U1"}, Covers: "/h/Devices/U1", Method: MethodCommand, Size: 12, Selectable: true}
	att := &Item{ID: "att", Paths: []string{"/h/Devices/U1/data/tmp/a.mov"}, Size: 7, Selectable: true}
	for _, order := range permutations([]*Item{dir, cmd, att}) {
		if got, want := topIDs(order), []string{"dir"}; !slices.Equal(got, want) {
			t.Errorf("order %v: TopLevel = %v, want %v", ids(order), got, want)
		}
		if n := Total(order); n != 10 {
			t.Errorf("order %v: Total = %d, want 10", ids(order), n)
		}
	}
	// A group item listing the covered directory wins too.
	grp := &Item{ID: "grp", Paths: []string{"/h/Devices/U1", "/h/other"}, Size: 20, Selectable: true}
	for _, order := range permutations([]*Item{grp, cmd}) {
		if got, want := topIDs(order), []string{"grp"}; !slices.Equal(got, want) {
			t.Errorf("group: order %v: TopLevel = %v, want %v", ids(order), got, want)
		}
	}
	// Covers inside a path item: the command is dropped; a path item inside
	// Covers: the path item is dropped (unchanged).
	parent := &Item{ID: "parent", Path: "/h/Devices", Size: 50, Selectable: true}
	for _, order := range permutations([]*Item{parent, cmd}) {
		if got, want := topIDs(order), []string{"parent"}; !slices.Equal(got, want) {
			t.Errorf("covers inside path: order %v: %v, want %v", ids(order), got, want)
		}
	}
	for _, order := range permutations([]*Item{att, cmd}) {
		if got, want := topIDs(order), []string{"cmd"}; !slices.Equal(got, want) {
			t.Errorf("path inside covers: order %v: %v, want %v", ids(order), got, want)
		}
	}
	// pnpm store + prune (Covers = <store>/files): the store wins in both orders.
	store := &Item{ID: "store", Path: "/h/Library/pnpm/store/v10", Size: 100, Selectable: true}
	prune := &Item{ID: "prune", Command: []string{"pnpm", "store", "prune"}, Covers: "/h/Library/pnpm/store/v10/files", Method: MethodCommand, Selectable: true}
	for _, order := range permutations([]*Item{store, prune}) {
		if got, want := topIDs(order), []string{"store"}; !slices.Equal(got, want) {
			t.Errorf("pnpm: order %v: %v, want %v", ids(order), got, want)
		}
	}
}

func ids(items []*Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
