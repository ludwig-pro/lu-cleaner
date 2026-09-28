package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
)

type jsonEntry struct {
	ID           string        `json:"id"`
	Category     core.Category `json:"category"`
	Name         string        `json:"name"`
	Paths        []string      `json:"paths"`
	Exclude      []string      `json:"exclude,omitempty"`
	Risk         core.Risk     `json:"risk"`
	Method       core.Method   `json:"method"`
	Command      []string      `json:"command,omitempty"`
	Requires     string        `json:"requires,omitempty"`
	ProcessGuard []string      `json:"process_guard,omitempty"`
	Note         string        `json:"note,omitempty"`
	Mode         string        `json:"mode"`
	OlderThan    string        `json:"older_than,omitempty"`
	KeepLatest   int           `json:"keep_latest,omitempty"`
	AllowGitRepo bool          `json:"allow_git_repo,omitempty"`
	Recommended  bool          `json:"recommended,omitempty"`
	MinBytes     int64         `json:"min_bytes,omitempty"`
	Files        bool          `json:"files,omitempty"`
}

func toJSONEntry(e catalog.Entry) jsonEntry {
	j := jsonEntry{
		ID: e.ID, Category: e.Category, Name: e.Name, Paths: e.Paths, Exclude: e.Exclude,
		Risk: e.Risk, Method: e.Method, Command: e.Command, Requires: e.Requires,
		ProcessGuard: e.ProcessGuard, Note: e.Note, Mode: "group", KeepLatest: e.KeepLatest,
		AllowGitRepo: e.AllowGitRepo, Recommended: e.Recommended, MinBytes: e.MinBytes, Files: e.Files,
	}
	if j.Paths == nil {
		j.Paths = []string{}
	}
	if e.Mode == catalog.Each {
		j.Mode = "each"
	}
	if e.OlderThan > 0 {
		j.OlderThan = e.OlderThan.String()
	}
	return j
}

func (c *cli) catalogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "catalog",
		Short: "List the well-known paths lu-cleaner knows about (audit)",
		Long: `List the catalog of well-known caches and tool data: id, category, risk,
cleaning method and path globs ("~" = home, $TMPDIR = per-user temp dir).
Filter with --category and --kind (substring of the id).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var cats []core.Category
			for _, name := range splitList(c.f.categories) {
				cat, err := core.ParseCategory(name)
				if err != nil {
					return usageErr("--category: %v", err)
				}
				cats = append(cats, cat)
			}
			kinds := splitList(c.f.kinds)
			var entries []catalog.Entry
			for _, e := range c.Catalog() {
				if len(cats) > 0 && !hasCat(cats, e.Category) {
					continue
				}
				if len(kinds) > 0 && !containsAny(e.ID, kinds) {
					continue
				}
				entries = append(entries, e)
			}
			if c.f.json {
				out := make([]jsonEntry, 0, len(entries))
				for _, e := range entries {
					out = append(out, toJSONEntry(e))
				}
				return c.writeJSON(out)
			}
			o := c.out
			if len(entries) == 0 {
				o.println("No catalog entries.")
				return nil
			}
			t := newTable("ID", "CATEGORY", "RISK", "METHOD", "PATHS")
			t.shrink = 4
			t.maxw[0] = 36
			for _, e := range entries {
				where := strings.Join(e.Paths, ", ")
				if len(e.Command) > 0 {
					where = "$ " + strings.Join(e.Command, " ") + "  " + where
				}
				risk := e.Risk
				t.add(func(col int, v string) string {
					switch col {
					case 2:
						return o.riskPaint(risk, v)
					case 4:
						return o.paint(o.faint, v)
					}
					return v
				}, e.ID, string(e.Category), e.Risk.String(), e.Method.String(), where)
			}
			t.render(o, "", true)
			o.println(o.paint(o.faint, plural(len(entries), "entry", "entries")))
			return nil
		},
	}
}

func containsAny(s string, subs []string) bool {
	s = strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(s, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
