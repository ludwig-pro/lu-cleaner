// Package safety is the last line of defence before anything is removed.
//
// Every deletion performed by lu-cleaner goes through Guard.Check, whatever
// the provider that produced the item. The rules are deliberately strict and
// independent from the catalog so that a bad catalog entry, a bug in a
// scanner or a weird symlink can never wipe something important.
package safety

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Guard validates paths before they are deleted or moved to the Trash.
type Guard struct {
	home    string
	allowed []string        // deletions must be strictly inside one of these (lowercased)
	deny    map[string]bool // exact paths that can never be removed (lowercased)
	protect []string        // paths that must never be removed nor be inside a removed path (lowercased)
}

// ErrBlocked is wrapped by every refusal.
var ErrBlocked = errors.New("blocked by safety guard")

// New builds a Guard for home and tmpDir. roots are the user's project /
// worktree roots (they may be cleaned inside, never removed themselves);
// extra are user-configured protected paths (config "protect" + "exclude").
func New(home, tmpDir string, roots, extra []string) *Guard {
	g := &Guard{home: clean(home), deny: map[string]bool{}}
	h := g.home

	// Where deletions may happen at all.
	g.allowed = []string{h}
	if tmpDir != "" {
		t := clean(tmpDir)
		// $TMPDIR is /var/folders/xx/yyyy/T ; its parent also holds C/ (per-user caches).
		g.allowed = append(g.allowed, filepath.Dir(t))
		if r, err := filepath.EvalSymlinks(filepath.Dir(t)); err == nil {
			g.allowed = append(g.allowed, clean(r))
		}
		for _, sub := range []string{"T", "C", "0", "X"} {
			g.denyPath(filepath.Join(filepath.Dir(t), sub))
		}
		g.denyPath(t)
	}

	for _, p := range []string{
		"/", "/System", "/Library", "/Applications", "/Users", "/usr", "/bin", "/sbin",
		"/etc", "/var", "/private", "/private/var", "/private/var/folders", "/var/folders",
		"/opt", "/opt/homebrew", "/Volumes", "/cores", "/tmp", "/private/tmp", "/dev", "/Network",
	} {
		g.denyPath(p)
	}
	for _, rel := range []string{
		"", "Library", "Documents", "Desktop", "Downloads", "Pictures", "Movies", "Music",
		"Public", "Applications", "Sites", ".Trash",
		"Library/Application Support", "Library/Caches", "Library/Containers",
		"Library/Group Containers", "Library/Developer", "Library/Developer/Xcode",
		"Library/Developer/CoreSimulator", "Library/Developer/CoreSimulator/Devices",
		"Library/Logs", "Library/Preferences", "Library/Android", "Library/Android/sdk",
		"Library/Java", "Library/Fonts", "Library/LaunchAgents",
		".config", ".local", ".local/share", ".local/state", ".local/bin", ".cache",
		".claude", ".codex", ".cursor", ".gradle", ".android", ".npm", ".yarn", ".bun",
		".nvm", ".cargo", ".rustup", "go", ".vscode", ".expo", ".cocoapods", ".colima", ".docker",
		".conductor", "conductor", ".m2", ".konan",
	} {
		g.denyPath(filepath.Join(h, rel))
	}

	// Never delete these, nor anything that contains them.
	for _, rel := range []string{
		".ssh", ".gnupg", ".aws", ".azure", ".kube", ".netrc", ".npmrc", ".yarnrc", ".yarnrc.yml",
		".gitconfig", ".git-credentials", ".zshrc", ".zprofile", ".zshenv", ".bashrc", ".bash_profile",
		".profile", ".config/gh", ".config/git", ".password-store",
		".docker/config.json", ".docker/contexts",
		"Library/Keychains", "Library/Preferences", "Library/Mobile Documents", "Library/CloudStorage",
		"Library/Mail", "Library/Messages", "Library/Calendars", "Library/Safari",
		"Library/Accounts", "Library/Cookies", "Library/MobileDevice/Provisioning Profiles",
		"Library/Developer/Xcode/UserData/CodeSnippets", "Library/Developer/Xcode/UserData/KeyBindings",
		"Library/Developer/Xcode/UserData/FontAndColorThemes",
		"Library/Application Support/MobileSync",
		// Android signing & adb identity
		".android/debug.keystore", ".android/adbkey", ".android/adbkey.pub", ".gradle/gradle.properties",
		".gradle/init.d", ".gradle/init.gradle",
		// Claude Code / Claude desktop
		".claude.json", ".claude/.credentials.json", ".claude/settings.json", ".claude/settings.local.json",
		".claude/CLAUDE.md", ".claude/skills", ".claude/agents", ".claude/commands", ".claude/hooks",
		".claude/rules", ".claude/plugins/installed_plugins.json", ".claude/plugins/known_marketplaces.json",
		"Library/Application Support/Claude/claude_desktop_config.json",
		// Codex
		".codex/auth.json", ".codex/config.toml", ".codex/memories", ".codex/skills", ".codex/rules",
		".codex/AGENTS.md", ".codex/automations",
		// Cursor / VS Code user settings
		".cursor/mcp.json", ".cursor/rules", ".cursor/skills",
		"Library/Application Support/Cursor/User/settings.json",
		"Library/Application Support/Cursor/User/keybindings.json",
		"Library/Application Support/Cursor/User/snippets",
		"Library/Application Support/Code/User/settings.json",
		"Library/Application Support/Code/User/keybindings.json",
		"Library/Application Support/Code/User/snippets",
		// Expo / EAS auth
		".expo/state.json",
	} {
		g.protectPath(filepath.Join(h, rel))
	}
	for _, r := range roots {
		if r != "" {
			g.protectPath(r)
		}
	}
	for _, x := range extra {
		if x != "" {
			g.protectPath(x)
		}
	}
	return g
}

func clean(p string) string { return filepath.Clean(p) }
func key(p string) string   { return strings.ToLower(filepath.Clean(p)) }

func (g *Guard) denyPath(p string)    { g.deny[key(p)] = true }
func (g *Guard) protectPath(p string) { g.protect = append(g.protect, key(p)) }

// within reports whether p is parent or below it (both lowercased & clean).
func within(p, parent string) bool {
	if p == parent {
		return true
	}
	if parent == "/" {
		return strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, parent+"/")
}

// Options relax specific checks for trusted item kinds.
type Options struct {
	// AllowGitRepo permits removing a directory that is itself a git repository
	// (has a .git directory), e.g. ~/.cocoapods/repos/master. Linked worktrees
	// (with a .git *file*) are always allowed.
	AllowGitRepo bool
}

// Check returns nil when path may be removed, or an error wrapping ErrBlocked.
func (g *Guard) Check(path string, opt Options) error {
	if path == "" {
		return fmt.Errorf("%w: empty path", ErrBlocked)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %s is not absolute", ErrBlocked, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%w: %s is not a clean path", ErrBlocked, path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("%w: %s contains relative segments", ErrBlocked, path)
		}
	}
	if err := g.checkOne(path); err != nil {
		return err
	}
	// Resolve symlinks in the parent chain: the real location must pass too.
	if rp, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		real := filepath.Join(rp, filepath.Base(path))
		if real != path {
			if err := g.checkOne(real); err != nil {
				return fmt.Errorf("%w (resolved from %s)", err, path)
			}
		}
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() && !opt.AllowGitRepo {
		if gi, err := os.Lstat(filepath.Join(path, ".git")); err == nil && gi.IsDir() {
			return fmt.Errorf("%w: %s is a git repository (source code)", ErrBlocked, path)
		}
	}
	return nil
}

func (g *Guard) checkOne(path string) error {
	k := key(path)
	if g.deny[k] {
		return fmt.Errorf("%w: %s is a protected system or home directory", ErrBlocked, path)
	}
	inside := false
	for _, a := range g.allowed {
		if k != key(a) && within(k, key(a)) {
			inside = true
			break
		}
	}
	if !inside {
		return fmt.Errorf("%w: %s is outside the allowed areas (home, per-user temp)", ErrBlocked, path)
	}
	for _, p := range g.protect {
		if within(p, k) { // protected path is the target or lives inside it
			return fmt.Errorf("%w: %s is or contains protected path %s", ErrBlocked, path, p)
		}
	}
	return nil
}

// Protected reports whether path is (or is inside) a protected path. Used by
// scanners to avoid even proposing such items.
func (g *Guard) Protected(path string) bool {
	k := key(path)
	for _, p := range g.protect {
		if within(k, p) || within(p, k) {
			return true
		}
	}
	return g.deny[k]
}
