package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

func (c *cli) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show, create or edit the configuration file",
		Long: `The configuration lives in ~/.config/lu-cleaner/config.toml ($XDG_CONFIG_HOME,
or $LU_CLEANER_CONFIG). Every key is optional: run 'lu-cleaner config init'
for a commented sample.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.configShow()
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Fprintln(c.Stdout, config.Path())
				return nil
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Write a commented sample config (if none exists)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				p, err := config.WriteSample()
				if err != nil {
					return err
				}
				c.out.printf("Wrote %s\n", p)
				return nil
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective configuration, with resolved roots",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.configShow()
			},
		},
		&cobra.Command{
			Use:   "edit",
			Short: "Open the config in $VISUAL / $EDITOR (default: open -t)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.configEdit()
			},
		},
	)
	return cmd
}

type configView struct {
	Path          string          `json:"path"`
	Exists        bool            `json:"exists"`
	Config        map[string]any  `json:"config"`
	Roots         []string        `json:"roots"`
	RootsSource   string          `json:"roots_source"`
	WorktreeRoots []string        `json:"worktree_roots"`
	Exclude       []string        `json:"exclude"`
	Protect       []string        `json:"protect"`
	StaleAfter    string          `json:"stale_after"`
	MinSize       int64           `json:"min_size"`
	Disabled      []string        `json:"disabled_categories"`
	StateDir      string          `json:"state_dir"`
	HistoryFile   string          `json:"history_file"`
	Clean         configCleanView `json:"clean"`
}

type configCleanView struct {
	Trash bool `json:"trash"`
	Force bool `json:"force"`
}

func (c *cli) configShow() error {
	s, err := c.newSetup(nil)
	if err != nil {
		return err
	}
	v := configView{
		Path:          config.Path(),
		Exists:        fsx.Exists(config.Path()),
		Config:        map[string]any{},
		Roots:         nonNil(s.env.Roots),
		RootsSource:   s.rootsSource,
		WorktreeRoots: nonNil(s.env.WorktreeRoots),
		Exclude:       nonNil(s.env.Exclude),
		Protect:       nonNil(s.protect),
		StaleAfter:    s.cfg.StaleAfter,
		MinSize:       s.minSize,
		Disabled:      []string{},
		StateDir:      config.StateDir(),
		HistoryFile:   clean.HistoryPath(),
		Clean:         configCleanView{Trash: s.clean.Trash, Force: s.clean.Force},
	}
	// Round-trip through TOML to get the same snake_case keys as the file.
	_, _ = toml.Decode(s.cfg.Dump(), &v.Config)
	for _, d := range s.disabled {
		v.Disabled = append(v.Disabled, string(d))
	}
	if c.f.json {
		return c.writeJSON(v)
	}

	o := c.out
	state := "exists"
	if !v.Exists {
		state = "not found — defaults in use ('lu-cleaner config init' writes a sample)"
	}
	o.printf("%s %s %s\n\n", o.paint(o.title, "Config file:"), v.Path, o.paint(o.faint, "("+state+")"))
	o.println(o.paint(o.dim, "# effective values"))
	o.println(strings.TrimRight(s.cfg.Dump(), "\n"))
	o.println()
	o.println(o.paint(o.title, "Resolved"))
	list := func(label string, paths []string, note string) {
		o.printf("  %s", o.paint(o.bold, label))
		if note != "" {
			o.printf(" %s", o.paint(o.faint, "("+note+")"))
		}
		o.println()
		if len(paths) == 0 {
			o.println("    " + o.paint(o.faint, "none"))
		}
		for _, p := range paths {
			o.println("    " + s.env.Pretty(p))
		}
	}
	list("Project roots", v.Roots, v.RootsSource)
	list("Worktree roots", v.WorktreeRoots, "built-in + worktree_roots, existing only")
	list("Excluded", v.Exclude, "")
	list("Protected (in addition to the built-in list)", v.Protect, "")
	o.printf("  %s %s · %s %s · %s %v\n",
		o.paint(o.bold, "Stale after:"), v.StaleAfter,
		o.paint(o.bold, "Min size:"), fsx.Bytes(v.MinSize),
		o.paint(o.bold, "Use Trash:"), v.Clean.Trash)
	o.printf("  %s %s\n", o.paint(o.bold, "History:"), s.env.Pretty(v.HistoryFile))
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (c *cli) configEdit() error {
	p := config.Path()
	if !fsx.Exists(p) {
		if _, err := config.WriteSample(); err != nil {
			return err
		}
		c.out.printf("Created %s\n", p)
	}
	editor := c.Getenv("VISUAL")
	if editor == "" {
		editor = c.Getenv("EDITOR")
	}
	var cmd *exec.Cmd
	if argv := strings.Fields(editor); len(argv) > 0 {
		cmd = exec.Command(argv[0], append(argv[1:], p)...)
	} else {
		cmd = exec.Command("open", "-t", p)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s exited with status %d", cmd.Path, ee.ExitCode())
		}
		return err
	}
	// Validate what was saved.
	if _, err := c.LoadConfig(); err != nil {
		return fmt.Errorf("the config has errors: %w", err)
	}
	return nil
}
