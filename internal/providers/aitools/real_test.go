package aitools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// TestRealMachine scans the real home directory, read-only, and prints what
// the provider (and the "ai-" catalog entries) would propose.
// Run with: LU_REAL=1 go test ./internal/providers/aitools -run TestRealMachine -v
func TestRealMachine(t *testing.T) {
	if os.Getenv("LU_REAL") == "" {
		t.Skip("set LU_REAL=1 to scan the real machine (read-only)")
	}
	env := core.NewEnv()
	env.Logf = func(f string, a ...any) { fmt.Printf("# "+f+"\n", a...) }
	g := safety.New(env.Home, env.TmpDir, nil, nil)
	var roots []string
	for _, r := range config.DefaultWorktreeRoots {
		roots = append(roots, env.Expand(r))
	}
	env.Protected = func(p string) bool {
		if g.Protected(p) {
			return true
		}
		for _, r := range roots {
			if fsx.Within(strings.ToLower(r), strings.ToLower(filepath.Clean(p))) {
				return true
			}
		}
		return false
	}
	var mu sync.Mutex
	items := map[string]*core.Item{}
	collect := func(it *core.Item) {
		mu.Lock()
		items[it.ID] = it
		mu.Unlock()
	}
	start := time.Now()
	if err := New().Scan(context.Background(), env, collect); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if os.Getenv("LU_REAL_CATALOG") != "" {
		cat := catalog.New()
		_ = cat.Scan(context.Background(), env, func(it *core.Item) {
			if strings.HasPrefix(it.Kind, "ai-") {
				collect(it)
			}
		})
	}
	var list []*core.Item
	for _, it := range items {
		list = append(list, it)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Size > list[j].Size })
	var total, rec int64
	for _, it := range list {
		mark := " "
		if core.Recommend(it, env.Now, 30*24*time.Hour) {
			mark = "*"
			rec += it.Freed()
		}
		if it.CanClean() {
			total += it.Freed()
		}
		fmt.Printf("%s %9s  %-8s %-7s %-4s %-42s %s  %s\n", mark, fsx.Bytes(it.Size), it.Risk, it.Method,
			fsx.Age(it.Age(env.Now)), it.Kind, it.Name, env.Pretty(it.Where()))
		if it.Warn != "" {
			fmt.Printf("      ! %s\n", it.Warn)
		}
	}
	fmt.Printf("\n%d items, cleanable %s, smart-selected %s, provider scan %s\n", len(list), fsx.Bytes(total), fsx.Bytes(rec), took.Round(time.Millisecond))
}
