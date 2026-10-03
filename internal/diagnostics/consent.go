package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/statefile"
	"golang.org/x/sys/unix"
)

// A changed notice or destination requires a new affirmative decision.
const NoticeVersion = "2026-10-03.2"
const Notice = `Optional technical error reports (notice ` + NoticeVersion + `)
Recipient: the lu-cleaner maintainer, contact@ludwigvantours.dev, through Sentry's
dedicated lu-cleaner project in the ludwig-developer organization.
The official project stores error events in Sentry's European Union (Germany) region.
Purpose: diagnose internal failures. No usage analytics or permanent user/device ID.
Sent: release, OS family, architecture, command name, scan mode, fixed error code,
and stack frames limited to lu-cleaner's source (relative filenames and line numbers).
Never sent: file contents, personal paths, command arguments, logs, environment,
panic values, names, email, hostname or history. The receiver necessarily sees the IP
address of the HTTPS connection. The official project scrubs IP addresses and derived
location from events, with additional rules for unsolicited user, request, extra,
breadcrumb and context fields. This is not a promise of anonymity.
Delivery is best effort, with no offline upload backlog. The latest sanitized report
expires after 7 days and is removed on the next diagnostics access. The Developer plan
provides a 30-day lookback for event details. Aggregated issue and release metadata may
remain longer; this is not a guarantee that all server data is deleted after 30 days.
These settings describe the official project. For a custom recipient, verify its
region, retention and privacy policy before consenting.
You may refuse without losing any feature, and revoke with 'diagnostics disable'.
Previously received reports require a deletion request to the maintainer:
contact@ludwigvantours.dev (include the event ID from 'diagnostics export' if available).
Details: https://ludwig-pro.github.io/lu-cleaner/guides/diagnostics/
`

type Consent struct {
	Decision string    `json:"decision"` // granted / declined; absent means undecided
	Notice   string    `json:"notice"`
	Target   string    `json:"target"`
	Updated  time.Time `json:"updated"`
}

type Store struct{ Dir, DSN string }

func (s Store) target() string {
	sum := sha256.Sum256([]byte(s.DSN))
	return hex.EncodeToString(sum[:])
}

func (s Store) Consent() (Consent, error) {
	f, err := os.Lstat(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return Consent{}, nil
	}
	if err != nil {
		return Consent{}, err
	}
	if !f.IsDir() {
		return Consent{}, fmt.Errorf("invalid diagnostics directory")
	}
	d, err := statefile.OpenDir(s.Dir)
	if err != nil {
		return Consent{}, err
	}
	defer d.Close()
	file, err := d.Open("consent.json", unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return Consent{}, nil
	}
	if err != nil {
		return Consent{}, err
	}
	defer file.Close()
	var c Consent
	err = json.NewDecoder(io.LimitReader(file, 4096)).Decode(&c)
	return c, err
}

func (s Store) Enabled() bool {
	if _, err := parseDSN(s.DSN); err != nil {
		return false
	}
	c, err := s.Consent()
	return err == nil && c.Decision == "granted" && c.Notice == NoticeVersion && c.Target == s.target()
}

// Decide records explicit consent independently of the cleaning --yes flag.
func (s Store) Decide(accept bool) error {
	if accept {
		if _, err := parseDSN(s.DSN); err != nil {
			return err
		}
	}
	d, err := statefile.OpenDir(s.Dir)
	if err != nil {
		return err
	}
	defer d.Close()
	unlock, err := d.Lock("consent.lock")
	if err != nil {
		return err
	}
	defer unlock()
	decision := "declined"
	if accept {
		decision = "granted"
	}
	c := Consent{Decision: decision, Notice: NoticeVersion, Target: s.target(), Updated: time.Now().UTC()}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err = d.Put("consent.json", data); err != nil {
		return err
	}
	if !accept {
		return d.Remove("last-report.json")
	}
	return nil
}

func (s Store) Destination() string {
	t, err := parseDSN(s.DSN)
	if err != nil {
		return "not configured"
	}
	return t.host
}

func (s Store) Export() ([]byte, error) {
	if _, err := os.Lstat(s.Dir); err != nil {
		return nil, err
	}
	d, err := statefile.OpenDir(s.Dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	f, err := d.Open("last-report.json", unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if time.Since(st.ModTime()) > 7*24*time.Hour {
		_ = d.Remove("last-report.json")
		return nil, os.ErrNotExist
	}
	if st.Size() > maxReportBytes {
		return nil, fmt.Errorf("diagnostic report exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReportBytes))
	if err == nil && !json.Valid(data) {
		err = fmt.Errorf("invalid diagnostic report")
	}
	return data, err
}
