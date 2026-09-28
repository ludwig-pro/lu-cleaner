package artifacts

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// TestRealMachine scans the real machine, read-only, and prints the items.
// Run with: LU_REAL=1 go test ./internal/providers/artifacts -run TestRealMachine -v
func TestRealMachine(t *testing.T) {
	if os.Getenv("LU_REAL") != "1" {
		t.Skip("set LU_REAL=1 to scan this machine (read-only)")
	}
	env := core.NewEnv()
	for _, r := range config.DefaultRootCandidates {
		if p := env.Expand(r); fsx.IsDir(p) {
			env.Roots = append(env.Roots, p)
		}
	}
	for _, r := range config.DefaultWorktreeRoots {
		if p := env.Expand(r); fsx.IsDir(p) {
			env.WorktreeRoots = append(env.WorktreeRoots, p)
		}
	}
	roots := append(append([]string(nil), env.Roots...), env.WorktreeRoots...)
	g := safety.New(env.Home, env.TmpDir, roots, nil)
	scanGuard := safety.New(env.Home, env.TmpDir, nil, nil)
	env.Protected = func(p string) bool {
		if scanGuard.Protected(p) {
			return true
		}
		for _, r := range roots {
			if fsx.Within(r, p) {
				return true
			}
		}
		return false
	}
	if os.Getenv("LU_DEBUG") == "1" {
		env.Logf = t.Logf
	}

	var mu sync.Mutex
	items := map[string]*core.Item{}
	var placeholders int
	start := time.Now()
	var first time.Duration
	err := New().Scan(context.Background(), env, func(it *core.Item) {
		mu.Lock()
		if it.Sizing {
			placeholders++
			if first == 0 {
				first = time.Since(start)
			}
		}
		items[it.ID] = it
		mu.Unlock()
	})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	var list []*core.Item
	for _, it := range items {
		list = append(list, it)
		if it.Sizing {
			t.Errorf("item still sizing: %s", it.ID)
		}
		if it.CanClean() {
			for _, p := range it.Targets() {
				if err := g.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
					t.Errorf("guard refuses %s: %v", p, err)
				}
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Size > list[j].Size })
	byKind := map[string]int64{}
	var total, rec int64
	for _, it := range list {
		flag := " "
		if core.Recommend(it, env.Now, 14*24*time.Hour) {
			flag = "*"
		}
		if it.CanClean() {
			byKind[it.Kind] += it.Freed()
		}
		if os.Getenv("LU_ALL") != "1" && it.Size < 50<<20 && it.Method != core.MethodReport {
			continue
		}
		fmt.Printf("%s %-9s %-8s %-8s %-6s %-20s %-70s %s | %s\n", flag, fsx.Bytes(it.Size), it.Risk, it.Method,
			fsx.Age(it.Age(env.Now)), it.Kind, it.Name, env.Pretty(it.Where()), it.Warn)
		if os.Getenv("LU_META") == "1" {
			fmt.Printf("      meta=%v reclaim=%s note=%s\n", it.Meta, fsx.Bytes(it.Reclaim), it.Note)
		}
	}
	for _, it := range core.TopLevel(list) {
		if it.CanClean() {
			total += it.Freed()
			if core.Recommend(it, env.Now, 14*24*time.Hour) {
				rec += it.Freed()
			}
		}
	}
	var kinds []string
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return byKind[kinds[i]] > byKind[kinds[j]] })
	for _, k := range kinds {
		fmt.Printf("  %-22s %s\n", k, fsx.Bytes(byKind[k]))
	}
	fmt.Printf("items=%d placeholders=%d first-placeholder=%s cleanable(top-level)=%s recommended=%s took=%s\n",
		len(list), placeholders, first, fsx.Bytes(total), fsx.Bytes(rec), took)
}
