package worktrees

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
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// TestRealMachine scans this machine read-only and prints what it finds.
// Run with: LU_REAL=1 go test -run TestRealMachine -v ./internal/providers/worktrees/
func TestRealMachine(t *testing.T) {
	if os.Getenv("LU_REAL") == "" {
		t.Skip("set LU_REAL=1 to scan the real machine (read-only)")
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

	start := time.Now()
	var mu sync.Mutex
	byID := map[string]*core.Item{}
	firstSeen := map[string]time.Duration{}
	err := New().Scan(context.Background(), env, func(it *core.Item) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := firstSeen[it.ID]; !ok {
			firstSeen[it.ID] = time.Since(start)
		}
		byID[it.ID] = it
	})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	var items []*core.Item
	for _, it := range byID {
		items = append(items, it)
		if it.Sizing {
			t.Errorf("%s still sizing", it.ID)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Size > items[j].Size })
	var total, rec, recN int64
	kinds := map[string]int{}
	risks := map[string]int{}
	for _, it := range items {
		kinds[it.Kind]++
		risks[it.Risk.String()+"/"+it.Method.String()]++
		if it.Method.Cleanable() {
			total += it.Freed()
		}
		if core.Recommend(it, env.Now, 14*24*time.Hour) {
			rec += it.Freed()
			recN++
		}
		age := fsx.Age(it.Age(env.Now))
		fmt.Printf("%-9s %-8s %-8s rec=%-5v %5s  %-8s %s\n    %s\n    warn=%s\n    meta: status=%s dirty=%s unpushed=%s merged=%s nm=%s artifacts=%s\n",
			fsx.Bytes(it.Size), it.Risk, it.Method, it.Recommended, age, it.Kind, it.Name, env.Pretty(it.Where()), it.Warn,
			it.Meta["status"], it.Meta["dirty"], it.Meta["unpushed"], it.Meta["merged"], it.Meta["node_modules"], it.Meta["artifacts"])
		if os.Getenv("LU_REAL") == "v" {
			fmt.Printf("    note=%s\n    meta=%v\n", it.Note, it.Meta)
		}
	}
	var firstEmit time.Duration = took
	for _, d := range firstSeen {
		if d < firstEmit {
			firstEmit = d
		}
	}
	fmt.Printf("\nfirst item after %s\n", firstEmit.Round(time.Millisecond))
	fmt.Printf("\n%d items in %s — cleanable total %s, recommended %d items / %s\nkinds=%v\nrisk/method=%v\n",
		len(items), took.Round(time.Millisecond), fsx.Bytes(total), recN, fsx.Bytes(rec), kinds, risks)
}
