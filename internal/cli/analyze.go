package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"github.com/ludwig-pro/lu-cleaner/internal/tui"
)

func (c *cli) analyzeCmd() *cobra.Command {
	var (
		top int
		all bool
	)
	cmd := &cobra.Command{
		Use:   "analyze [path]",
		Short: "Explore a directory tree by size (ncdu-like, default: home)",
		Long: `Navigate any directory tree sorted by size and delete what you do not need.
Without a terminal (or with --json), prints the size of the direct children.`,
		Example: `  lu-cleaner analyze
  lu-cleaner analyze ~/Library
  lu-cleaner analyze . --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := c.newScanSetup(nil, cmd.Context())
			if err != nil {
				return err
			}
			root := s.env.Home
			if len(args) == 1 {
				root = absPath(s.env, args[0], "")
			}
			if r, err := filepath.EvalSymlinks(root); err == nil {
				root = r
			}
			fi, err := os.Stat(root)
			if err != nil || !fi.IsDir() {
				return usageErr("%s is not a directory", root)
			}
			ctx, err := c.startScan(cmd.Context(), s)
			if err != nil {
				return err
			}
			if c.interactive() && !c.f.json {
				c.propagateNoColor()
				return c.Analyze(ctx, tui.AnalyzeOptions{Env: s.env, Root: root, Clean: s.clean})
			}
			if all {
				top = 0
			}
			return c.analyzeReport(ctx, s, root, top)
		},
	}
	cmd.Flags().IntVar(&top, "top", 25, "entries listed without a terminal (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "list every entry")
	return cmd
}

type dirEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Dir        bool   `json:"dir"`
	Size       int64  `json:"size"`
	Files      int64  `json:"files"`
	Unreadable int64  `json:"unreadable,omitempty"`
}

// analyzeReport prints the direct children of root sorted by size.
func (c *cli) analyzeReport(ctx context.Context, s *setup, root string, top int) error {
	ctx = scanctl.Ensure(ctx)
	limits := scanctl.From(ctx).Limits()
	des, err := fsx.ReadDir(ctx, root)
	if err != nil {
		return err
	}
	entries := make([]dirEntry, len(des))
	var done atomic.Int64
	var total atomic.Int64
	sp := c.startSpinner(func() string {
		return fmt.Sprintf("analyzing… %s · %d/%d entries · %s", limits.Mode, done.Load(), len(des), fsx.Bytes(total.Load()))
	})
	defer sp.stop()
	var wg sync.WaitGroup
	ctx, group := diagnostics.NewGroup(ctx, "cli")
	defer group.Close()
	jobs := make(chan int)
	for range min(limits.Prefetch, len(des)) {
		wg.Go(func() {
			defer group.Recover()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				de := des[i]
				p := filepath.Join(root, de.Name())
				st, _ := fsx.Size(ctx, p, nil)
				entries[i] = dirEntry{Name: de.Name(), Path: p, Dir: de.IsDir(), Size: st.Bytes, Files: st.Files, Unreadable: st.Errors}
				done.Add(1)
				total.Add(st.Bytes)
			}
		})
	}
queue:
	for i := range des {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break queue
		}
	}
	close(jobs)
	wg.Wait()
	sp.stop()
	if err := group.Err(); err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Size > entries[j].Size })
	sum := total.Load()

	if c.f.json {
		return c.writeJSON(map[string]any{"root": root, "total": sum, "entries": entries})
	}
	o := c.out
	o.printf("%s  %s\n", o.paint(o.title, sanitize(s.env.Pretty(root))), o.sizeText(sum))
	t := newTable("SIZE", "SHARE", "FILES", "NAME")
	t.right[0], t.right[2] = true, true
	t.shrink = 3
	shown := entries
	if top > 0 && len(shown) > top {
		shown = shown[:top]
	}
	var unreadable int64
	for _, e := range entries {
		unreadable += e.Unreadable
	}
	for _, e := range shown {
		name := e.Name
		if e.Dir {
			name += "/"
		}
		pct := 0.0
		if sum > 0 {
			pct = float64(e.Size) / float64(sum) * 100
		}
		size := e.Size
		t.add(func(col int, v string) string {
			switch col {
			case 0:
				return o.sizeText(size)
			case 1:
				return o.paint(o.accent, v)
			case 2:
				return o.paint(o.faint, v)
			}
			return v
		}, fsx.Bytes(e.Size), fmt.Sprintf("%s %4.1f%%", bar(pct, 10), pct), formatCount(int(e.Files)), name)
	}
	t.render(o, "  ", true)
	if rest := entries[len(shown):]; len(rest) > 0 {
		var b int64
		for _, e := range rest {
			b += e.Size
		}
		o.println(o.paint(o.faint, fmt.Sprintf("  … %s more (%s) — use --all", formatCount(len(rest)), fsx.Bytes(b))))
	}
	if unreadable > 0 {
		o.println(o.paint(o.warn, fmt.Sprintf("  %s unreadable entries: sizes are partial (grant Full Disk Access to your terminal).", formatCount(int(unreadable)))))
	}
	return nil
}

// bar renders pct (0-100) as a bar of width cells.
func bar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	full := int(pct/100*float64(width) + 0.5)
	return strings.Repeat("█", full) + strings.Repeat("░", width-full)
}
