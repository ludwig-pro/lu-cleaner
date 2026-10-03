package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Minimal Sentry envelope transport. A general SDK would also gather default
// metadata; this transport serializes only Event. Protocol reference:
// https://develop.sentry.dev/sdk/envelopes/
type target struct{ endpoint, host, key string }

var keyRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var projectRE = regexp.MustCompile(`^[0-9]+$`)

func parseDSN(dsn string) (target, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User == nil || u.RawQuery != "" || u.Fragment != "" {
		return target{}, fmt.Errorf("configure a public HTTPS Sentry DSN before enabling reports")
	}
	key := u.User.Username()
	_, password := u.User.Password()
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if password || !keyRE.MatchString(key) || len(parts) == 0 || !projectRE.MatchString(parts[len(parts)-1]) {
		return target{}, fmt.Errorf("invalid public Sentry DSN")
	}
	project := parts[len(parts)-1]
	u.User = nil
	u.Path = "/" + strings.Join(append(parts[:len(parts)-1], "api", project, "envelope"), "/") + "/"
	u.RawPath = ""
	return target{endpoint: u.String(), host: u.Host, key: key}, nil
}

func send(ctx context.Context, client *http.Client, t target, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(payload) > maxEventBytes {
		return fmt.Errorf("event exceeds size limit")
	}
	header, _ := json.Marshal(map[string]string{"event_id": event.ID, "sent_at": time.Now().UTC().Format(time.RFC3339Nano)})
	item, _ := json.Marshal(struct {
		Type   string `json:"type"`
		Length int    `json:"length"`
	}{"event", len(payload)})
	body := bytes.Join([][]byte{header, item, payload, nil}, []byte("\n"))
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-sentry-envelope")
	req.Header.Set("X-Sentry-Auth", "Sentry sentry_version=7, sentry_key="+t.key)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.CopyN(io.Discard, res.Body, 1024)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("report rejected")
	}
	return nil
}
