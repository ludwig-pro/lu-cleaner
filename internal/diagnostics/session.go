package diagnostics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/statefile"
)

const maxEvents = 16
const maxEventBytes = 16 << 10
const maxReportBytes = 256 << 10

type Event struct {
	ID        string            `json:"event_id"`
	Timestamp time.Time         `json:"timestamp"`
	Platform  string            `json:"platform"`
	Level     string            `json:"level"`
	Release   string            `json:"release"`
	Tags      map[string]string `json:"tags"`
	Exception struct {
		Values []Exception `json:"values"`
	} `json:"exception"`
}
type Exception struct {
	Type       string `json:"type"`
	Value      string `json:"value"`
	Stacktrace struct {
		Frames []Frame `json:"frames"`
	} `json:"stacktrace"`
}

// Session is bounded and nonblocking for workers. There is no sender goroutine
// or HTTP client when consent is absent. Events are never replayed next run.
type Session struct {
	store                  Store
	version, command, mode string
	mu                     sync.Mutex
	events                 []Event
	seen                   map[string]bool
	closed                 bool
	queue                  chan Event
	done                   chan struct{}
	cancel                 context.CancelFunc
	sent                   int
}

var versionRE = regexp.MustCompile(`^(dev|v?[0-9]+\.[0-9]+\.[0-9]+[a-zA-Z0-9.+-]*|[a-f0-9]{7,40}(-dirty)?)$`)
var filenameRE = regexp.MustCompile(`^(internal|cmd)/[a-zA-Z0-9_./-]+\.go$`)
var functionRE = regexp.MustCompile(`^(internal|cmd)/[a-zA-Z0-9_./*()\[\]-]+$`)

func filteredFrames(frames []Frame) []Frame {
	var out []Frame
	for _, frame := range frames {
		if len(out) == 24 {
			break
		}
		if len(frame.Filename) > 240 || len(frame.Function) > 240 || !filenameRE.MatchString(frame.Filename) || !functionRE.MatchString(frame.Function) || strings.Contains(frame.Filename, "..") {
			continue
		}
		frame.InApp = true
		out = append(out, frame)
	}
	return out
}

func NewSession(store Store, version, command string, client *http.Client) *Session {
	if !versionRE.MatchString(version) {
		version = "unknown"
	}
	s := &Session{store: store, version: version, command: commandName(command), mode: "unknown", seen: map[string]bool{}}
	// Expire local state lazily, without reading logs or personal files.
	_, _ = store.Export()
	if !store.Enabled() {
		return s
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.queue, s.done = cancel, make(chan Event, maxEvents), make(chan struct{})
	t, _ := parseDSN(store.DSN)
	go func() {
		defer close(s.done)
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-s.queue:
				if !ok {
					return
				}
				// Revocation and destination changes apply to running invocations.
				if ctx.Err() != nil || !store.Enabled() {
					continue
				}
				if send(ctx, &copyClient, t, e) == nil {
					s.mu.Lock()
					s.sent++
					s.mu.Unlock()
				}
			}
		}
	}()
	return s
}

func commandName(s string) string {
	switch s {
	case "lu-cleaner", "scan", "clean", "artifacts", "worktrees", "devices", "analyze", "doctor":
		return s
	default:
		return "internal"
	}
}

func (s *Session) SetMode(mode string) {
	if mode != "eco" && mode != "fast" {
		return
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
}

func (s *Session) Capture(err error) {
	var f *Fault
	if !errors.As(err, &f) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.events) == maxEvents {
		return
	}
	code := f.code
	if code == "" {
		code = "internal_panic"
	}
	frames := filteredFrames(f.Frames)
	fingerprint := componentName(f.Component) + code
	if len(frames) > 0 {
		fingerprint += frames[0].Function
	}
	if s.seen[fingerprint] {
		return
	}
	s.seen[fingerprint] = true
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	e := Event{ID: hex.EncodeToString(id[:]), Timestamp: time.Now().UTC(), Platform: "go", Level: "error", Release: s.version,
		Tags: map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "command": s.command, "scan_mode": s.mode, "component": componentName(f.Component), "error_code": code, "notice": NoticeVersion}}
	x := Exception{Type: "InternalPanic", Value: "Internal task failed; results may be incomplete"}
	if code != "internal_panic" {
		x.Type, x.Value = "TechnicalFailure", code
	}
	// Sentry orders frames from oldest to most recent.
	for i := len(frames) - 1; i >= 0; i-- {
		x.Stacktrace.Frames = append(x.Stacktrace.Frames, frames[i])
	}
	e.Exception.Values = []Exception{x}
	if data, err := json.Marshal(e); err != nil || len(data) > maxEventBytes {
		return
	}
	s.events = append(s.events, e)
	if s.queue != nil {
		select {
		case s.queue <- e:
		default:
		}
	}
}

// Close gives delivery 500 ms, then cancels the active request. Reporting never
// controls the exit code. Only sanitized events are written to local state.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.queue != nil {
		close(s.queue)
	}
	s.mu.Unlock()
	if s.done != nil {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-s.done:
		case <-timer.C:
		}
		timer.Stop()
		s.cancel()
		// net/http wakes on request cancellation; bound even a broken custom transport.
		timer = time.NewTimer(100 * time.Millisecond)
		select {
		case <-s.done:
		case <-timer.C:
		}
		timer.Stop()
	}
	s.mu.Lock()
	data, err := json.Marshal(struct {
		Notice string  `json:"notice"`
		Sent   int     `json:"sent"`
		Events []Event `json:"events"`
	}{NoticeVersion, s.sent, s.events})
	count := len(s.events)
	s.mu.Unlock()
	if err != nil || count == 0 || len(data) > maxReportBytes {
		return
	}
	// A concurrent revocation must not recreate a report cleared by disable.
	d, err := statefile.OpenDir(s.store.Dir)
	if err != nil {
		return
	}
	defer d.Close()
	unlock, err := d.Lock("consent.lock")
	if err != nil {
		return
	}
	defer unlock()
	c, err := s.store.Consent()
	if err != nil || c.Decision == "declined" {
		return
	}
	_ = d.Put("last-report.json", data)
}
