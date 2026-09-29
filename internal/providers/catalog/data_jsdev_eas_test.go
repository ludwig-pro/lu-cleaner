package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// An `eas build` / `eas update` upload creates its work folder inside
// $TMPDIR/eas-cli-nodejs: a folder touched within the last day may belong
// to a live upload and is not proposed.
func TestEasCliTmpSkipsLiveUploads(t *testing.T) {
	e := entryByID(t, "js-eas-cli-tmp")
	if e.OlderThan < 24*time.Hour {
		t.Fatalf("js-eas-cli-tmp OlderThan = %v, want >= 24h", e.OlderThan)
	}
	for _, c := range []struct {
		age  time.Duration
		want bool
	}{
		{time.Hour, false},
		{3 * 24 * time.Hour, true},
	} {
		home, _ := filepath.EvalSymlinks(t.TempDir())
		tmp, _ := filepath.EvalSymlinks(t.TempDir())
		dir := filepath.Join(tmp, "eas-cli-nodejs")
		mkFile(t, filepath.Join(dir, "0b6c1d0e-upload", "project.tar.gz"), 10, c.age)
		ts := time.Now().Add(-c.age)
		if err := os.Chtimes(dir, ts, ts); err != nil {
			t.Fatal(err)
		}
		env := core.NewEnv()
		env.Home, env.TmpDir = home, tmp
		got := false
		for _, m := range e.Expand(env) {
			if m.path == dir {
				got = true
			}
		}
		if got != c.want {
			t.Errorf("folder touched %v ago: matched = %v, want %v", c.age, got, c.want)
		}
	}
}
