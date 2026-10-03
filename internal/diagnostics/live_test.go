package diagnostics

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSentryLiveSmoke is deliberately opt-in. Ordinary tests and CI never
// contact Sentry. The explicit opt-in authorizes one synthetic report, using
// disposable consent state instead of changing the operator's preference.
func TestSentryLiveSmoke(t *testing.T) {
	if os.Getenv("LU_DIAGNOSTICS_LIVE_TEST") != "1" {
		t.Skip("requires explicit LU_DIAGNOSTICS_LIVE_TEST=1 and LU_DIAGNOSTICS_DSN")
	}
	dsn := os.Getenv("LU_DIAGNOSTICS_DSN")
	if _, err := parseDSN(dsn); err != nil {
		t.Fatal("a valid public HTTPS LU_DIAGNOSTICS_DSN is required")
	}
	store := Store{Dir: filepath.Join(t.TempDir(), "diagnostics"), DSN: dsn}
	if err := store.Decide(true); err != nil {
		t.Fatal(err)
	}
	outcome := make(chan liveResult, 1)
	client := &http.Client{Transport: liveTransport{outcome: outcome}}
	session := NewSession(store, "0.0.0-sentry-test", "doctor", client)
	defer session.Close()
	session.SetMode("eco")
	fault := NewFault("cli")
	fault.code = "command_failed"
	session.Capture(fault)
	select {
	case result := <-outcome:
		if result.failed || result.status < 200 || result.status >= 300 {
			t.Fatalf("synthetic report rejected: HTTP %d, transport failure %t", result.status, result.failed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic report did not finish within the network deadline")
	}
	session.Close()
	if session.sent != 1 || len(session.events) != 1 {
		t.Fatal("expected exactly one delivered synthetic report")
	}
	t.Logf("Sentry accepted synthetic event %s (release 0.0.0-sentry-test); verify ingestion and privacy in the project", session.events[0].ID)
}

type liveResult struct {
	status int
	failed bool
}

type liveTransport struct{ outcome chan<- liveResult }

func (transport liveTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(request)
	result := liveResult{failed: err != nil}
	if response != nil {
		result.status = response.StatusCode
	}
	transport.outcome <- result
	return response, err
}
