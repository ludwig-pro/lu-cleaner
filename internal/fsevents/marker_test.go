package fsevents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The marker may already be recorded when the first replay (without the
// marker directory) runs: Changes must still find it.
func TestChangesMarkerAlreadyRecorded(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	for range 5 {
		since := CurrentEventID()
		os.WriteFile(filepath.Join(dir, "x"), nil, 0o644)
		time.Sleep(100 * time.Millisecond) // everything recorded, marker too once written
		start := time.Now()
		if !Changes(context.Background(), []string{dir}, since, 3*time.Second, func(Event) bool { return true }) {
			t.Fatal("replay failed")
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("replay took %v: the marker was missed", d)
		}
	}
}
