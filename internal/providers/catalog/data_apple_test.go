package catalog

import (
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// TestAppleDeviceLogsNeverPreselected: devices purge their crash logs, so the
// copies Xcode imported are often the only ones left. Smart select must never
// pick them, whatever their age.
func TestAppleDeviceLogsNeverPreselected(t *testing.T) {
	var found bool
	for _, e := range Entries() {
		if e.ID != "apple-device-logs" {
			continue
		}
		found = true
		if e.Risk < core.RiskCaution || e.Recommended {
			t.Errorf("apple-device-logs: risk %s recommended %v, want caution and never recommended", e.Risk, e.Recommended)
		}
	}
	if !found {
		t.Fatal("apple-device-logs entry missing")
	}
}
