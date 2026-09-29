package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sample written by `config init` must load as is (no unknown keys) and
// yield the defaults.
func TestSampleLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(Sample), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LU_CLEANER_CONFIG", p)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load(sample): %v", err)
	}
	if c.KeepLatest != 1 || c.MaxDepth != 8 || c.MinSize != "1MB" || c.StaleAfter != "14d" || c.UseTrash {
		t.Errorf("sample does not yield the defaults: %+v", c)
	}
}

// The keep_latest documentation lists exactly what honours it: Gradle
// distributions do not (they were listed), the providers below do.
func TestSampleKeepLatestDoc(t *testing.T) {
	i := strings.Index(Sample, "keep_latest =")
	if i < 0 {
		t.Fatal("no keep_latest in the sample")
	}
	// The comment block right above the key.
	start := strings.LastIndex(Sample[:i], "\n\n")
	doc := Sample[start:i]
	if strings.Contains(doc, "Gradle") {
		t.Errorf("keep_latest doc claims Gradle honours it:\n%s", doc)
	}
	for _, want := range []string{"AI tool versions", "NDK", "build-tools", "JetBrains", "Android Studio", "catalog", "node versions", "simulator runtimes"} {
		if !strings.Contains(doc, want) {
			t.Errorf("keep_latest doc does not mention %q:\n%s", want, doc)
		}
	}
}
