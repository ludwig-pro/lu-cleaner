package aitools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Claude Code maps every non-alphanumeric character to '-': ~/proj/my-app and
// ~/proj/my_app share one project directory. The sessions of the folder that
// still exists must never be proposed with those of the deleted one.
func TestClaudeOrphanNameCollision(t *testing.T) {
	f := newFixture(t)
	alive := f.dir("proj/my-app", 0)
	gone := f.path("proj/my_app") // deleted
	if claudeEncode(alive) != claudeEncode(gone) {
		t.Fatal("fixture: the two folders must collide")
	}
	p := ".claude/projects/" + claudeEncode(alive)
	f.text(p+"/aaaa.jsonl", transcript(alive), 0)
	f.file(p+"/aaaa/subagents/a.jsonl", 100, 0)
	f.text(p+"/bbbb.jsonl", transcript(gone), 0)
	f.file(p+"/bbbb/tool-results/x.txt", 100, 0)
	f.text(p+"/sessions-index.json", "{}", 0)
	f.ageTree(p+"/aaaa", 3*day)
	f.age(p+"/aaaa.jsonl", 3*day)
	f.ageTree(p+"/bbbb", 45*day)
	f.age(p+"/bbbb.jsonl", 45*day)

	r := f.scan()
	f.checkInvariants(r)
	it := r.one(t, "claude-code-orphan-project")
	if it.Path != "" {
		t.Fatalf("the whole project dir must not go while it holds sessions of an existing folder: %s", it.Path)
	}
	want := []string{f.path(p + "/bbbb"), f.path(p + "/bbbb.jsonl")}
	if strings.Join(it.Paths, ",") != strings.Join(want, ",") {
		t.Errorf("orphan targets = %v, want %v", it.Paths, want)
	}
	if it.Meta["cwd"] != gone || it.Meta["sessions"] != "1" || it.Meta["kept_sessions"] != "1" {
		t.Errorf("meta = %v", it.Meta)
	}
	if err := it.Recheck(context.Background()); err != nil {
		t.Errorf("recheck: %v", err)
	}
	// aaaa is 3 days old: not an old session either.
	if n := len(r.byKind("claude-code-old-sessions")); n != 0 {
		t.Errorf("old sessions = %v", names(r.byKind("claude-code-old-sessions")))
	}

	// Both old: the existing folder's session is only an (unselected) old session.
	f.ageTree(p+"/aaaa", 40*day)
	f.age(p+"/aaaa.jsonl", 40*day)
	r = f.scan()
	f.checkInvariants(r)
	old := r.one(t, "claude-code-old-sessions")
	if !hasTarget(old, f.path(p+"/aaaa.jsonl")) || hasTarget(old, f.path(p+"/bbbb.jsonl")) || old.Project != alive {
		t.Errorf("old sessions = %v (%s)", old.Paths, old.Project)
	}
}

// When the directory holds only sessions of the deleted folder it goes as a
// whole, but a session appearing afterwards (the name is reused) stops it.
func TestClaudeOrphanRecheckNewSession(t *testing.T) {
	f := newFixture(t)
	gone := f.path("proj/app")
	p := ".claude/projects/" + claudeEncode(gone)
	f.text(p+"/s1.jsonl", transcript(gone), 0)
	f.ageTree(p, 40*day)
	r := f.scan()
	it := r.one(t, "claude-code-orphan-project")
	if it.Path != f.path(p) {
		t.Fatalf("whole dir expected, got %v", it.Targets())
	}
	if err := it.Recheck(context.Background()); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	f.text(p+"/s2.jsonl", transcript(f.path("proj/app2")), 2*day)
	if err := it.Recheck(context.Background()); err == nil {
		t.Errorf("a new session in the directory must stop the whole-dir removal")
	}
}

func TestClaudeOrphanRecommendPolicy(t *testing.T) {
	cases := []struct {
		name string
		cwd  func(f *fixture) string
		age  float64 // days
		rec  bool
	}{
		{"deleted 2 hours ago", func(f *fixture) string { return f.path("gone/a") }, 2.0 / 24, false},
		{"deleted 10 days ago", func(f *fixture) string { return f.path("gone/a") }, 10, false},
		{"deleted 40 days ago", func(f *fixture) string { return f.path("gone/a") }, 40, true},
		{"archived Conductor workspace", func(f *fixture) string { return f.path("conductor/workspaces/repo/puebla-v4") }, 40, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			cwd := c.cwd(f)
			p := ".claude/projects/" + claudeEncode(cwd)
			f.text(p+"/s.jsonl", transcript(cwd), 0)
			f.ageTree(p, dur(c.age))
			r := f.scan()
			f.checkInvariants(r)
			it := r.one(t, "claude-code-orphan-project")
			got := core.Recommend(it, f.now, 14*day)
			if got != c.rec {
				t.Errorf("Recommend = %v, want %v (risk %v, rec %v, norec %v)", got, c.rec, it.Risk, it.Recommended, it.NoRecommend)
			}
			if !c.rec && (it.Risk != core.RiskCaution || !it.NoRecommend) {
				t.Errorf("an unsure orphan is caution with a veto: %v %v", it.Risk, it.NoRecommend)
			}
		})
	}
}

// A project whose folder is only known from the lossy directory name is never
// preselected, however old (the old code set Recommended=false, which the
// stale rule then overrode for a moderate item).
func TestClaudeOrphanFromDirNameNeverRecommended(t *testing.T) {
	f := newFixture(t)
	f.dir("work", 0)
	p := ".claude/projects/" + claudeEncode(f.path("work/deleted"))
	f.file(p+"/s1/subagents/a.jsonl", 100, 0) // no transcript: no cwd
	f.ageTree(p, 90*day)
	r := f.scan()
	f.checkInvariants(r)
	it := r.one(t, "claude-code-orphan-project")
	if it.Meta["cwd_source"] != "dir-name" || core.Recommend(it, f.now, 14*day) || it.Risk != core.RiskCaution {
		t.Errorf("dir-name orphan = %v %v %v", it.Meta, it.Risk, it.NoRecommend)
	}
	if err := it.Recheck(context.Background()); err != nil {
		t.Errorf("recheck: %v", err)
	}
	f.dir("work/deleted", 0)
	if err := it.Recheck(context.Background()); err == nil {
		t.Errorf("recheck must re-resolve the directory name")
	}
}

// A folder in a logged-out cloud provider, or below a directory we may not
// list, is unknown: never an orphan.
func TestOrphanUnknownWhenFolderUnreachable(t *testing.T) {
	f := newFixture(t)
	cloud := f.path("Library/CloudStorage/GoogleDrive-me@example.com/My Drive/app")
	p := ".claude/projects/" + claudeEncode(cloud)
	f.text(p+"/s.jsonl", transcript(cloud), 0)
	f.ageTree(p, 60*day)

	locked := f.dir("locked", 0)
	hidden := filepath.Join(locked, "app")
	q := ".claude/projects/" + claudeEncode(hidden)
	f.text(q+"/s.jsonl", transcript(hidden), 0)
	f.ageTree(q, 60*day)
	if err := os.Chmod(locked, 0o111); err != nil { // traversable, not listable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if got := pathExistence(cloud); got != existUnknown {
		t.Errorf("CloudStorage provider missing: %v, want unknown", got)
	}
	if got := pathExistence(hidden); got != existUnknown && os.Geteuid() != 0 {
		t.Errorf("unreadable parent: %v, want unknown", got)
	}
	r := f.scan()
	if n := len(r.byKind("claude-code-orphan-project")); n != 0 && os.Geteuid() != 0 {
		t.Errorf("unreachable folders must not be orphans: %v", names(r.byKind("claude-code-orphan-project")))
	}
	for _, it := range r.byKind("claude-code-old-sessions") {
		if it.Project == cloud && it.Meta["cwd_status"] != "volume not mounted" {
			t.Errorf("cloud meta = %v", it.Meta)
		}
	}
}

// dur converts a number of days to a duration.
func dur(days float64) time.Duration { return time.Duration(days * float64(day)) }

func TestFileContainsAny(t *testing.T) {
	dir := t.TempDir()
	key := "composer.composerData"
	// the key straddles the 1 MiB read boundary
	data := append(make([]byte, 1<<20-5), []byte(key)...)
	p := filepath.Join(dir, "state.vscdb")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileContainsAny(p, cursorChatKeys, 64<<20) {
		t.Errorf("key across a chunk boundary not found")
	}
	if fileContainsAny(p, []string{"aiService.prompts"}, 64<<20) {
		t.Errorf("absent key found")
	}
	if !fileContainsAny(p, []string{"nope"}, 1<<10) {
		t.Errorf("a file over the limit must count as holding the key")
	}
	if fileContainsAny(filepath.Join(dir, "missing"), cursorChatKeys, 64<<20) {
		t.Errorf("a missing file holds nothing")
	}
}

func TestMissingVolume(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"Library/CloudStorage/iCloudDrive-x/src", "Library/Mobile Documents/iCloud~md~obsidian"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, want := range map[string]bool{
		filepath.Join(home, "Library/CloudStorage/GoogleDrive-me/My Drive/app"): true,  // provider logged out
		filepath.Join(home, "Library/CloudStorage/iCloudDrive-x/src/gone"):      false, // provider there: really gone
		// iCloud Drive signed out: the container is gone, not the project
		filepath.Join(home, "Library/Mobile Documents/com~apple~CloudDocs/app"): true,
		filepath.Join(home, "Library/Mobile Documents/iCloud~md~obsidian/gone"): false,
		"/Volumes/lu-cleaner-test-not-mounted-42/src":                           true,
		"/System/Volumes/Data/Volumes/lu-cleaner-test-not-mounted-42/src":       true,
		filepath.Join(home, "src/app"):                                          false,
	} {
		if got := missingVolume(p); got != want {
			t.Errorf("missingVolume(%s) = %v, want %v", p, got, want)
		}
	}
}

// A session whose own folder cannot be read from its transcript (no "cwd",
// no sessions-index entry) is unknown: it stays, and so does the directory,
// even when the other session of the directory is a verified old orphan.
func TestClaudeOrphanKeepsSessionOfUnknownFolder(t *testing.T) {
	f := newFixture(t)
	alive := f.dir("proj/my-app", 0)
	gone := f.path("proj/my_app")
	p := ".claude/projects/" + claudeEncode(alive)
	f.text(p+"/aaaa.jsonl", `{"type":"summary","summary":"no cwd here"}`+"\n", 3*day)
	f.text(p+"/bbbb.jsonl", transcript(gone), 50*day)
	r := f.scan()
	f.checkInvariants(r)
	it := r.one(t, "claude-code-orphan-project")
	if it.Path != "" || len(it.Paths) != 1 || !hasTarget(it, f.path(p+"/bbbb.jsonl")) {
		t.Fatalf("orphan targets = %v, want only bbbb.jsonl", it.Targets())
	}
	if it.Meta["kept_sessions"] != "1" || !core.Recommend(it, f.now, 14*day) {
		t.Errorf("meta %v, recommend %v", it.Meta, core.Recommend(it, f.now, 14*day))
	}
	for _, other := range r.items {
		if hasTarget(other, f.path(p+"/aaaa.jsonl")) {
			t.Errorf("%s proposes the session of an unknown folder", other.ID)
		}
	}
}
