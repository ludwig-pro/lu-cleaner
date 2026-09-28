package worktrees

import (
	"context"
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
}

// measure walks the worktree once, pruning artifact directories, then
// measures each artifact directory on its own and adds everything up.
func measure(ctx context.Context, root string) measurement {
	var mu sync.Mutex
	var found []struct{ path, key string }
	skip := func(path, _ string) bool {
		rel := strings.TrimPrefix(path, root+"/")
		if rel == path {
			return false
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
	m := measurement{
		total: st.Bytes, reclaim: st.Reclaim, files: st.Files,
		artifacts: map[string]int64{}, newest: st.Newest,
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
	if m.reclaim < m.total {
		it.Reclaim = m.reclaim
	}
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
}

func orderOf(k string) int {
	for i, x := range artifactOrder {
		if x == k {
			return i
		}
	}
	return len(artifactOrder)
}
