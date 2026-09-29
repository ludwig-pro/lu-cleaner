// Package config loads ~/.config/lu-cleaner/config.toml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the user configuration. Every field is optional.
type Config struct {
	// Roots are scanned for project artifacts (node_modules, Pods, builds...).
	// Empty = auto-detect common folders (~/local_sources, ~/dev, ~/Projects...).
	Roots []string `toml:"roots" json:"roots"`
	// WorktreeRoots are folders where tools create git worktrees. Added to the built-in list.
	WorktreeRoots []string `toml:"worktree_roots" json:"worktree_roots"`
	// Exclude: never scanned, never cleaned (prefix match).
	Exclude []string `toml:"exclude" json:"exclude"`
	// Protect: never cleaned (and nothing containing them).
	Protect []string `toml:"protect" json:"protect"`
	// MaxDepth for the artifact scan below each root (default 8).
	MaxDepth int `toml:"max_depth" json:"max_depth"`
	// MinSize hides items smaller than this in lists (default "1MB").
	MinSize string `toml:"min_size" json:"min_size"`
	// StaleAfter: items unused for longer are "stale" and preselected by smart select (default "14d").
	StaleAfter string `toml:"stale_after" json:"stale_after"`
	// DisabledCategories are skipped entirely (e.g. ["system", "containers"]).
	DisabledCategories []string `toml:"disabled_categories" json:"disabled_categories"`
	// UseTrash moves things to ~/.Trash instead of deleting (does not free space until emptied!).
	UseTrash bool `toml:"use_trash" json:"use_trash"`
	// ExtraArtifacts adds project artifact directory names, e.g. ["tmp-build"].
	ExtraArtifacts []string `toml:"extra_artifacts" json:"extra_artifacts"`
	// KeepLatest is how many of the newest versions of versioned things are
	// kept (never preselected; default 1, at least 1). It is honoured by
	// exactly: AI tool versions (Claude Code, cursor-agent, Conductor's
	// bundled agents...), Android SDK packages (NDK, build-tools, platforms,
	// CMake, sources), JetBrains IDEs and Android Studio, catalog entries that
	// keep their newest matches (Puppeteer / Cypress browsers, Kotlin/Native,
	// Skiko: it raises their own count), node versions (per version manager)
	// and iOS simulator runtimes (per platform). Gradle is not versioned this way.
	KeepLatest int `toml:"keep_latest" json:"keep_latest"`
}

// DefaultRootCandidates are probed when Roots is empty.
var DefaultRootCandidates = []string{
	"~/local_sources", "~/dev", "~/Dev", "~/Developer", "~/Projects", "~/projects", "~/code", "~/Code",
	"~/src", "~/workspace", "~/Workspace", "~/repos", "~/git", "~/github", "~/GitHub",
	"~/Documents/GitHub", "~/Documents/Projects", "~/Documents/dev", "~/Sites", "~/conductor/repos",
}

// DefaultWorktreeRoots are the well-known worktree homes of AI tools.
var DefaultWorktreeRoots = []string{
	"~/.codex/worktrees",
	"~/.cursor/worktrees",
	"~/conductor/workspaces",
	"~/.claude/worktrees",
	"~/.claude-worktrees",
	"~/.worktrees",
	"~/.superset/worktrees",
	"~/Library/Application Support/Claude/worktrees",
}

// Path returns the config file location ($XDG_CONFIG_HOME or ~/.config).
func Path() string {
	if x := os.Getenv("LU_CLEANER_CONFIG"); x != "" {
		return x
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "lu-cleaner", "config.toml")
}

// StateDir returns where history is stored (~/.local/state/lu-cleaner).
func StateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "lu-cleaner")
}

// Load reads the config file. A missing file yields defaults and no error.
func Load() (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		c.fill()
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	md, err := toml.Decode(string(data), c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys: %s", Path(), strings.Join(keys, ", "))
	}
	c.fill()
	return c, nil
}

func (c *Config) fill() {
	if c.MaxDepth <= 0 {
		c.MaxDepth = 8
	}
	if c.MinSize == "" {
		c.MinSize = "1MB"
	}
	if c.StaleAfter == "" {
		c.StaleAfter = "14d"
	}
	if c.KeepLatest <= 0 {
		c.KeepLatest = 1
	}
}

// Sample is written by `lu-cleaner config init`.
const Sample = `# lu-cleaner configuration — https://github.com/ludwig-pro/lu-cleaner
# Paths accept "~". Every key is optional.

# Folders scanned for project artifacts (node_modules, ios/Pods, android/build, .expo...).
# Empty = auto-detect (~/local_sources, ~/dev, ~/Developer, ~/Projects, ~/code, ~/conductor/repos...)
roots = []

# Extra folders where tools create git worktrees (built-in: ~/.codex/worktrees,
# ~/.cursor/worktrees, ~/conductor/workspaces, <repo>/.claude/worktrees ...).
worktree_roots = []

# Never scanned, never cleaned.
exclude = []

# Never cleaned (nor anything containing them), in addition to the built-in list
# (ssh keys, credentials, AI tool configs, keystores...).
protect = []

# Hide items smaller than this in lists.
min_size = "1MB"

# Items unused for longer are "stale" and are picked by smart select (the "a" key, or --smart).
stale_after = "14d"

# Keep the N newest versions of versioned things (never recommended by smart select; at least 1):
# AI tool versions (Claude Code, cursor-agent, Conductor's bundled agents...),
# Android SDK packages (NDK, build-tools, platforms, CMake, sources),
# JetBrains IDEs and Android Studio, catalog entries that keep their newest
# copies (Puppeteer / Cypress browsers, Kotlin/Native, Skiko), node versions
# (per version manager) and iOS simulator runtimes (per platform).
keep_latest = 1

# Skip whole categories: worktrees, artifacts, simulators, xcode, android, ai, js, ide, containers, langs, system
disabled_categories = []

# Move to ~/.Trash instead of deleting. Warning: space is only freed once the Trash is emptied.
use_trash = false

# Additional project artifact directory names to look for.
extra_artifacts = []
`

// WriteSample creates the config file if it does not exist yet.
func WriteSample() (string, error) {
	p := Path()
	if _, err := os.Stat(p); err == nil {
		return p, fmt.Errorf("%s already exists", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return p, err
	}
	return p, os.WriteFile(p, []byte(Sample), 0o644)
}

// Dump returns the effective configuration as TOML.
func (c *Config) Dump() string {
	var b bytes.Buffer
	_ = toml.NewEncoder(&b).Encode(c)
	return b.String()
}
