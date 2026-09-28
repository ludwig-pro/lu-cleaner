package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseRiskRejectsNever(t *testing.T) {
	for _, s := range []string{"never", "n", " Never "} {
		if r, err := ParseRisk(s); err == nil {
			t.Errorf("ParseRisk(%q) = %v, want an error: a maximum risk of never admits caution items", s, r)
		}
	}
	for s, want := range map[string]Risk{"safe": RiskSafe, "s": RiskSafe, "mod": RiskModerate, "moderate": RiskModerate, "c": RiskCaution, "CAUTION": RiskCaution} {
		if r, err := ParseRisk(s); err != nil || r != want {
			t.Errorf("ParseRisk(%q) = %v, %v; want %v", s, r, err, want)
		}
	}
	if _, err := ParseRisk("bogus"); err == nil {
		t.Error("ParseRisk(bogus) accepted")
	}
	// Serialized items keep round-tripping "never".
	var it Item
	if err := json.Unmarshal([]byte(`{"risk":"never","method":"report"}`), &it); err != nil || it.Risk != RiskNever {
		t.Errorf("unmarshal never: %v, %v", it.Risk, err)
	}
	b, _ := json.Marshal(&Item{Risk: RiskNever})
	if !strings.Contains(string(b), `"risk":"never"`) {
		t.Errorf("marshal: %s", b)
	}
}

func TestParseCategoryRejectsToolNames(t *testing.T) {
	ok := map[string]Category{
		"ai": CatAI, "AI": CatAI, "worktrees": CatWorktrees, "wt": CatWorktrees, "worktree": CatWorktrees,
		"artifacts": CatArtifacts, "artifact": CatArtifacts, "sim": CatSimulators, "simulator": CatSimulators,
		"simulators": CatSimulators, "containers": CatContainers, "js": CatJS, " system ": CatSystem,
		"xcode": CatXcode, "android": CatAndroid, "ide": CatIDE, "langs": CatLangs,
	}
	for s, want := range ok {
		if c, err := ParseCategory(s); err != nil || c != want {
			t.Errorf("ParseCategory(%q) = %q, %v; want %q", s, c, err, want)
		}
	}
	// Tool / item names used to widen silently to the whole category:
	// `clean --yes -c cursor` cleaned Claude, Codex, ChatGPT... data.
	for _, s := range []string{"cursor", "claude", "codex", "node_modules", "projects", "npm", "node", "gradle", "emulators", "ios", "docker"} {
		c, err := ParseCategory(s)
		if err == nil {
			t.Errorf("ParseCategory(%q) = %q, want an error", s, c)
			continue
		}
		if !strings.Contains(err.Error(), "not a category") || !strings.Contains(err.Error(), "-k") {
			t.Errorf("ParseCategory(%q): unhelpful error %q", s, err)
		}
	}
	if _, err := ParseCategory("nope"); err == nil || !strings.Contains(err.Error(), "worktrees|") {
		t.Errorf("unknown category error should list the ids: %v", err)
	}
}

// Disabling a category through a tool name (config disabled_categories =
// ["docker"]) must keep working: widening a *disable* is the safe direction.
func TestParseCategoryOrTool(t *testing.T) {
	for s, want := range map[string]Category{"docker": CatContainers, "Cursor": CatAI, "node_modules": CatArtifacts, "trash": CatSystem} {
		c, tool, err := ParseCategoryOrTool(s)
		if err != nil || !tool || c != want {
			t.Errorf("ParseCategoryOrTool(%q) = %q, %v, %v; want %q, true", s, c, tool, err, want)
		}
	}
	if c, tool, err := ParseCategoryOrTool("wt"); err != nil || tool || c != CatWorktrees {
		t.Errorf("ParseCategoryOrTool(wt) = %q, %v, %v", c, tool, err)
	}
	if _, _, err := ParseCategoryOrTool("nope"); err == nil {
		t.Error("unknown name accepted")
	}
}
