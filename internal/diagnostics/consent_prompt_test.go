package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNeedsConsentRespectsSavedRefusalAndCurrentAgreement(t *testing.T) {
	for _, scenario := range []string{"new", "granted", "declined", "changed-target", "changed-notice", "changed-declined", "unconfigured"} {
		t.Run(scenario, func(t *testing.T) {
			store := Store{Dir: filepath.Join(t.TempDir(), "diagnostics"), DSN: "https://public@invalid.example/42"}
			if scenario != "new" && scenario != "unconfigured" {
				if err := store.Decide(scenario != "declined" && scenario != "changed-declined"); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "changed-target", "changed-declined":
				store.DSN += "1"
			case "changed-notice":
				consent, _ := store.Consent()
				consent.Notice = "old"
				data, _ := json.Marshal(consent)
				if err := os.WriteFile(filepath.Join(store.Dir, "consent.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "unconfigured":
				store.DSN = ""
			}
			needs, err := store.NeedsConsent()
			want := scenario == "new" || scenario == "changed-target" || scenario == "changed-notice"
			if err != nil || needs != want {
				t.Fatalf("needs=%t want=%t error=%v", needs, want, err)
			}
		})
	}
}
