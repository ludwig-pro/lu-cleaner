package jsdev

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func vs(ss ...string) []version {
	var out []version
	for _, s := range ss {
		v, ok := parseVersion(s)
		if !ok {
			panic(s)
		}
		out = append(out, v)
	}
	return out
}

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"v18.20.4", true}, {"18.20.4", true}, {"v18.20", false}, {"20", false}, {"lts", false},
		{"v18.20.4-rc.1", false}, {".downloads", false}, {"v1.2.x", false},
	} {
		if _, ok := parseVersion(tc.in); ok != tc.ok {
			t.Errorf("parseVersion(%q) ok=%v, want %v", tc.in, ok, tc.ok)
		}
	}
}

func TestSpecBest(t *testing.T) {
	installed := vs("v16.20.2", "v18.17.1", "v20.11.1", "v20.19.6", "v21.7.3", "v22.20.0", "v22.21.1", "v24.20.0")
	for _, tc := range []struct {
		spec string
		want string // "" = no match
		// fallback applies bestOrMajor
		fallback string
	}{
		{"20", "v20.19.6", "v20.19.6"},
		{"v20.11.1", "v20.11.1", "v20.11.1"},
		{"20.11", "v20.11.1", "v20.11.1"},
		{"20.19.5", "", "v20.19.6"}, // exact pin not installed: newest of the major
		{"22.x", "v22.21.1", "v22.21.1"},
		{"^22.1.0", "v22.21.1", "v22.21.1"},
		{">=18", "v18.17.1", "v18.17.1"},
		{"lts/iron", "v20.19.6", "v20.19.6"},
		{"lts/hydrogen", "v18.17.1", "v18.17.1"},
		{"lts/*", "v24.20.0", "v24.20.0"},
		{"lts/unknownfuture", "v24.20.0", "v24.20.0"},
		{"node", "v24.20.0", "v24.20.0"},
		{"stable", "v24.20.0", "v24.20.0"},
		{"system", "", ""},
		{"", "", ""},
		{"garbage", "", ""},
		{"19", "", ""},
		{"24.20.0 # comment", "v24.20.0", "v24.20.0"},
	} {
		sp := parseSpec(tc.spec)
		got := ""
		if i := sp.best(installed); i >= 0 {
			got = installed[i].String()
		}
		if got != tc.want {
			t.Errorf("best(%q) = %q, want %q", tc.spec, got, tc.want)
		}
		got = ""
		if i := sp.bestOrMajor(installed); i >= 0 {
			got = installed[i].String()
		}
		if got != tc.fallback {
			t.Errorf("bestOrMajor(%q) = %q, want %q", tc.spec, got, tc.fallback)
		}
	}
}

func TestParseEtime(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"03:21", 3*time.Minute + 21*time.Second, true},
		{"01:02:03", time.Hour + 2*time.Minute + 3*time.Second, true},
		{"03-03:47:29", 3*24*time.Hour + 3*time.Hour + 47*time.Minute + 29*time.Second, true},
		{"  12:00  ", 12 * time.Minute, true},
		{"", 0, false},
		{"abc", 0, false},
		{"1:2:3:4", 0, false},
	} {
		got, ok := parseEtime(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseEtime(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestToolVersionsNode(t *testing.T) {
	got := toolVersionsNode("ruby 3.3.0\nnodejs 20.11.1 18.19.0 # two\nnode lts\n# nodejs 16\n")
	if len(got) != 3 || got[0].raw != "20.11.1" || got[1].raw != "18.19.0" || got[2].kind != specNewestLTS {
		t.Fatalf("toolVersionsNode = %+v", got)
	}
}

func TestResolveNvmAlias(t *testing.T) {
	root := t.TempDir()
	w := func(rel, content string) {
		p := filepath.Join(root, "alias", rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content+"\n"), 0o644)
	}
	w("default", "lts/*")
	w("lts/*", "lts/krypton")
	w("lts/krypton", "v24.21.0")
	w("work", "myalias")
	w("myalias", "20")
	w("loop", "loop")
	if sp := resolveNvmAlias(root, "default"); sp.kind != specExact || sp.raw != "v24.21.0" {
		t.Errorf("default -> %+v", sp)
	}
	if sp := resolveNvmAlias(root, "work"); sp.kind != specPrefix || sp.raw != "20" {
		t.Errorf("work -> %+v", sp)
	}
	if sp := resolveNvmAlias(root, "loop"); sp.kind != specNone {
		t.Errorf("loop -> %+v", sp)
	}
	if sp := resolveNvmAlias(root, "missing"); sp.kind != specNone {
		t.Errorf("missing -> %+v", sp)
	}
}

func TestFnmVersionDir(t *testing.T) {
	for in, want := range map[string]string{
		"/h/.local/share/fnm/node-versions/v22.22.0/installation":          "/h/.local/share/fnm/node-versions/v22.22.0",
		"/h/.local/share/fnm/node-versions/v22.22.0/installation/bin/node": "/h/.local/share/fnm/node-versions/v22.22.0",
		"/h/.local/share/fnm/node-versions/v22.22.0":                       "/h/.local/share/fnm/node-versions/v22.22.0",
		"/h/.hermes/node/bin/node":                                         "",
	} {
		if got := fnmVersionDir(in); got != want {
			t.Errorf("fnmVersionDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNpmLeftoverName(t *testing.T) {
	for name, want := range map[string]bool{
		".openclaw-Hd08o9WA":    true,
		".claude-code-jK9KMe16": true,
		".codex-AbCdEf12":       true,
		".package-lock.json":    false,
		".bin":                  false,
		"eas-cli":               false,
		".foo-short":            false,
		".foo-ABCDEFGHI":        false,
	} {
		if got := npmLeftoverName.MatchString(name); got != want {
			t.Errorf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestProcSnapshot(t *testing.T) {
	now := time.Now()
	ps := &procSnapshot{
		ok:      true,
		args:    []string{"node /h/.npm/_npx/abc/node_modules/.bin/mcp", "/bin/zsh"},
		envText: "zsh PATH=/h/.local/state/fnm_multishells/123_456/bin:/usr/bin FNM_MULTISHELL_PATH=/h/.local/state/fnm_multishells/789_1011",
		bins:    []string{"/h/.nvm/versions/node/v24.1.0/bin/node"},
		starts:  []time.Time{now.Add(-time.Hour)},
	}
	if ps.usesDir("/h/.npm/_npx/abc") != 1 || ps.usesDir("/h/.npm/_npx/ab") != 0 {
		t.Errorf("usesDir args")
	}
	if ps.usesDir("/h/.nvm/versions/node/v24.1.0") != 1 || ps.usesDir("/h/.nvm/versions/node/v24.1") != 0 {
		t.Errorf("usesDir bins")
	}
	refs := ps.multishellRefs()
	if !refs["123_456"] || !refs["789_1011"] || len(refs) != 2 {
		t.Errorf("refs = %v", refs)
	}
	if !ps.startedAround(now.Add(-time.Hour+time.Minute), 10*time.Minute, time.Minute) {
		t.Errorf("startedAround should match")
	}
	if ps.startedAround(now.Add(-2*time.Hour), 10*time.Minute, time.Minute) {
		t.Errorf("startedAround should not match")
	}
}

func TestExpoSDKOf(t *testing.T) {
	for in, want := range map[string]int{
		`{"dependencies":{"expo":"~56.0.0"}}`:     56,
		`{"dependencies":{"expo":"^55.0.1"}}`:     55,
		`{"devDependencies":{"expo":"54.0.3"}}`:   54,
		`{"dependencies":{"expo":">= 53"}}`:       53,
		`{"dependencies":{"expo":"latest"}}`:      0,
		`{"dependencies":{"expo":"workspace:*"}}`: 0,
		`{"dependencies":{"react":"19.0.0"}}`:     0,
		`not json`:                                0,
	} {
		if got := expoSDKOf(in); got != want {
			t.Errorf("expoSDKOf(%s) = %d, want %d", in, got, want)
		}
	}
}
