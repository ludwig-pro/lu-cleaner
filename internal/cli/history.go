package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

func (c *cli) historyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "What was cleaned, when, and how much it freed",
		Long: `List what was cleaned, newest first, from the history file.
FREED and "Total freed" are the space really freed by permanent removals:
less than SIZE for data shared with other files (hardlinks or APFS clones),
what a failed deletion freed before failing. What was moved to the Trash
still uses its space until the Trash is emptied (it is totalled apart).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return usageErr("--limit must be >= 0")
			}
			entries, err := c.History()
			if err != nil {
				return err
			}
			// Total freed is what was really freed (HistoryEntry.Freed: less
			// than the size for data shared through hardlinks or APFS
			// clones, what a failed deletion freed before failing). Moves to
			// the Trash free nothing until it is emptied: totalled apart.
			var freed, trashed int64
			done, trashedN := 0, 0
			for _, e := range entries {
				if e.Method == core.MethodTrash.String() {
					if e.Status == clean.StatusDone.String() {
						trashed += e.Size
						trashedN++
					}
					continue
				}
				freed += max(e.Freed, 0)
				if e.Status == clean.StatusDone.String() {
					done++
				}
			}
			// Newest first.
			shown := make([]clean.HistoryEntry, 0, len(entries))
			for i := len(entries) - 1; i >= 0; i-- {
				if limit > 0 && len(shown) >= limit {
					break
				}
				shown = append(shown, entries[i])
			}
			if c.f.json {
				return c.writeJSON(map[string]any{
					"path": clean.HistoryPath(), "total": len(entries), "freed": freed, "trashed": trashed, "entries": shown,
				})
			}
			o := c.out
			if len(entries) == 0 {
				o.println("Nothing cleaned yet.")
				return nil
			}
			env := c.NewEnv()
			t := newTable("TIME", "STATUS", "METHOD", "SIZE", "FREED", "NAME", "PATH/COMMAND")
			t.right[3], t.right[4] = true, true
			t.maxw[5] = 36
			t.leftTrunc[6] = true
			t.shrink = 6
			for _, e := range shown {
				where := historyWhere(env, e)
				status := e.Status
				size, freedCell := historySizes(e)
				t.add(func(col int, v string) string {
					switch col {
					case 0:
						return o.paint(o.faint, v)
					case 1:
						switch status {
						case "done":
							return o.paint(o.good, v)
						case "failed":
							return o.paint(o.bad, v)
						}
						return o.paint(o.warn, v)
					case 3:
						if v == "?" {
							return o.paint(o.faint, v)
						}
						return o.sizeText(e.Size)
					case 4, 6:
						return o.paint(o.faint, v)
					}
					return v
				}, e.Time.Local().Format("2006-01-02 15:04"), e.Status, e.Method, size, freedCell, e.Name, where)
				if e.Error != "" {
					t.line("    " + o.paint(o.bad, "! "+prettyText(env, e.Error)))
				}
				if e.Message != "" && e.Message != e.Error && status != clean.StatusDone.String() {
					st := o.warn
					if e.Error == "" && status == clean.StatusFailed.String() {
						st = o.bad
					}
					t.line("    " + o.paint(st, "! "+prettyText(env, e.Message)))
				}
			}
			t.render(o, "", true)
			o.println()
			if len(shown) < len(entries) {
				o.println(o.paint(o.faint, fmt.Sprintf("Showing the last %d of %d entries (--limit 0 for all).", len(shown), len(entries))))
			}
			o.printf("%s %s over %s (%s)\n", o.paint(o.bold, "Total freed:"), o.sizeText(freed),
				plural(done, "cleaned item", "cleaned items"), sanitize(env.Pretty(clean.HistoryPath())))
			if trashedN > 0 {
				o.println(o.paint(o.faint, fmt.Sprintf("Moved to the Trash: %s over %s (freed only once the Trash is emptied).",
					fsx.Bytes(trashed), plural(trashedN, "item", "items"))))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 30, "entries shown, newest first (0 = all)")
	return cmd
}

// historySizes returns the SIZE and FREED cells of a history entry. FREED
// is what was really freed ("" for Trash moves and skipped items: nothing
// was freed); a command whose gain was unknown (size 0) shows "?".
func historySizes(e clean.HistoryEntry) (size, freed string) {
	unknown := e.Size == 0 && e.Method == core.MethodCommand.String()
	size = fsx.Bytes(e.Size)
	if unknown {
		size = "?"
	}
	switch {
	case e.Method == core.MethodTrash.String():
		if e.Status == clean.StatusDone.String() {
			freed = "Trash"
		}
	case e.Status == clean.StatusDone.String() || e.Status == clean.StatusFailed.String():
		freed = fsx.Bytes(max(e.Freed, 0))
		if unknown && e.Freed <= 0 {
			freed = "?"
		}
	}
	return size, freed
}

// historyWhere is the PATH/COMMAND cell of a history entry: the path, the
// command, or the location of a group item with its number of paths.
func historyWhere(env *core.Env, e clean.HistoryEntry) string {
	n := max(e.Count, len(e.Paths))
	switch {
	case e.Path != "":
		return env.Pretty(e.Path)
	case e.Command != "":
		return "$ " + e.Command
	case e.Location != "":
		if n > 1 {
			return fmt.Sprintf("%s (%d paths)", env.Pretty(e.Location), n)
		}
		return env.Pretty(e.Location)
	case len(e.Paths) > 0:
		if n > 1 {
			return fmt.Sprintf("%s (+%d more)", env.Pretty(e.Paths[0]), n-1)
		}
		return env.Pretty(e.Paths[0])
	case n > 0:
		return plural(n, "path", "paths")
	}
	return ""
}
