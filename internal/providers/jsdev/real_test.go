package jsdev

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

// TestRealMachine scans THIS machine read-only and prints what would be
// proposed (jsdev provider + the js-* catalog entries). Nothing is deleted.
//
//	LU_REAL=1 go test ./internal/providers/jsdev -run TestRealMachine -v
func TestRealMachine(t *testing.T) {
	if os.Getenv("LU_REAL") != "1" {
		t.Skip("set LU_REAL=1 to scan this machine (read-only)")
	}
	env := core.NewEnv()
	existing := func(list []string) []string {
		var out []string
		for _, p := range list {
			p = env.Expand(p)
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				out = append(out, p)
			}
		}
		return out
	}
	env.Roots = existing(config.DefaultRootCandidates)
	env.WorktreeRoots = existing(config.DefaultWorktreeRoots)
	roots := append(append([]string{}, env.Roots...), env.WorktreeRoots...)
	g := safety.New(env.Home, env.TmpDir, nil, nil)
	env.Protected = func(p string) bool {
		if g.Protected(p) {
			return true
		}
		k := strings.ToLower(filepath.Clean(p))
		for _, r := range roots {
			if fsx.Within(strings.ToLower(r), k) {
				return true
			}
		}
		return false
	}

	var mu sync.Mutex
	items := map[string]*core.Item{}
	emit := func(it *core.Item) {
		mu.Lock()
		items[it.ID] = it
		mu.Unlock()
	}
	start := time.Now()
	if err := New().Scan(context.Background(), env, emit); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	catN := 0
	if err := catalog.New().Scan(context.Background(), env, func(it *core.Item) {
		mu.Lock()
		catN++
		mu.Unlock()
		if strings.HasPrefix(it.Kind, "js-") {
			emit(it)
		}
	}); err != nil {
		t.Fatal(err)
	}

	var list []*core.Item
	for _, it := range items {
		if it.Size > 0 || it.CanClean() {
			list = append(list, it)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Size > list[j].Size })
	var rec []*core.Item
	for _, it := range list {
		r := core.Recommend(it, env.Now, 14*24*time.Hour)
		if r {
			rec = append(rec, it)
		}
		mark := " "
		if r {
			mark = "*"
		}
		fmt.Printf("%s %9s %-8s %-7s %-22s %-48s %s", mark, fsx.Bytes(it.Freed()), it.Risk, it.Method, it.Kind, trunc(it.Name, 48), env.Pretty(it.Where()))
		if it.Warn != "" {
			fmt.Printf("  ⚠ %s", it.Warn)
		}
		fmt.Println()
		if os.Getenv("LU_REAL_VERBOSE") == "1" {
			fmt.Printf("      note: %s\n      meta: %v  last used: %s\n", it.Note, it.Meta, it.LastUsed.Format("2006-01-02"))
		}
	}
	fmt.Printf("catalog emitted %d items (all domains)\n", catN)
	fmt.Printf("\n%d items, total %s, recommended %s (%d items); jsdev provider scan took %s\n",
		len(list), fsx.Bytes(core.Total(list)), fsx.Bytes(core.Total(rec)), len(rec), took.Round(time.Millisecond))
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
