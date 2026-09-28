package safety

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	mkdirs(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Any .git entry (file, symlink) and bare repositories are repositories, not
// only a .git directory (finding guard-gitfile-bypass).
func TestGuardGitEntriesOfAnyKind(t *testing.T) {
	home := realTemp(t)
	g := New(home, "", nil, nil)

	bareLayout := filepath.Join(home, "dev/myrepo") // myrepo/.bare + .git file
	mkdirs(t, filepath.Join(bareLayout, ".bare/objects"), filepath.Join(bareLayout, ".bare/refs"))
	write(t, filepath.Join(bareLayout, ".bare/HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(bareLayout, ".git"), "gitdir: ./.bare\n")

	submodule := filepath.Join(home, "dev/app/sub")
	write(t, filepath.Join(submodule, ".git"), "gitdir: ../.git/modules/sub\n")

	linked := filepath.Join(home, ".codex/worktrees/ab12/app")
	write(t, filepath.Join(linked, ".git"), "gitdir: /x/app/.git/worktrees/app\n")

	symlinked := filepath.Join(home, "dev/linked-git")
	mkdirs(t, symlinked, filepath.Join(home, "elsewhere/.git"))
	if err := os.Symlink(filepath.Join(home, "elsewhere/.git"), filepath.Join(symlinked, ".git")); err != nil {
		t.Fatal(err)
	}

	bare := filepath.Join(home, "dev/server.git")
	mkdirs(t, filepath.Join(bare, "objects"), filepath.Join(bare, "refs"))
	write(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n")

	dotgit := filepath.Join(home, "dev/clone/.git") // deleting a .git dir itself
	mkdirs(t, filepath.Join(dotgit, "objects"), filepath.Join(dotgit, "refs"))
	write(t, filepath.Join(dotgit, "HEAD"), "ref: refs/heads/main\n")

	for _, p := range []string{bareLayout, submodule, linked, symlinked, bare, dotgit} {
		err := g.Check(p, Options{})
		if err == nil || !strings.Contains(err.Error(), "git repository") {
			t.Errorf("Check(%s) = %v, want blocked as a git repository", p, err)
		}
		if err := g.Check(p, Options{AllowGitRepo: true}); err != nil {
			t.Errorf("Check(%s, AllowGitRepo) = %v, want nil", p, err)
		}
	}
	// Not repositories: a HEAD file alone, a plain folder.
	plain := filepath.Join(home, "dev/notes")
	write(t, filepath.Join(plain, "HEAD"), "x")
	if err := g.Check(plain, Options{}); err != nil {
		t.Errorf("plain folder blocked: %v", err)
	}
}

// With TMPDIR unset (sudo, cron, launchd) the guard must not open /private
// (finding guard-tmpdir-parent-private).
func TestGuardTmpdirParentNotOpened(t *testing.T) {
	home := realTemp(t)
	for _, tmp := range []string{"/tmp", "/private/tmp", "/var/tmp", "/Volumes/Scratch"} {
		g := New(home, tmp, nil, nil)
		for _, p := range []string{"/private/etc/hosts", "/private/etc/sudoers", "/private/var/db/dslocal",
			"/private/var/log/system.log", "/private/tmp/some-file", "/tmp/some-file"} {
			if err := g.Check(p, Options{}); err == nil {
				t.Errorf("TMPDIR=%s: Check(%s) = nil, want blocked", tmp, p)
			}
		}
		if len(TempAreas(tmp)) != 0 {
			t.Errorf("TempAreas(%s) = %v, want none", tmp, TempAreas(tmp))
		}
	}
	// A private TMPDIR only allows what is strictly inside it.
	tmp := filepath.Join(realTemp(t), "T")
	mkdirs(t, filepath.Join(tmp, "metro-cache"), filepath.Join(filepath.Dir(tmp), "C/x"))
	g := New(home, tmp, nil, nil)
	if err := g.Check(filepath.Join(tmp, "metro-cache"), Options{}); err != nil {
		t.Errorf("inside TMPDIR blocked: %v", err)
	}
	if err := g.Check(filepath.Join(filepath.Dir(tmp), "C/x"), Options{}); err == nil {
		t.Errorf("sibling of a non-standard TMPDIR allowed")
	}
	// The real per-user temp dir allows its parent (it also holds C/).
	if real := os.Getenv("TMPDIR"); real != "" {
		if r, err := filepath.EvalSymlinks(real); err == nil && perUserTemp.MatchString(r) {
			areas := TempAreas(real)
			found := false
			for _, a := range areas {
				found = found || a == filepath.Dir(r)
			}
			if !found {
				t.Errorf("TempAreas(%s) = %v, want %s", real, areas, filepath.Dir(r))
			}
		}
	}
}

// A home that is "/" or a system directory never becomes an allowed area.
func TestGuardRefusesDegenerateHome(t *testing.T) {
	for _, h := range []string{"/", "/Users", "/private/var", ""} {
		g := New(h, "", nil, nil)
		if err := g.Check("/usr/local/share", Options{}); err == nil {
			t.Errorf("home %q: /usr/local/share allowed", h)
		}
	}
}

// User protect entries are literal: '[' is not a glob (finding
// guard-protect-glob-metachars).
func TestGuardUserProtectIsLiteral(t *testing.T) {
	home := realTemp(t)
	dir := filepath.Join(home, "Clients/[ACME] site")
	mkdirs(t, filepath.Join(dir, "node_modules"))
	g := New(home, "", nil, []string{dir})
	for _, p := range []string{dir, filepath.Join(dir, "node_modules"), filepath.Join(home, "Clients")} {
		if err := g.Check(p, Options{}); err == nil {
			t.Errorf("Check(%s) = nil, want blocked (protected)", p)
		}
	}
	if !g.Protected(filepath.Join(dir, "node_modules")) {
		t.Errorf("Protected(node_modules inside [ACME] site) = false")
	}
	// A sibling is not protected.
	other := filepath.Join(home, "Clients/A site/node_modules")
	mkdirs(t, other)
	if err := g.Check(other, Options{}); err != nil {
		t.Errorf("sibling blocked: %v", err)
	}
}

// NFC config entries protect NFD folders created by Finder, and case does not
// matter (finding guard-unicode-normalization).
func TestGuardUnicodeNormalization(t *testing.T) {
	home := realTemp(t)
	nfd := filepath.Join(home, "Projets", norm.NFD.String("Élan"))
	mkdirs(t, filepath.Join(nfd, "node_modules"))
	if norm.NFD.String("Élan") == norm.NFC.String("Élan") {
		t.Fatal("test strings are not different")
	}
	nfc := filepath.Join(home, "Projets", norm.NFC.String("Élan"))
	for _, protect := range []string{nfc, strings.ToUpper(nfc[:len(home)]) + nfc[len(home):]} {
		g := New(home, "", nil, []string{protect})
		for _, p := range []string{nfd, filepath.Join(nfd, "node_modules")} {
			if err := g.Check(p, Options{}); err == nil {
				t.Errorf("protect %q: Check(%q) = nil, want blocked", protect, p)
			}
			if !g.Protected(p) {
				t.Errorf("protect %q: Protected(%q) = false", protect, p)
			}
		}
	}
	// Roots too: an NFD root is not removable through its NFC spelling.
	g := New(home, "", []string{nfd}, nil)
	if err := g.Check(nfc, Options{}); err == nil {
		t.Errorf("root removable through another normalization")
	}
	// Built-in glob entries match NFD/uppercase names as well.
	mem := filepath.Join(home, ".claude/projects", norm.NFD.String("-Users-é"), "MEMORY")
	mkdirs(t, mem)
	g = New(home, "", nil, nil)
	if err := g.Check(filepath.Dir(mem), Options{}); err == nil {
		t.Errorf("project dir holding memory/ (other case) not blocked")
	}
}

// A symlinked $HOME: paths in both spellings are allowed, and the protected
// paths are protected in both spellings (finding symlinked-home-blocks-everything).
func TestGuardSymlinkedHome(t *testing.T) {
	base := realTemp(t)
	real := filepath.Join(base, "home")
	link := filepath.Join(base, "homelink")
	mkdirs(t, filepath.Join(real, ".npm/_cacache"), filepath.Join(real, ".ssh/keys"), filepath.Join(real, "src/app/node_modules"),
		filepath.Join(real, "Secret/stuff"))
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	g := New(link, "", []string{filepath.Join(link, "src")}, []string{filepath.Join(link, "Secret")})
	for _, h := range []string{real, link} {
		if err := g.Check(filepath.Join(h, ".npm/_cacache"), Options{}); err != nil {
			t.Errorf("Check(%s/.npm/_cacache) = %v, want nil", h, err)
		}
		if err := g.Check(filepath.Join(h, "src/app/node_modules"), Options{}); err != nil {
			t.Errorf("Check(%s/src/app/node_modules) = %v, want nil", h, err)
		}
		for _, rel := range []string{".ssh/keys", ".ssh", "src", "Secret/stuff", "Library", ".npm"} {
			if err := g.Check(filepath.Join(h, rel), Options{}); err == nil && rel != "Library" {
				t.Errorf("Check(%s/%s) = nil, want blocked", h, rel)
			}
		}
		if !g.Protected(filepath.Join(h, "Secret/stuff")) || !g.Protected(filepath.Join(h, ".ssh/keys")) {
			t.Errorf("protected paths not reported under %s", h)
		}
		if err := g.Check(h, Options{}); err == nil {
			t.Errorf("home %s itself allowed", h)
		}
	}
}

func TestFindNestedRepo(t *testing.T) {
	root := realTemp(t)
	write(t, filepath.Join(root, ".git"), "gitdir: /x/.git/worktrees/a\n") // its own .git is ignored
	mkdirs(t, filepath.Join(root, "src/deep"))
	if got, err := FindNestedRepo(root, 10, 0, nil); got != "" || err != nil {
		t.Fatalf("clean tree: %q, %v", got, err)
	}
	nested := filepath.Join(root, ".claude/worktrees/b")
	write(t, filepath.Join(nested, ".git"), "gitdir: /x/.git/worktrees/b\n")
	if got, _ := FindNestedRepo(root, 10, 0, nil); got != nested {
		t.Fatalf("nested worktree: got %q, want %q", got, nested)
	}
	if got, _ := FindNestedRepo(root, 10, 0, func(p, name string) bool { return p == nested }); got != "" {
		t.Fatalf("skipped dir reported: %q", got)
	}
	if got, _ := FindNestedRepo(root, 1, 0, nil); got != "" {
		t.Fatalf("depth limit not honoured: %q", got)
	}
	bare := filepath.Join(root, "vendor/server.git")
	mkdirs(t, filepath.Join(bare, "objects"), filepath.Join(bare, "refs"))
	write(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n")
	if got, _ := FindNestedRepo(root, 10, 0, func(p, name string) bool { return p == nested }); got != bare {
		t.Fatalf("bare repo: got %q", got)
	}
	if _, err := FindNestedRepo(root, 10, 3, nil); err == nil {
		t.Fatalf("entry limit not enforced")
	}
}

// A home spelled with glob metacharacters keeps its built-in protections:
// only the relative part of a built-in entry is a pattern (reverification of
// guard-protect-glob-metachars).
func TestGuardHomeWithGlobMetachars(t *testing.T) {
	home := filepath.Join(realTemp(t), "Data [2]", "bob?")
	mkdirs(t, filepath.Join(home, ".ssh/keys"), filepath.Join(home, ".claude/projects/p/memory"),
		filepath.Join(home, ".codex"), filepath.Join(home, ".cache/x"))
	write(t, filepath.Join(home, ".codex/state_5.sqlite"), "db")
	g := New(home, "", nil, nil)
	for _, p := range []string{
		filepath.Join(home, ".ssh"), filepath.Join(home, ".ssh/keys"),
		filepath.Join(home, ".claude/projects/p/memory"), filepath.Join(home, ".claude/projects/p"),
		filepath.Join(home, ".codex/state_5.sqlite"),
	} {
		if err := g.Check(p, Options{}); err == nil {
			t.Errorf("Check(%s) = nil, want blocked", p)
		}
		if !g.Protected(p) {
			t.Errorf("Protected(%s) = false", p)
		}
	}
	if err := g.Check(filepath.Join(home, ".cache/x"), Options{}); err != nil {
		t.Errorf("ordinary cache blocked: %v", err)
	}
}

// A world-writable TMPDIR (shared, e.g. /var/tmp for root) is never an
// allowed area, even when owned by the effective user.
func TestGuardSharedTmpdirNotOpened(t *testing.T) {
	home := realTemp(t)
	tmp := filepath.Join(realTemp(t), "shared")
	mkdirs(t, filepath.Join(tmp, "someone-else"))
	if err := os.Chmod(tmp, 0o1777); err != nil {
		t.Fatal(err)
	}
	if a := TempAreas(tmp); len(a) != 0 {
		t.Errorf("TempAreas(world-writable) = %v, want none", a)
	}
	if err := New(home, tmp, nil, nil).Check(filepath.Join(tmp, "someone-else"), Options{}); err == nil {
		t.Errorf("inside a world-writable TMPDIR allowed")
	}
}
