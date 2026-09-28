package aitools

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

func TestConductorArchivedContexts(t *testing.T) {
	f := newFixture(t)
	base := "conductor/archived-contexts/myrepo/"
	f.file(base+"lagos/app.apk", 50_000, 0)
	f.text(base+"lagos/notes.md", "n", 0)
	f.ageTree(base+"lagos", 60*day)
	f.file(base+"paris/plan.md", 100, 0)
	f.ageTree(base+"paris", 60*day)
	f.age(base+"paris/plan.md", 3*day) // edited recently: whole workspace kept
	f.file("conductor/workspaces/myrepo/tokyo/src.ts", 100, 0)
	f.ageTree("conductor/workspaces", 90*day)
	r := f.scan()
	f.checkInvariants(r)
	it := r.one(t, "conductor-archived-contexts")
	if len(it.Paths) != 1 || !hasTarget(it, f.path(base+"lagos")) || it.Risk != core.RiskCaution || it.Recommended {
		t.Errorf("archived contexts = %v (%v, rec %v)", it.Paths, it.Risk, it.Recommended)
	}
	for _, x := range r.items {
		for _, tg := range x.Targets() {
			if strings.Contains(tg, "conductor/workspaces") {
				t.Errorf("Conductor workspaces belong to the worktrees provider: %s", tg)
			}
		}
	}
}

func TestLeftoversOfUninstalledApps(t *testing.T) {
	for _, installed := range []bool{false, true} {
		f := newFixture(t)
		if installed {
			f.dir("Applications/ChatGPT Atlas.app", 0)
			f.dir("Applications/Antigravity.app", 0)
		}
		f.file("Library/Application Support/com.openai.atlas/browser-data/x", 5000, 0)
		f.file(".gemini/antigravity-browser-profile/Default/x", 5000, 0)
		f.file(".antigravity/extensions/e/x", 5000, 0)
		f.file(".gemini/antigravity/browser_recordings/rec1/f.jpg", 5000, 0)
		f.ageTree(".gemini/antigravity/browser_recordings", 200*day)
		f.file(".gemini/antigravity/conversations/c.pb", 5000, 0)
		oauth := f.text(".gemini/oauth_creds.json", "{}", 0)
		gsettings := f.text(".gemini/settings.json", "{}", 0)

		r := f.scan()
		f.checkInvariants(r, oauth, gsettings, f.path(".gemini"))
		want := map[string]int{"chatgpt-atlas-leftover": 1, "antigravity-browser-profile": 1, "antigravity-extensions": 1}
		for kind, n := range want {
			if installed {
				n = 0
			}
			if got := len(r.byKind(kind)); got != n {
				t.Errorf("installed=%v %s: got %d items, want %d", installed, kind, got, n)
			}
		}
		rec := r.one(t, "antigravity-browser-recordings")
		if rec.Risk != core.RiskCaution {
			t.Errorf("recordings risk = %v", rec.Risk)
		}
		conv := r.one(t, "antigravity-conversations")
		if conv.CanClean() {
			t.Errorf("conversations are report-only")
		}
	}
}

func TestLocalModels(t *testing.T) {
	f := newFixture(t)
	m := ".ollama/models/"
	f.text(m+"manifests/registry.ollama.ai/library/llama3/8b",
		`{"config":{"digest":"sha256:c1"},"layers":[{"digest":"sha256:w1"},{"digest":"sha256:shared"}]}`, 20*day)
	f.text(m+"manifests/registry.ollama.ai/library/llama3/latest",
		`{"config":{"digest":"sha256:c2"},"layers":[{"digest":"sha256:w2"},{"digest":"sha256:shared"}]}`, 10*day)
	f.text(m+"manifests/hf.co/org/qwen/q4",
		`{"config":{"digest":"sha256:c3"},"layers":[{"digest":"sha256:w3"}]}`, 5*day)
	for _, b := range []string{"c1", "w1", "shared", "c2", "w2", "c3", "w3"} {
		f.file(m+"blobs/sha256-"+b, 20_000, 0)
	}
	key := f.text(".ollama/id_ed25519", "PRIVATE", 0)
	f.file(".lmstudio/models/lmstudio-community/Qwen-7B-GGUF/q.gguf", 30_000, 0)
	f.file(".cache/huggingface/hub/models--openai--whisper-tiny/blobs/abc", 2<<20, 0)
	f.dir(".cache/huggingface/hub/.locks", 0)
	f.file(".cache/whisper/base.pt", 2<<20, 0)
	f.file(".cache/whisper/notes.txt", 2<<20, 0)
	f.link(".cache/whisper/dangling.bin", "/nonexistent-lu-cleaner/dangling.bin")
	f.link(".cache/whisper/external.bin", "/Volumes/lu-cleaner-test-not-mounted-42/external.bin")

	r := f.scan()
	f.checkInvariants(r, key, f.path(m+"blobs/sha256-shared"))

	ollama := r.byKind("ollama-model")
	byName := map[string]*core.Item{}
	for _, it := range ollama {
		byName[it.Meta["model"]] = it
		if it.Risk != core.RiskCaution || it.Recommended || len(it.ProcessGuard) == 0 {
			t.Errorf("%s: models are caution, never recommended, guarded", it.Name)
		}
	}
	if len(byName) != 3 || byName["llama3:8b"] == nil || byName["llama3:latest"] == nil || byName["hf.co/org/qwen:q4"] == nil {
		t.Fatalf("ollama models = %v", names(ollama))
	}
	l8 := byName["llama3:8b"]
	if !hasTarget(l8, f.path(m+"blobs/sha256-w1")) || !hasTarget(l8, f.path(m+"blobs/sha256-c1")) ||
		!hasTarget(l8, f.path(m+"manifests/registry.ollama.ai/library/llama3/8b")) || l8.Meta["shared_layers_kept"] != "1" {
		t.Errorf("llama3:8b targets = %v meta %v", l8.Targets(), l8.Meta)
	}
	if n := len(r.byKind("lmstudio-model")); n != 1 {
		t.Errorf("lmstudio models = %d", n)
	}
	hf := r.one(t, "huggingface-model")
	if !strings.Contains(hf.Name, "openai/whisper-tiny") {
		t.Errorf("hf name = %q", hf.Name)
	}
	var whisper []string
	for _, it := range r.byKind("whisper-model") {
		whisper = append(whisper, it.Name)
		if strings.Contains(it.Name, "external") {
			if it.Method != core.MethodReport || !strings.Contains(it.Warn, "external volume") || it.CanClean() {
				t.Errorf("model under a missing mount must be report-only: %+v", it)
			}
		}
	}
	sort.Strings(whisper)
	if strings.Join(whisper, "|") != "Whisper model · base|Whisper model · external" {
		t.Errorf("whisper items = %v (dangling symlinks and non-model files are skipped)", whisper)
	}
}

// TestIDStabilityAndPlaceholders scans the same fixture twice: identical IDs,
// every placeholder is followed by a final version with the same ID.
func TestIDStabilityAndPlaceholders(t *testing.T) {
	f := newFixture(t)
	f.file(".local/share/claude/versions/1.0.0", 1000, 20*day)
	f.file(".local/share/claude/versions/1.0.1", 1000, 1*day)
	f.file(".codex/sessions/2026/01/02/rollout-a.jsonl", 1000, 200*day)
	gone := f.path("gone/wt")
	p := ".claude/projects/" + claudeEncode(gone)
	f.text(p+"/s.jsonl", transcript(gone), 0)
	f.ageTree(p, 3*day)

	ids := func() []string {
		r := f.scan()
		var out []string
		for id, it := range r.items {
			if it.Sizing {
				t.Errorf("%s: last emission still Sizing", id)
			}
			out = append(out, id)
		}
		sort.Strings(out)
		return out
	}
	a, b := ids(), ids()
	if len(a) != 3 || !reflect.DeepEqual(a, b) {
		t.Errorf("ids differ or unexpected:\n%v\n%v", a, b)
	}
}

func TestProtectedAndExcludedPathsAreNeverProposed(t *testing.T) {
	f := newFixture(t)
	f.file(".local/share/claude/versions/1.0.0", 1000, 20*day)
	f.file(".local/share/claude/versions/1.0.1", 1000, 1*day)
	f.file(".codex/logs_2.sqlite", 1000, 1*day)
	excluded := f.path(".local/share/claude/versions/1.0.0")
	f.env.Exclude = []string{excluded}
	guard := f.env.Protected
	f.env.Protected = func(p string) bool { return guard(p) || p == f.path(".codex/logs_2.sqlite") }
	r := f.scan()
	for _, it := range r.items {
		for _, tg := range it.Targets() {
			if tg == excluded || tg == f.path(".codex/logs_2.sqlite") {
				t.Errorf("%s proposes %s", it.ID, tg)
			}
		}
	}
}

func TestEmptyHomeAndCancellation(t *testing.T) {
	f := newFixture(t)
	r := f.scan()
	if len(r.items) != 0 {
		t.Errorf("empty home: %v", r.items)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.file(".local/share/claude/versions/1.0.0", 1000, 20*day)
	f.file(".local/share/claude/versions/1.0.1", 1000, 1*day)
	n := 0
	err := f.p.Scan(ctx, f.env, func(*core.Item) { n++ })
	if err == nil || n != 0 {
		t.Errorf("cancelled scan: err=%v emitted=%d", err, n)
	}
}

func TestProviderMetadata(t *testing.T) {
	p := New()
	if p.ID() != providerID || p.Title() == "" {
		t.Errorf("id/title")
	}
	cats := p.Categories()
	if len(cats) != 2 || cats[0] != core.CatAI || cats[1] != core.CatIDE {
		t.Errorf("categories = %v", cats)
	}
}
