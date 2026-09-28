package safety

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGuard(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	tmp := filepath.Join(t.TempDir(), "T")
	os.MkdirAll(tmp, 0o755)
	tmp, _ = filepath.EvalSymlinks(tmp)
	mk := func(rel string) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	proj := mk("local_sources/app")
	nm := mk("local_sources/app/node_modules")
	mk("local_sources/app/.git")
	mk(".claude/projects/x")
	os.WriteFile(filepath.Join(home, ".claude/.credentials.json"), []byte("{}"), 0o600)
	repoCache := mk(".cocoapods/repos/master")
	mk(".cocoapods/repos/master/.git")
	metro := filepath.Join(tmp, "metro-cache")
	os.MkdirAll(metro, 0o755)

	g := New(home, tmp, []string{filepath.Join(home, "local_sources")}, nil)

	ok := []string{nm, filepath.Join(home, ".claude/projects/x"), metro}
	for _, p := range ok {
		if err := g.Check(p, Options{}); err != nil {
			t.Errorf("Check(%s) = %v, want nil", p, err)
		}
	}
	if err := g.Check(repoCache, Options{AllowGitRepo: true}); err != nil {
		t.Errorf("allowed git repo cache blocked: %v", err)
	}

	blocked := []string{
		"/", "/Users", "/Applications", home, filepath.Join(home, "Library"),
		filepath.Join(home, "local_sources"), // a root itself
		proj,                                 // a git repository
		filepath.Join(home, ".claude"),       // denied + contains credentials
		filepath.Join(home, ".claude/.credentials.json"),
		filepath.Join(home, "LIBRARY"), // case-insensitive
		tmp,
		"relative/path",
		nm + "/../..",
		"/etc/hosts",
		repoCache, // git repo without AllowGitRepo
	}
	for _, p := range blocked {
		err := g.Check(p, Options{})
		if err == nil {
			t.Errorf("Check(%s) = nil, want blocked", p)
		}
	}
	if err := g.Check(proj, Options{}); !errors.Is(err, ErrBlocked) {
		t.Errorf("want ErrBlocked, got %v", err)
	}

	// symlink escaping home must be judged on its real location
	link := filepath.Join(home, "local_sources/app/evil")
	os.Symlink("/", link)
	if err := g.Check(filepath.Join(link, "usr"), Options{}); err == nil {
		t.Errorf("symlinked parent escaping home not blocked")
	}
	if g.Protected(nm) {
		t.Errorf("paths inside a scan root must not be reported as protected")
	}
	if !g.Protected(filepath.Join(home, "local_sources")) {
		t.Errorf("a scan root itself must be protected")
	}
	mk(".claude/projects/-Users-x-app/memory")
	mk(".claude/projects/-Users-x-app/sessions-old")
	os.WriteFile(filepath.Join(home, ".codex/state_5.sqlite"), nil, 0o644)
	os.MkdirAll(filepath.Join(home, ".codex/tmp"), 0o755)
	for _, p := range []string{
		filepath.Join(home, ".claude/projects/-Users-x-app/memory"),
		filepath.Join(home, ".claude/projects/-Users-x-app"), // contains memory/
		filepath.Join(home, ".codex/state_5.sqlite"),
	} {
		if err := g.Check(p, Options{}); err == nil {
			t.Errorf("glob-protected %s not blocked", p)
		}
	}
	if err := g.Check(filepath.Join(home, ".claude/projects/-Users-x-app/sessions-old"), Options{}); err != nil {
		t.Errorf("sibling of a protected memory dir blocked: %v", err)
	}
	if err := g.Check(filepath.Join(home, ".codex/tmp"), Options{}); err != nil {
		t.Errorf(".codex/tmp blocked: %v", err)
	}
	if err := g.Check(filepath.Join(home, ".codex/sqlite"), Options{}); err == nil {
		t.Errorf(".codex/sqlite contains the live codex-dev.db and must be blocked")
	}
	if !g.Protected(filepath.Join(home, ".claude")) {
		t.Errorf(".claude should be reported as protected (contains credentials)")
	}
}
