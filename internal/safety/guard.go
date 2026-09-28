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
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/text/unicode/norm"
)

// Guard validates paths before they are deleted or moved to the Trash.
//
// Every stored path is a key (see Key): lowercased and NFC-normalized, since
// APFS is case-insensitive and normalization-insensitive while Finder stores
// decomposed (NFD) names and config files hold composed (NFC) ones.
type Guard struct {
	home    string
	allowed []string        // deletions must be strictly inside one of these
	deny    map[string]bool // exact paths that can never be removed
	// protect are the built-in sensitive glob patterns (the home part
	// escaped): never removed, nor anything inside them, nor anything
	// containing them.
	protect []string
	// protectLit are the literal protected paths, same semantics: built-in
	// entries without a wildcard, and the user-supplied ones (config
	// "protect" and "exclude"), always literal so that a folder named
	// "[ACME] site" (or a home spelled with such characters) is not mistaken
	// for a glob.
	protectLit []string
	roots      []string // project/worktree roots: may be cleaned inside, never removed themselves nor any ancestor
}

// ErrBlocked is wrapped by every refusal.
var ErrBlocked = errors.New("blocked by safety guard")

// systemPaths can never be removed, and can never serve as an allowed area.
var systemPaths = []string{
	"/", "/System", "/Library", "/Applications", "/Users", "/usr", "/bin", "/sbin",
	"/etc", "/var", "/private", "/private/var", "/private/var/folders", "/var/folders",
	"/opt", "/opt/homebrew", "/Volumes", "/cores", "/tmp", "/private/tmp", "/dev", "/Network",
	"/private/etc", "/private/var/db", "/private/var/log", "/private/var/root", "/var/root",
}

// perUserTemp matches the resolved per-user temporary directory handed out by
// macOS (confstr _CS_DARWIN_USER_TEMP_DIR): /private/var/folders/xx/yyyy/T.
var perUserTemp = regexp.MustCompile(`^/private/var/folders/[^/]+/[^/]+/T$`)

// New builds a Guard for home and tmpDir. roots are the user's project /
// worktree roots (they may be cleaned inside, never removed themselves);
// extra are user-configured protected paths (config "protect" + "exclude").
//
// When home goes through a symlink, both the given and the resolved home are
// allowed and every home-relative rule is applied to both, since providers
// may emit either form.
func New(home, tmpDir string, roots, extra []string) *Guard {
	g := &Guard{home: clean(home), deny: map[string]bool{}}
	homes := resolvedForms(g.home)

	// Where deletions may happen at all.
	for _, h := range homes {
		if safeArea(h, 2) {
			g.allowed = append(g.allowed, Key(h))
		}
	}
	for _, t := range TempAreas(tmpDir) {
		g.allowed = append(g.allowed, Key(t))
	}
	if tmpDir != "" {
		for _, t := range resolvedForms(clean(tmpDir)) {
			g.denyPath(t)
			if perUserTemp.MatchString(resolve(t)) {
				// The siblings of T in the per-user folder are never removed whole.
				for _, sub := range []string{"T", "C", "0", "X"} {
					g.denyPath(filepath.Join(filepath.Dir(t), sub))
				}
			}
		}
	}

	for _, p := range systemPaths {
		g.denyPath(p)
	}
	for _, h := range homes {
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
			if isGlob(rel) {
				// Only rel is a pattern: the home is escaped, so that a home
				// spelled with metacharacters ("/Volumes/Data [2]/Users/x")
				// still matches.
				g.protect = append(g.protect, Key(globEscape(h)+"/"+rel))
			} else {
				g.protectLit = append(g.protectLit, Key(filepath.Join(h, rel)))
			}
		}
	}
	for _, r := range roots {
		if r != "" {
			g.roots = append(g.roots, keyForms(r, homes)...)
		}
	}
	for _, x := range extra {
		if x != "" {
			g.protectLit = append(g.protectLit, keyForms(x, homes)...)
		}
	}
	return g
}

func clean(p string) string { return filepath.Clean(p) }

// Key is the comparison form of a path on macOS: cleaned, lowercased (APFS
// is case-insensitive) and NFC-normalized (APFS is normalization-insensitive,
// Finder stores NFD names, config files hold NFC). Two spellings of the same
// file have the same key.
func Key(p string) string { return norm.NFC.String(strings.ToLower(filepath.Clean(p))) }

// normName is the comparison form of a single file name (see Key).
func normName(n string) string { return norm.NFC.String(strings.ToLower(n)) }

// Within reports whether p is parent or below it, comparing keys (case and
// Unicode normalization insensitive).
func Within(p, parent string) bool { return within(Key(p), Key(parent)) }

func (g *Guard) denyPath(p string) { g.deny[Key(p)] = true }

// resolve returns p with symlinks resolved, or p when that fails.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return clean(r)
	}
	return p
}

// resolvedForms returns p and, when different, its symlink-resolved form.
func resolvedForms(p string) []string {
	if r := resolve(p); r != p {
		return []string{p, r}
	}
	return []string{p}
}

// keyForms returns the keys of every spelling of a user path: as given, with
// symlinks resolved, and with the home prefix swapped between the given and
// the resolved home (homes[0] and homes[1]).
func keyForms(p string, homes []string) []string {
	c := clean(p)
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, v := range resolvedForms(c) {
		k := Key(v)
		add(k)
		for i, h := range homes {
			kh := Key(h)
			if !within(k, kh) {
				continue
			}
			for j, o := range homes {
				if i != j {
					add(Key(o) + k[len(kh):])
				}
			}
		}
	}
	return out
}

// components counts the non-empty elements of an absolute path.
func components(p string) int {
	n := 0
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			n++
		}
	}
	return n
}

// safeArea reports whether p may serve as an area inside which deletions are
// allowed: absolute, at least min components deep, not a system directory and
// not a volume root (/Volumes/<name>).
func safeArea(p string, min int) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || components(p) < min {
		return false
	}
	if Key(filepath.Dir(p)) == Key("/Volumes") {
		return false
	}
	for _, s := range systemPaths {
		if Key(s) == Key(p) {
			return false
		}
	}
	return true
}

// privateDir reports whether p is an existing directory that belongs to the
// effective user and that other users cannot write to (a world-writable
// directory such as /private/var/tmp is a shared area, even for root).
func privateDir(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o002 != 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// globEscape escapes the filepath.Match metacharacters of a literal path.
func globEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '*', '?', '[':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TempAreas returns the temporary areas inside which deletions are allowed
// for tmpDir ($TMPDIR), in their given and resolved spellings:
//
//   - the per-user folder /private/var/folders/xx/yyyy (parent of T, it also
//     holds C/, the per-user caches) when tmpDir resolves to its T and is
//     owned by the effective user;
//   - otherwise tmpDir itself (only what is strictly inside it), provided it
//     is owned by the effective user, is not world-writable, is at least
//     three levels deep and is not a system directory: an unset TMPDIR
//     (/tmp), a shared temp dir (/var/tmp) or a volume root never opens a
//     whole shared area.
//
// Providers that mirror the guard's allowed areas must use this function.
func TempAreas(tmpDir string) []string {
	if tmpDir == "" || !filepath.IsAbs(tmpDir) {
		return nil
	}
	t := clean(tmpDir)
	rt := resolve(t)
	if !privateDir(rt) {
		return nil
	}
	var out []string
	if perUserTemp.MatchString(rt) {
		out = append(out, filepath.Dir(rt))
		if d := filepath.Dir(t); d != filepath.Dir(rt) && resolve(d) == filepath.Dir(rt) {
			out = append(out, d)
		}
		return out
	}
	if !safeArea(rt, 3) {
		return nil
	}
	out = append(out, rt)
	if t != rt {
		out = append(out, t)
	}
	return out
}

// isGlob reports whether a protected entry is a glob pattern.
func isGlob(p string) bool { return strings.ContainsAny(p, "*?[") }

// hitsProtected reports whether removing k (a key; orig is the same path with
// its real spelling) would remove the protected entry p: k is p, is inside p,
// or contains p. A glob entry matches k or any ancestor of k, and blocks k
// when an existing path inside k matches it.
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

// existsMatch reports whether dir contains a path matching the (key form)
// glob components rest, comparing names case- and normalization-insensitively.
func existsMatch(dir string, rest []string) bool {
	if len(rest) == 0 {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if ok, _ := filepath.Match(rest[0], normName(e.Name())); ok {
			if len(rest) == 1 || (e.IsDir() && existsMatch(filepath.Join(dir, e.Name()), rest[1:])) {
				return true
			}
		}
	}
	return false
}

// within reports whether p is parent or below it (both keys).
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
	// AllowGitRepo permits removing a directory that is itself a git
	// repository or worktree (see RepoKind), e.g. ~/.cocoapods/repos/master,
	// a trashed project, or a linked worktree removed through git.
	AllowGitRepo bool
}

// RepoKind reports why dir is a git repository or worktree, or "" when it is
// not one: it holds a .git entry of any type (a directory for a clone, a file
// for a linked worktree, a submodule checkout, a bare-repo layout or a
// --separate-git-dir, a symlink) or it is itself a bare repository (HEAD file
// plus objects/ and refs/ directories). Unreadable entries count as present:
// only a clean ENOENT proves absence.
func RepoKind(dir string) string {
	gi, err := os.Lstat(filepath.Join(dir, ".git"))
	switch {
	case err == nil && gi.IsDir():
		return ".git directory"
	case err == nil && gi.Mode()&os.ModeSymlink != 0:
		return ".git symlink"
	case err == nil:
		return ".git file: linked worktree, submodule or separate git dir"
	case !errors.Is(err, os.ErrNotExist):
		return ".git entry unreadable: " + err.Error()
	}
	if isBareRepo(dir) {
		return "bare repository"
	}
	return ""
}

// isBareRepo reports a bare repository layout: HEAD file, objects/ and refs/.
func isBareRepo(dir string) bool {
	h, err := os.Lstat(filepath.Join(dir, "HEAD"))
	if err != nil || !h.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Lstat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// ErrTooLarge is returned by FindNestedRepo when the tree holds more entries
// than it is allowed to inspect.
var ErrTooLarge = errors.New("too many entries to inspect")

// FindNestedRepo looks for a git repository or worktree strictly inside dir
// (dir's own .git is ignored), at most maxDepth levels below it, without
// following symlinks and without entering .git directories. It returns the
// first one found ("" if none). skip, when non-nil, prunes directories (it
// receives the path and the name). The walk visits at most maxEntries entries
// (0 = 1,000,000) and fails with ErrTooLarge beyond that; an unreadable
// directory is also an error: callers must fail closed.
func FindNestedRepo(dir string, maxDepth, maxEntries int, skip func(path, name string) bool) (string, error) {
	if maxEntries <= 0 {
		maxEntries = 1_000_000
	}
	seen := 0
	var walk func(d string, depth int) (string, error)
	walk = func(d string, depth int) (string, error) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return "", err
		}
		seen += len(entries)
		if seen > maxEntries {
			return "", fmt.Errorf("%s: %w", dir, ErrTooLarge)
		}
		if depth > 0 {
			var head, objects, refs bool
			for _, e := range entries {
				switch e.Name() {
				case ".git":
					return d, nil // any type: directory, file or symlink
				case "HEAD":
					head = e.Type().IsRegular()
				case "objects":
					objects = e.IsDir()
				case "refs":
					refs = e.IsDir()
				}
			}
			if head && objects && refs {
				return d, nil
			}
		}
		if depth >= maxDepth {
			return "", nil
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name() == ".git" { // IsDir is false for symlinks
				continue
			}
			p := filepath.Join(d, e.Name())
			if skip != nil && skip(p, e.Name()) {
				continue
			}
			if found, err := walk(p, depth+1); found != "" || err != nil {
				return found, err
			}
		}
		return "", nil
	}
	return walk(dir, 0)
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
		if why := RepoKind(path); why != "" {
			return fmt.Errorf("%w: %s is a git repository (%s)", ErrBlocked, path, why)
		}
	}
	return nil
}

func (g *Guard) checkOne(path string) error {
	k := Key(path)
	if g.deny[k] {
		return fmt.Errorf("%w: %s is a protected system or home directory", ErrBlocked, path)
	}
	inside := false
	for _, a := range g.allowed {
		if k != a && within(k, a) {
			inside = true
			break
		}
	}
	if !inside {
		return fmt.Errorf("%w: %s is outside the allowed areas (home, per-user temp)", ErrBlocked, path)
	}
	if p := g.protectedBy(k, path); p != "" {
		return fmt.Errorf("%w: %s is, contains or is inside protected path %s", ErrBlocked, path, p)
	}
	for _, r := range g.roots {
		if within(r, k) { // a scan root is the target or lives inside it
			return fmt.Errorf("%w: %s is or contains the scan root %s", ErrBlocked, path, r)
		}
	}
	return nil
}

// protectedBy returns the protected entry that removing k (orig: the same
// path as given) would hit, or "".
func (g *Guard) protectedBy(k, orig string) string {
	for _, p := range g.protect {
		if hitsProtected(k, orig, p) { // protected path is the target, contains it, or lives inside it
			return p
		}
	}
	for _, p := range g.protectLit {
		if within(p, k) || within(k, p) {
			return p
		}
	}
	return ""
}

// Protected reports whether path must never be proposed for deletion: it is
// (or is inside, or contains) a sensitive path, it is (or contains) a scan
// root, or it is a denied system/home directory. Paths *inside* scan roots are
// not protected. Used by scanners to avoid even proposing such items.
func (g *Guard) Protected(path string) bool {
	k := Key(path)
	if g.protectedBy(k, path) != "" {
		return true
	}
	for _, r := range g.roots {
		if within(r, k) {
			return true
		}
	}
	return g.deny[k]
}
