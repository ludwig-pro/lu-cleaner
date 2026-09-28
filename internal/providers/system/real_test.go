package system

import (
	"context"
	"fmt"
	"os"
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

// TestRealMachine scans this Mac, read-only, and prints the items.
// Run with: LU_REAL=1 go test ./internal/providers/system -run TestRealMachine -v
// (LU_META=1 also prints meta and notes; LU_CATALOG=1 prints this domain's
// static catalog entries instead).
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
	g := safety.New(env.Home, env.TmpDir, append(append([]string(nil), env.Roots...), env.WorktreeRoots...), nil)
	env.Protected = g.Protected
	env.Logf = t.Logf

	var mu sync.Mutex
	items := map[string]*core.Item{}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	collect := func(it *core.Item) {
		mu.Lock()
		items[it.ID] = it
		mu.Unlock()
	}
	var err error
	if os.Getenv("LU_CATALOG") == "1" {
		// Static entries of this domain (data_system.go), evaluated by the catalog.
		err = catalog.New().Scan(ctx, env, func(it *core.Item) {
			for _, pre := range []string{"browser-", "app-", "system-", "ide-", "containers-", "langs-"} {
				if strings.HasPrefix(it.Kind, pre) {
					collect(it)
				}
			}
		})
	} else {
		err = New().Scan(ctx, env, collect)
	}
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
		if it.CanClean() && it.Method != core.MethodCommand {
			for _, p := range it.Targets() {
				if err := g.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
					t.Errorf("guard refuses %s: %v", p, err)
				}
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Size > list[j].Size })
	var total, rec int64
	for _, it := range list {
		flag := " "
		if core.Recommend(it, env.Now, 14*24*time.Hour) {
			flag = "*"
			rec += it.Freed()
		}
		if it.CanClean() {
			total += it.Freed()
		}
		fmt.Printf("%s %-9s %-8s %-8s %-6s %-30s %-60s %s | %s\n", flag, fsx.Bytes(it.Size), it.Risk, it.Method,
			fsx.Age(it.Age(env.Now)), it.Kind, it.Name, env.Pretty(it.Where()), it.Warn)
		if os.Getenv("LU_META") == "1" {
			fmt.Printf("      meta=%v cmd=%v post=%v guard=%v\n      note=%s\n", it.Meta, it.Command, it.PostCommands, it.ProcessGuard, it.Note)
		}
	}
	fmt.Printf("items=%d cleanable total=%s recommended=%s took=%s\n", len(list), fsx.Bytes(total), fsx.Bytes(rec), took)
}
