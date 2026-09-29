package worktrees

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// artifactOrder is the display order of the size breakdown (Meta keys).
var artifactOrder = []string{
	"node_modules", "ios/Pods", "ios/build", "android/build", "android/app/build",
	"android/.gradle", "android/.cxx", ".expo", ".next", ".turbo", "dist",
}

// artifactKey classifies a directory met while sizing a worktree: rel is its
// path relative to the worktree ("apps/app/ios/Pods"). "" = not an artifact.
func artifactKey(rel string) string {
	parts := strings.Split(rel, "/")
	n := len(parts)
	name := parts[n-1]
	parent, grand := "", ""
	if n >= 2 {
		parent = parts[n-2]
	}
	if n >= 3 {
		grand = parts[n-3]
	}
	switch name {
	case "node_modules":
		return "node_modules"
	case "Pods":
		if parent == "ios" {
			return "ios/Pods"
		}
	case "build":
		switch {
		case parent == "ios":
			return "ios/build"
		case parent == "app" && grand == "android":
			return "android/app/build"
		case parent == "android" || grand == "android":
			return "android/build"
		}
	case ".gradle":
		if parent == "android" {
			return "android/.gradle"
		}
	case ".cxx":
		if parent == "android" || grand == "android" {
			return "android/.cxx"
		}
	case ".expo", ".next", ".turbo", "dist":
		return name
	}
	return ""
}

// measurement is the size of a worktree with its artifact breakdown.
type measurement struct {
	total, reclaim, files int64
	artifacts             map[string]int64
	newest                time.Time
	nested                []string // repositories / worktrees found inside (not below artifacts)
}

// measure walks the worktree once, pruning artifact directories, then
// measures each artifact directory on its own and adds everything up. The
// walk also notes nested git repositories and worktrees (a .git entry of any
// type, or a bare layout): removing the worktree would delete them too.
func measure(ctx context.Context, root string) measurement {
	var mu sync.Mutex
	var found []struct{ path, key string }
	var nested []string
	skip := func(path, name string) bool {
		rel := strings.TrimPrefix(path, root+"/")
		if rel == path {
			return false
		}
		// (Nothing inside a .git directory counts: its modules/ look bare.)
		if name != ".git" && !strings.Contains("/"+rel+"/", "/.git/") &&
			(fsx.Exists(filepath.Join(path, ".git")) || looksBare(path)) {
			mu.Lock()
			nested = append(nested, path)
			mu.Unlock()
		}
		if k := artifactKey(rel); k != "" {
			mu.Lock()
			found = append(found, struct{ path, key string }{path, k})
			mu.Unlock()
			return true
		}
		return false
	}
	st, _ := fsx.Size(ctx, root, &fsx.Options{Skip: skip})
	sort.Strings(nested)
	m := measurement{
		total: st.Bytes, reclaim: st.Reclaim, files: st.Files,
		artifacts: map[string]int64{}, newest: st.Newest, nested: nested,
	}
	for _, f := range found {
		if ctx.Err() != nil {
			break
		}
		a, _ := fsx.Size(ctx, f.path, nil)
		m.total += a.Bytes
		m.reclaim += a.Reclaim
		m.files += a.Files
		m.artifacts[f.key] += a.Bytes
	}
	return m
}

// applySize sets the measured size on the item.
func (s *scan) applySize(w *worktree, it *core.Item, m measurement) {
	it.Sizing = false
	it.Size, it.Files = m.total, m.files
	// A tree fully shared with other files (hardlinks or APFS clones) frees
	// ~nothing: SetReclaim stores it as 1 byte, since 0 means "same as Size".
	it.SetReclaim(m.reclaim)
	var art int64
	keys := make([]string, 0, len(m.artifacts))
	for k, v := range m.artifacts {
		if v > 0 {
			keys = append(keys, k)
			art += v
		}
	}
	sort.Slice(keys, func(i, j int) bool { return orderOf(keys[i]) < orderOf(keys[j]) })
	if it.Meta == nil {
		it.Meta = map[string]string{}
	}
	for _, k := range keys {
		it.Meta[k] = fsx.Bytes(m.artifacts[k])
	}
	it.Meta["artifacts"] = fsx.Bytes(art)
	it.Meta["artifacts_bytes"] = strconv.FormatInt(art, 10)
	it.Meta["checkout"] = fsx.Bytes(m.total - art)
	if it.Reclaim > 0 {
		it.Meta["shared_hardlinks"] = fsx.Bytes(m.total - m.reclaim)
	}
	// An orphan has no git activity signal: its newest file is the best one.
	if w.orphan != "" && m.newest.After(it.LastUsed) && !m.newest.After(s.now.Add(24*time.Hour)) {
		it.LastUsed = m.newest
	}
	s.applyNested(w, it, m.nested)
}

// applyNested flags a worktree holding other repositories or worktrees that
// the scan had not seen (ignored clones, a gh-pages worktree in dist/...):
// removing it would delete them too, so it is caution (the cleaner refuses it
// without --force) and an orphan is no longer deleted with rm -rf.
func (s *scan) applyNested(w *worktree, it *core.Item, found []string) {
	known := map[string]bool{}
	for _, p := range w.nested {
		known[p] = true
	}
	var extra []string
	for _, p := range found {
		if !known[p] {
			extra = append(extra, p)
		}
	}
	if len(extra) == 0 {
		return
	}
	all := append(append([]string(nil), w.nested...), extra...)
	it.Meta["nested"] = strings.Join(all, ", ")
	it.Risk = core.RiskCaution
	msg := nestedWarn(s, extra)
	if it.Warn == "" {
		it.Warn = msg
	} else {
		it.Warn += " · " + msg
	}
	if it.Method == core.MethodDelete {
		it.Method = core.MethodReport
		it.Selectable = false
		it.AllowGitRepo = false
		it.RequireForce = false
		it.Recheck = nil
	}
	s.recommend(w, it)
}

func orderOf(k string) int {
	for i, x := range artifactOrder {
		if x == k {
			return i
		}
	}
	return len(artifactOrder)
}
