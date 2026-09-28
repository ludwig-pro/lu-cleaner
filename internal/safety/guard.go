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
	protect []string        // sensitive paths: never removed, nor anything inside them, nor anything containing them (lowercased)
	roots   []string        // project/worktree roots: may be cleaned inside, never removed themselves nor any ancestor (lowercased)
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
		".claude/history.jsonl", ".claude/sessions", ".claude/projects/*/memory",
		".codex/history.jsonl", ".codex/session_index.jsonl", ".codex/.codex-global-state.json",
		".codex/.codex-global-state.json.bak", ".codex/state_*.sqlite*", ".codex/goals_*.sqlite",
		".codex/memories_*.sqlite", ".codex/sqlite/codex-dev.db",
		"Library/Application Support/Cursor/User/globalStorage/state.vscdb",
		"Library/Application Support/Cursor/User/globalStorage/storage.json",
		"Library/Application Support/Code/User/globalStorage/state.vscdb",
		"Library/Application Support/Code/User/globalStorage/storage.json",
		"Library/Application Support/Claude/config.json",
		"Library/Application Support/Claude/git-worktrees.json",
		".ollama/id_ed25519", ".ollama/id_ed25519.pub",
		".gemini/oauth_creds.json", ".gemini/settings.json", ".gemini/GEMINI.md",
		".multica", ".conductor/settings.toml",
		// Expo / EAS auth
		".expo/state.json",
	} {
		g.protectPath(filepath.Join(h, rel))
	}
	for _, r := range roots {
		if r != "" {
			g.roots = append(g.roots, key(r))
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

// isGlob reports whether a protected entry is a glob pattern.
func isGlob(p string) bool { return strings.ContainsAny(p, "*?[") }

// hitsProtected reports whether removing k (lowercased, clean; orig is the
// same path with its real case) would remove the protected entry p: k is p,
// is inside p, or contains p. A glob entry matches k or any ancestor of k,
// and blocks k when an existing path inside k matches it.
func hitsProtected(k, orig, p string) bool {
	if !isGlob(p) {
		return within(p, k) || within(k, p)
	}
	for q := k; q != "/" && q != "."; q = filepath.Dir(q) {
		if ok, _ := filepath.Match(p, q); ok {
			return true
		}
	}
	kParts := strings.Split(strings.TrimPrefix(k, "/"), "/")
	pParts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(pParts) <= len(kParts) {
		return false
	}
	for i, kp := range kParts {
		if ok, _ := filepath.Match(pParts[i], kp); !ok {
			return false
		}
	}
	return existsMatch(orig, pParts[len(kParts):])
}

// existsMatch reports whether dir contains a path matching the (lowercased)
// glob components rest, comparing names case-insensitively.
func existsMatch(dir string, rest []string) bool {
	if len(rest) == 0 {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if ok, _ := filepath.Match(rest[0], strings.ToLower(e.Name())); ok {
			if len(rest) == 1 || (e.IsDir() && existsMatch(filepath.Join(dir, e.Name()), rest[1:])) {
				return true
			}
		}
	}
	return false
}

// staticPrefix is the longest leading directory of a glob without metacharacters.
func staticPrefix(p string) string {
	for isGlob(p) {
		p = filepath.Dir(p)
	}
	return p
}

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
		if hitsProtected(k, path, p) { // protected path is the target, contains it, or lives inside it
			return fmt.Errorf("%w: %s is, contains or is inside protected path %s", ErrBlocked, path, p)
		}
	}
	for _, r := range g.roots {
		if within(r, k) { // a scan root is the target or lives inside it
			return fmt.Errorf("%w: %s is or contains the scan root %s", ErrBlocked, path, r)
		}
	}
	return nil
}

// Protected reports whether path must never be proposed for deletion: it is
// (or is inside, or contains) a sensitive path, it is (or contains) a scan
// root, or it is a denied system/home directory. Paths *inside* scan roots are
// not protected. Used by scanners to avoid even proposing such items.
func (g *Guard) Protected(path string) bool {
	k := key(path)
	for _, p := range g.protect {
		if hitsProtected(k, path, p) {
			return true
		}
	}
	for _, r := range g.roots {
		if within(r, k) {
			return true
		}
	}
	return g.deny[k]
}
