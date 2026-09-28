package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
)

// genDocsCmd writes the generated reference pages of the documentation site
// (site/src/content/docs/reference/…): one page per command and the catalog.
// Hidden: it is a maintainer tool (`make docs`).
func (c *cli) genDocsCmd(root *cobra.Command) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:    "gen-docs",
		Short:  "Generate the reference pages of the documentation site",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmdDir := filepath.Join(out, "commands")
			if err := os.MkdirAll(cmdDir, 0o755); err != nil {
				return err
			}
			order := 0
			for _, sub := range docCommands(root) {
				order++
				name := strings.TrimPrefix(strings.ReplaceAll(sub.CommandPath(), " ", "-"), "lu-cleaner-")
				if sub == root {
					name = "lu-cleaner"
				}
				if err := os.WriteFile(filepath.Join(cmdDir, name+".md"), []byte(commandPage(sub, order)), 0o644); err != nil {
					return err
				}
			}
			if err := os.WriteFile(filepath.Join(out, "catalog.md"), []byte(catalogPage(c.Catalog())), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(c.Stdout, "wrote %d command pages and the catalog to %s\n", order, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "site/src/content/docs/reference", "output directory")
	return cmd
}

// docCommands returns the root and every visible command, depth first.
func docCommands(root *cobra.Command) []*cobra.Command {
	out := []*cobra.Command{root}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		subs := c.Commands()
		sort.SliceStable(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
		for _, s := range subs {
			if s.Hidden || !s.IsAvailableCommand() || s.Name() == "help" {
				continue
			}
			out = append(out, s)
			walk(s)
		}
	}
	walk(root)
	return out
}

func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func commandPage(cmd *cobra.Command, order int) string {
	var b strings.Builder
	title := cmd.CommandPath()
	fmt.Fprintf(&b, "---\ntitle: %s\ndescription: %s\nsidebar:\n  order: %d\n  label: %s\n---\n\n",
		yamlString(title), yamlString(cmd.Short), order, yamlString(strings.TrimPrefix(title, "lu-cleaner ")))
	b.WriteString(":::note[Generated]\nThis page is generated from the CLI itself (`make docs`). Do not edit it by hand.\n:::\n\n")
	if cmd.Long != "" {
		b.WriteString(cmd.Long + "\n\n")
	} else {
		b.WriteString(cmd.Short + ".\n\n")
	}
	b.WriteString("## Usage\n\n```bash\n" + cmd.UseLine() + "\n```\n\n")
	if len(cmd.Aliases) > 0 {
		b.WriteString("**Aliases:** `" + strings.Join(cmd.Aliases, "`, `") + "`\n\n")
	}
	if cmd.Example != "" {
		b.WriteString("## Examples\n\n```bash\n" + strings.TrimRight(dedent(cmd.Example), "\n") + "\n```\n\n")
	}
	if subs := visibleSubs(cmd); len(subs) > 0 && cmd.HasParent() {
		b.WriteString("## Subcommands\n\n| Command | Description |\n|---|---|\n")
		for _, s := range subs {
			fmt.Fprintf(&b, "| `%s` | %s |\n", s.CommandPath(), mdCell(s.Short))
		}
		b.WriteString("\n")
	}
	if t := flagTable(cmd.NonInheritedFlags()); t != "" {
		b.WriteString("## Options\n\n" + t + "\n")
	}
	if t := flagTable(cmd.InheritedFlags()); t != "" {
		b.WriteString("## Global options\n\n" + t + "\n")
	}
	return b.String()
}

func visibleSubs(cmd *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, s := range cmd.Commands() {
		if !s.Hidden && s.IsAvailableCommand() && s.Name() != "help" {
			out = append(out, s)
		}
	}
	return out
}

func flagTable(fs *pflag.FlagSet) string {
	var rows []string
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		name := "`--" + f.Name + "`"
		if f.Shorthand != "" {
			name = "`-" + f.Shorthand + "`, " + name
		}
		typ := f.Value.Type()
		if typ == "bool" {
			typ = ""
		}
		def := f.DefValue
		switch def {
		case "", "false", "[]", "0":
			def = ""
		default:
			def = "`" + def + "`"
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s |", name, typ, def, mdCell(f.Usage)))
	})
	if len(rows) == 0 {
		return ""
	}
	return "| Flag | Type | Default | Description |\n|---|---|---|---|\n" + strings.Join(rows, "\n") + "\n"
}

func mdCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}

func dedent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "  ")
	}
	return strings.Join(lines, "\n")
}

func catalogPage(entries []catalog.Entry) string {
	var b strings.Builder
	b.WriteString("---\ntitle: \"Catalog of known locations\"\ndescription: \"Every well-known cache, log and tool-data path lu-cleaner checks, with its risk and cleaning method.\"\nsidebar:\n  order: 1\n---\n\n")
	b.WriteString(":::note[Generated]\nThis page is generated from the catalog in the source code (`make docs`). ")
	b.WriteString("Run `lu-cleaner catalog` to print it on your machine. `~` is your home folder and `$TMPDIR` your per-user temporary folder.\n:::\n\n")
	b.WriteString("These are the **static** locations. Things that need logic — git worktrees, project artifacts, simulators, emulators, node versions, AI sessions by age… — are found by dedicated scanners, described in [What gets scanned](/lu-cleaner/reference/scanners/).\n\n")
	fmt.Fprintf(&b, "**%d entries.** Risk: <span class=\"risk safe\">safe</span> pure cache · <span class=\"risk moderate\">moderate</span> regenerable at some cost · <span class=\"risk caution\">caution</span> may hold data you care about · <span class=\"risk never\">never</span> report only.\n\n", len(entries))
	byCat := map[core.Category][]catalog.Entry{}
	for _, e := range entries {
		byCat[e.Category] = append(byCat[e.Category], e)
	}
	for _, ci := range core.Categories {
		es := byCat[ci.ID]
		if len(es) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s %s\n\n%s — %d entries.\n\n", ci.Icon, ci.Title, ci.Desc, len(es))
		for _, e := range es {
			how := e.Method.String()
			if e.Method == core.MethodCommand {
				how = "runs `" + strings.Join(e.Command, " ") + "`"
			}
			fmt.Fprintf(&b, "#### %s\n\n<span class=\"risk %s\">%s</span> · %s · <code>%s</code>\n\n", e.Name, e.Risk, e.Risk, how, e.ID)
			for _, p := range e.Paths {
				fmt.Fprintf(&b, "- `%s`\n", p)
			}
			b.WriteString("\n" + e.Note)
			var extra []string
			if len(e.ProcessGuard) > 0 {
				extra = append(extra, "refused while "+strings.Join(e.ProcessGuard, ", ")+" runs")
			}
			if e.OlderThan > 0 {
				extra = append(extra, fmt.Sprintf("only items older than %d days", int(e.OlderThan.Hours()/24)))
			}
			if e.KeepLatest > 0 {
				extra = append(extra, fmt.Sprintf("keeps the %d newest", e.KeepLatest))
			}
			if len(extra) > 0 {
				b.WriteString(" _(" + strings.Join(extra, "; ") + ")_")
			}
			b.WriteString("\n\n")
		}
	}
	return b.String()
}
