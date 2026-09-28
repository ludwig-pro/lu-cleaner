package apple

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// TestRealMachine scans this Mac read-only and prints what would be proposed.
// Run with: LU_REAL=1 go test ./internal/providers/apple -run TestRealMachine -v
func TestRealMachine(t *testing.T) {
	if os.Getenv("LU_REAL") == "" {
		t.Skip("set LU_REAL=1 to scan this machine (read-only)")
	}
	env := core.NewEnv()
	g := safety.New(env.Home, env.TmpDir, nil, nil)
	env.Protected = g.Protected
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var mu sync.Mutex
	items := map[string]*core.Item{}
	start := time.Now()
	collect := func(it *core.Item) {
		mu.Lock()
		items[it.ID] = it
		mu.Unlock()
	}
	if err := New().Scan(ctx, env, collect); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("apple provider: %d items in %s\n", len(items), time.Since(start).Round(time.Millisecond))
	// Static entries of the apple domain (catalog/data_apple.go).
	if err := catalog.New().Scan(ctx, env, func(it *core.Item) {
		if strings.HasPrefix(it.Kind, "apple-") {
			collect(it)
		}
	}); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	var list []*core.Item
	for _, it := range items {
		list = append(list, it)
		if it.Sizing {
			t.Errorf("%s still sizing", it.ID)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Size > list[j].Size })
	var total, rec int64
	for _, it := range list {
		flags := ""
		if core.Recommend(it, env.Now, 14*24*time.Hour) {
			flags += "R"
			rec += it.Freed()
		}
		if !it.CanClean() {
			flags += "-"
		}
		fmt.Printf("%-9s %-3s %-8s %-8s %-27s %-5s %-60s %s\n", fsx.Bytes(it.Size), flags, it.Risk, it.Method, it.Kind,
			fsx.Age(it.Age(env.Now)), trunc(it.Name, 60), env.Pretty(it.Where()))
		if it.Warn != "" {
			fmt.Printf("%-9s     warn: %s\n", "", it.Warn)
		}
		if os.Getenv("LU_REAL") == "2" {
			fmt.Printf("%-9s     note: %s\n%-9s     meta: %v\n", "", it.Note, "", it.Meta)
		}
		if it.Method == core.MethodCommand {
			fmt.Printf("%-9s     cmd: %s\n", "", strings.Join(it.Command, " "))
		}
	}
	for _, it := range core.TopLevel(list) {
		if it.CanClean() {
			total += it.Freed()
		}
	}
	fmt.Printf("\n%d items in %s — cleanable %s, recommended %s\n", len(list), took.Round(time.Millisecond), fsx.Bytes(total), fsx.Bytes(rec))
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
