package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T, server *httptest.Server) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "diagnostics"), DSN: strings.Replace(server.URL, "https://", "https://public@", 1) + "/42"}
}

func TestConsentIsRequiredAndDestinationBound(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	store := testStore(t, server)
	for _, decision := range []string{"undecided", "declined", "changed-target", "old-notice"} {
		t.Run(decision, func(t *testing.T) {
			switch decision {
			case "declined":
				if err := store.Decide(false); err != nil {
					t.Fatal(err)
				}
			case "changed-target":
				if err := store.Decide(true); err != nil {
					t.Fatal(err)
				}
				store.DSN += "1"
			case "old-notice":
				if err := store.Decide(true); err != nil {
					t.Fatal(err)
				}
				c, _ := store.Consent()
				c.Notice = "old"
				data, _ := json.Marshal(c)
				if err := os.WriteFile(filepath.Join(store.Dir, "consent.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s := NewSession(store, "v0.3.1", "scan", server.Client())
			if s.queue != nil || s.done != nil {
				t.Fatal("reporter started without valid consent")
			}
			s.Capture(NewFault("ai"))
			s.Close()
			if requests.Load() != 0 {
				t.Fatal("network request without consent")
			}
		})
	}
}

func TestWireReportContainsOnlyTechnicalFields(t *testing.T) {
	var body []byte
	var auth, contentType, path string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		auth, contentType, path = r.Header.Get("X-Sentry-Auth"), r.Header.Get("Content-Type"), r.URL.Path
	}))
	defer server.Close()
	store := testStore(t, server)
	if err := store.Decide(true); err != nil {
		t.Fatal(err)
	}
	s := NewSession(store, "v0.3.1", "scan", server.Client())
	s.SetMode("eco")
	ctx := With(context.Background(), s)
	const secret = "/Users/private-person/project/token=secret@example.com"
	err := Catch(ctx, "ai", func() error { panic(secret) })
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe panic error: %v", err)
	}
	Capture(ctx, err) // duplicate captures across layers must not duplicate delivery
	s.Close()
	lines := bytes.Split(body, []byte("\n"))
	if len(lines) != 4 || path != "/api/42/envelope/" || contentType != "application/x-sentry-envelope" || !strings.Contains(auth, "sentry_key=public") {
		t.Fatalf("invalid envelope: %s %s %s %q", path, contentType, auth, body)
	}
	var item struct {
		Type   string
		Length int
	}
	if err := json.Unmarshal(lines[1], &item); err != nil || item.Type != "event" || item.Length != len(lines[2]) {
		t.Fatalf("invalid item length: %+v %v", item, err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(lines[2], &payload); err != nil {
		t.Fatal(err)
	}
	for key := range payload {
		if !strings.Contains("|event_id|timestamp|platform|level|release|tags|exception|", "|"+key+"|") {
			t.Fatalf("unexpected field: %s", key)
		}
	}
	for _, forbidden := range []string{secret, "/Users/", "hostname", "environment", "server_name", "\"user\"", "request", "argv"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("private field leaked: %s", forbidden)
		}
	}
	var event Event
	if err := json.Unmarshal(lines[2], &event); err != nil {
		t.Fatal(err)
	}
	if len(event.ID) != 32 || event.Tags["command"] != "scan" || event.Tags["scan_mode"] != "eco" || event.Tags["error_code"] != "internal_panic" {
		t.Fatalf("wrong event: %+v", event)
	}
	if len(event.Exception.Values[0].Stacktrace.Frames) == 0 {
		t.Fatal("repository frames missing")
	}
	for _, frame := range event.Exception.Values[0].Stacktrace.Frames {
		if !strings.HasPrefix(frame.Filename, "internal/") && !strings.HasPrefix(frame.Filename, "cmd/") {
			t.Fatalf("unsafe frame: %+v", frame)
		}
	}
	data, err := store.Export()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) || s.sent != 1 {
		t.Fatalf("report duplicated or unsafe: sent=%d", s.sent)
	}
	for _, name := range []string{"consent.json", "last-report.json"} {
		st, err := os.Stat(filepath.Join(store.Dir, name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("permissions: %s %v", name, err)
		}
	}
}

func TestRevocationDropsPendingReports(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); close(started); <-release }))
	defer server.Close()
	store := testStore(t, server)
	if err := store.Decide(true); err != nil {
		t.Fatal(err)
	}
	s := NewSession(store, "dev", "clean", server.Client())
	s.Capture(NewFault("ai"))
	<-started
	s.Capture(NewFault("clean"))
	if err := store.Decide(false); err != nil {
		t.Fatal(err)
	}
	close(release)
	s.Close()
	if requests.Load() != 1 {
		t.Fatal("pending report sent after revocation")
	}
	if _, err := store.Export(); !os.IsNotExist(err) {
		t.Fatalf("report recreated after revocation: %v", err)
	}
}

func TestQueueAndShutdownAreBounded(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer server.Close()
	store := testStore(t, server)
	if err := store.Decide(true); err != nil {
		t.Fatal(err)
	}
	s := NewSession(store, "dev", "analyze", server.Client())
	s.Capture(NewFault("ai"))
	<-started
	for i := range 1000 {
		f := NewFault("clean")
		f.Frames = []Frame{{Filename: "internal/clean/clean.go", Function: fmt.Sprintf("internal/clean.task%d", i)}}
		s.Capture(f)
	}
	if len(s.events) != maxEvents || len(s.queue) > maxEvents {
		t.Fatal("unbounded reports")
	}
	begin := time.Now()
	s.Close()
	if time.Since(begin) >= time.Second {
		t.Fatal("reporting delayed exit")
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("sender survived cancellation")
	}
	close(release)
	s.Capture(NewFault("tui"))
	if len(s.events) != maxEvents {
		t.Fatal("capture after Close")
	}
}

func TestTransportDoesNotFollowRedirectsOrRetry(t *testing.T) {
	var redirected atomic.Int64
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer other.Close()
	var attempts atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Location", other.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	store := testStore(t, server)
	if err := store.Decide(true); err != nil {
		t.Fatal(err)
	}
	s := NewSession(store, "dev", "scan", server.Client())
	s.Capture(NewFault("ai"))
	s.Close()
	if attempts.Load() != 1 || redirected.Load() != 0 {
		t.Fatal("report retried or forwarded to an unapproved recipient")
	}
}

func TestInvalidDSNAndExpiredLocalReport(t *testing.T) {
	for _, dsn := range []string{"", "http://key@example.com/42", "https://key:secret@example.com/42", "https://key@example.com/42?token=secret", "https://key@example.com/not-a-project"} {
		if _, err := parseDSN(dsn); err == nil {
			t.Fatalf("accepted unsafe DSN %q", dsn)
		}
	}
	store := Store{Dir: t.TempDir()}
	s := NewSession(store, "a private release string", "a personal argument", nil)
	s.Capture(NewFault("/Users/private"))
	s.Close()
	data, err := store.Export()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private")) || bytes.Contains(data, []byte("personal")) {
		t.Fatal("unsafe metadata")
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.Dir, "last-report.json"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Export(); !os.IsNotExist(err) {
		t.Fatalf("expired report retained: %v", err)
	}
}

func TestFilteringAlsoRunsAtSerializationBoundary(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	s := NewSession(store, "dev", "scan", nil)
	f := NewFault("ai")
	f.Frames = []Frame{
		{Filename: "/Users/private/project/main.go", Function: "token=secret@example.com"},
		{Filename: "internal/../../private.go", Function: "internal/private"},
		{Filename: "internal/clean/clean.go", Function: "internal/clean.Run", Lineno: 42},
	}
	s.Capture(f)
	s.Close()
	data, err := store.Export()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private")) || bytes.Contains(data, []byte("secret")) {
		t.Fatal("unsafe manually supplied frame leaked")
	}
	frames := s.events[0].Exception.Values[0].Stacktrace.Frames
	if len(frames) != 1 || frames[0].Lineno != 42 {
		t.Fatalf("wrong filtered frames: %+v", frames)
	}
}

func TestSiblingFailureCancelsAndIsReturned(t *testing.T) {
	ctx, group := NewGroup(context.Background(), "ai")
	defer group.Close()
	done := make(chan struct{})
	go func() { defer close(done); defer Recover(ctx, "ai"); panic("do not export this") }()
	<-done
	if ctx.Err() == nil || group.Err() == nil || strings.Contains(group.Err().Error(), "export") {
		t.Fatal("panic became a successful scan")
	}
}
