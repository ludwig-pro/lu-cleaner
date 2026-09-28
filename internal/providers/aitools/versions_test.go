package aitools

import (
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

func TestVersionSets(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(f *fixture)
		kind    string
		stale   []string // version names expected as items
		running string   // a ps line
	}{
		{
			name: "claude CLI: keep link target and newest pre-download",
			setup: func(f *fixture) {
				f.file(".local/share/claude/versions/2.1.278", 1000, 20*day)
				f.file(".local/share/claude/versions/2.1.280", 1000, 10*day)
				f.file(".local/share/claude/versions/2.1.283", 1000, 1*day)
				f.link(".local/bin/claude", f.path(".local/share/claude/versions/2.1.280"))
			},
			kind:  "claude-code-old-version",
			stale: []string{"2.1.278"},
		},
		{
			name: "claude CLI: a running old version is kept",
			setup: func(f *fixture) {
				f.file(".local/share/claude/versions/2.1.278", 1000, 20*day)
				f.file(".local/share/claude/versions/2.1.283", 1000, 1*day)
				f.link(".local/bin/claude", "../share/claude/versions/2.1.283") // relative link
			},
			running: "/share/claude/versions/2.1.278",
			kind:    "claude-code-old-version",
		},
		{
			name: "claude desktop: highest version wins over mtime",
			setup: func(f *fixture) {
				f.file("Library/Application Support/Claude/claude-code/2.1.9/claude.app/x", 1000, 1*day)
				f.file("Library/Application Support/Claude/claude-code/2.1.10/claude.app/x", 1000, 5*day)
			},
			kind:  "claude-desktop-old-claude-code",
			stale: []string{"2.1.9"},
		},
		{
			name: "claude desktop VM: pinned .sdk-version is kept",
			setup: func(f *fixture) {
				f.file("Library/Application Support/Claude/claude-code-vm/2.1.1/claude", 1000, 9*day)
				f.file("Library/Application Support/Claude/claude-code-vm/2.1.2/claude", 1000, 5*day)
				f.file("Library/Application Support/Claude/claude-code-vm/2.1.3/claude", 1000, 1*day)
				f.text("Library/Application Support/Claude/claude-code-vm/.sdk-version", "2.1.1\n", 0)
			},
			kind:  "claude-desktop-old-claude-code-vm",
			stale: []string{"2.1.2"},
		},
		{
			name: "cursor bundled agent: link target kept, tmp leftovers proposed",
			setup: func(f *fixture) {
				base := "Library/Application Support/Cursor/User/globalStorage/anysphere.cursor-agent-worker/agent-cli/.local/"
				f.file(base+"share/cursor-agent/versions/2026.08.11-e8db854/index.js", 1000, 40*day)
				f.file(base+"share/cursor-agent/versions/2026.09.18-9a7762b/index.js", 1000, 8*day)
				f.file(base+"share/cursor-agent/versions/2026.09.23-86fc751/index.js", 1000, 3*day)
				f.file(base+"share/cursor-agent/versions/.tmp-2026.09.23-1/partial", 1000, 3*day)
				f.ageTree(base+"share/cursor-agent/versions/.tmp-2026.09.23-1", 3*day)
				f.link(base+"bin/cursor-agent", f.path(base+"share/cursor-agent/versions/2026.09.18-9a7762b/cursor-agent"))
			},
			kind:  "cursor-agent-bundled-old-version",
			stale: []string{".tmp-2026.09.23-1", "2026.08.11-e8db854"},
		},
		{
			name: "conductor: link through /./ is resolved",
			setup: func(f *fixture) {
				base := "Library/Application Support/com.conductor.app/"
				f.file(base+"agent-binaries/codex/0.150.0/codex", 1000, 30*day)
				f.file(base+"agent-binaries/codex/0.156.1/codex", 1000, 20*day)
				f.file(base+"agent-binaries/codex/0.157.0/codex", 1000, 1*day)
				f.link(base+"bin/codex", f.path(base)+"/./agent-binaries/codex/0.156.1/codex")
			},
			kind:  "conductor-old-agent-binary",
			stale: []string{"0.150.0"},
		},
		{
			name: "single version: nothing to do",
			setup: func(f *fixture) {
				f.file(".vibe-kanban/bin/v0.1.26-20260307160046/vibe", 1000, 100*day)
			},
			kind: "vibe-kanban-old-version",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			if c.running != "" {
				f.runner.out["ps -axo comm="] = "/sbin/launchd\n" + f.home + "/.local" + c.running + "\n"
			}
			r := f.scan()
			f.checkInvariants(r)
			var got []string
			for _, it := range r.byKind(c.kind) {
				got = append(got, it.Path[strings.LastIndex(it.Path, "/")+1:])
				if !it.Recommended || it.Risk > core.RiskModerate {
					t.Errorf("%s: old versions are recommended and at most moderate", it.Name)
				}
			}
			if strings.Join(got, ",") != strings.Join(c.stale, ",") {
				t.Errorf("stale = %v, want %v", got, c.stale)
			}
		})
	}
}

func TestVersionFromLink(t *testing.T) {
	f := newFixture(t)
	dir := f.dir("v", 0)
	f.file("v/1.0/bin/tool", 10, 0)
	f.link("abs", f.path("v/1.0/bin/tool"))
	f.link("rel", "v/1.0")
	f.link("out", "/usr/bin/true")
	f.link("self", dir)
	for link, want := range map[string]string{"abs": "1.0", "rel": "1.0", "out": "", "self": "", "missing": ""} {
		if got := versionFromLink(f.path(link), dir); got != want {
			t.Errorf("versionFromLink(%s) = %q, want %q", link, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.1.10", "2.1.9", 1},
		{"2.1.9", "2.1.10", -1},
		{"1.0.0", "1.0.0", 0},
		{"v1.2.0", "1.1.9", 1},
		{"1.2.3.1", "1.2.3", 1},
		{"1.2.3-beta", "1.2.3", -1},
		{"2026.09.18-9a7762b", "2026.08.31-4057e58", 1},
		{"2026.06.15-18-00-12-6f5a2cf", "2026.08.11-e8db854", -1},
		{"0.13.6-alpha.0", "0.13.6-alpha.1", -1},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVersionSetsHonourKeepLatest(t *testing.T) {
	f := newFixture(t)
	f.env.KeepLatest = 2
	f.file(".local/share/cursor-agent/versions/2026.01.01-a/x", 1000, 90*day)
	f.file(".local/share/cursor-agent/versions/2026.02.01-b/x", 1000, 60*day)
	f.file(".local/share/cursor-agent/versions/2026.03.01-c/x", 1000, 30*day)
	f.file(".local/share/cursor-agent/versions/2026.04.01-d/x", 1000, 1*day)
	f.link(".local/bin/cursor-agent", f.path(".local/share/cursor-agent/versions/2026.01.01-a/cursor-agent"))
	r := f.scan()
	var got []string
	for _, it := range r.byKind("cursor-agent-old-version") {
		got = append(got, it.Meta["version"])
	}
	// kept: a (symlink target), d and c (2 newest)
	if strings.Join(got, ",") != "2026.02.01-b" {
		t.Errorf("stale = %v", got)
	}
}
