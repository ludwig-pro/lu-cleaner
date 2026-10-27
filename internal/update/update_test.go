package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/statefile"
)

type testPrompt struct {
	yes     bool
	offers  []Offer
	notices []string
	confirm func()
}

func (p *testPrompt) Confirm(_ context.Context, o Offer) (bool, error) {
	p.offers = append(p.offers, o)
	if p.confirm != nil {
		p.confirm()
	}
	return p.yes, nil
}
func (p *testPrompt) Notice(s string) { p.notices = append(p.notices, s) }

type localTransport struct {
	base        http.RoundTripper
	destination *url.URL
}

func (tr localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme = tr.destination.Scheme
	clone.URL.Host = tr.destination.Host
	clone.Host = tr.destination.Host
	return tr.base.RoundTrip(clone)
}

func testService(t *testing.T, handler http.HandlerFunc) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "lu-cleaner")
	if err := os.WriteFile(target, []byte("original executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	destination, _ := url.Parse(server.URL)
	s := New(Options{Version: "0.4.1", StateDir: filepath.Join(dir, "state")})
	s.client = newHTTPClient()
	s.client.Transport = localTransport{base: server.Client().Transport, destination: destination}
	s.executable = func() (string, error) { return target, nil }
	s.supported = func() bool { return true }
	s.now = func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
	return s, target
}

func metadata(tag string) []byte {
	body := struct {
		Tag    string              `json:"tag_name"`
		Assets []map[string]string `json:"assets"`
	}{Tag: tag}
	for _, name := range []string{archiveName, checksumsName} {
		body.Assets = append(body.Assets, map[string]string{"name": name, "browser_download_url": releaseAssetURL(tag, name)})
	}
	data, _ := json.Marshal(body)
	return data
}

func fatBinary(t *testing.T) []byte {
	t.Helper()
	data := make([]byte, 8192+32)
	b := bytes.NewBuffer(data[:0])
	for _, n := range []uint32{0xcafebabe, 2, uint32(macho.CpuAmd64), 3, 4096, 32, 12, uint32(macho.CpuArm64), 0, 8192, 32, 12} {
		if err := binary.Write(b, binary.BigEndian, n); err != nil {
			t.Fatal(err)
		}
	}
	for i, cpu := range []macho.Cpu{macho.CpuAmd64, macho.CpuArm64} {
		buf := bytes.NewBuffer(data[(i+1)*4096 : (i+1)*4096])
		sub := uint32(0)
		if i == 0 {
			sub = 3
		}
		for _, n := range []uint32{0xfeedfacf, uint32(cpu), sub, uint32(macho.TypeExec), 0, 0, 0, 0} {
			_ = binary.Write(buf, binary.LittleEndian, n)
		}
	}
	return data
}

type tarEntry struct {
	name string
	kind byte
	data []byte
	size int64
}

func makeArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		size := int64(len(e.data))
		if e.size != 0 {
			size = e.size
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: e.kind, Mode: 0o755, Size: size, Linkname: "elsewhere"}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
		if size != int64(len(e.data)) {
			break
		}
	}
	_ = tw.Close()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func fixtureHandler(archive []byte, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch filepath.Base(r.URL.Path) {
		case "latest":
			_, _ = w.Write(metadata("v0.4.2"))
		case checksumsName:
			fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(archive), archiveName)
		case archiveName:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAcceptedUpdatePreservesBackupAndReplacesExecutable(t *testing.T) {
	binary := fatBinary(t)
	archive := makeArchive(t, tarEntry{name: "lu-cleaner", data: binary}, tarEntry{name: "README.md", data: []byte("readme")}, tarEntry{name: "LICENSE", data: []byte("license")})
	var calls atomic.Int32
	s, target := testService(t, fixtureHandler(archive, &calls))
	p := &testPrompt{yes: true}
	if err := s.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, target), binary) {
		t.Fatal("new executable not installed")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".lu-cleaner-backup-*"))
	if len(backups) != 1 || string(readFile(t, backups[0])) != "original executable" {
		t.Fatal("original backup not preserved")
	}
	info, _ := os.Stat(target)
	saved, _ := os.Stat(backups[0])
	if info.Mode().Perm() != 0o755 || saved.Mode().Perm() != 0o700 {
		t.Fatal("unsafe installation modes")
	}
	if calls.Load() != 3 || len(p.offers) != 1 || !strings.Contains(strings.Join(p.notices, "\n"), "next invocation uses 0.4.2") || !strings.Contains(strings.Join(p.notices, "\n"), backups[0]) {
		t.Fatalf("bad update result: %d %+v %+v", calls.Load(), p.offers, p.notices)
	}
	staging, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".lu-cleaner-update-*"))
	archives, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".lu-cleaner-archive-*"))
	if len(staging)+len(archives) != 0 {
		t.Fatal("temporary update artifacts survived")
	}
	if err := s.Run(context.Background(), p); err != nil || calls.Load() != 3 || len(p.offers) != 1 {
		t.Fatal("update offered again within 24h")
	}
}

func TestDeclineAndOfflineConsumeDailyAttempt(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(fmt.Sprint(offline), func(t *testing.T) {
			var calls atomic.Int32
			s, target := testService(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if offline {
					w.WriteHeader(503)
					return
				}
				_, _ = w.Write(metadata("v0.4.2"))
			})
			p := &testPrompt{}
			for range 2 {
				if err := s.Run(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 1 || string(readFile(t, target)) != "original executable" || len(p.notices) != 0 {
				t.Fatalf("daily check changed executable or retried: %d %+v", calls.Load(), p)
			}
			wantOffers := 1
			if offline {
				wantOffers = 0
			}
			if len(p.offers) != wantOffers {
				t.Fatalf("offers: %+v", p.offers)
			}
		})
	}
}

func TestReservationRules(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		data []byte
		due  bool
	}{
		{"missing", nil, true}, {"24h", []byte(`{"schema":1,"last_attempt_utc":"2026-10-26T12:00:00Z"}`), true},
		{"recent", []byte(`{"schema":1,"last_attempt_utc":"2026-10-26T12:00:01Z"}`), false},
		{"future", []byte(`{"schema":1,"last_attempt_utc":"2026-10-28T12:00:00Z"}`), false},
		{"schema", []byte(`{"schema":2,"last_attempt_utc":"2026-10-25T12:00:00Z"}`), false},
		{"malformed", []byte(`broken`), false}, {"oversized", bytes.Repeat([]byte(" "), 4097), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := statefile.OpenDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			if tc.data != nil {
				if err := dir.Put("attempt.json", tc.data); err != nil {
					t.Fatal(err)
				}
			}
			if got := reserveAttempt(dir, now); got != tc.due {
				t.Fatalf("reservation=%t want %t", got, tc.due)
			}
			if !tc.due {
				f, _ := dir.Open("attempt.json", os.O_RDONLY)
				defer f.Close()
				data, _ := io.ReadAll(f)
				if !bytes.Equal(data, tc.data) {
					t.Fatal("invalid state overwritten")
				}
			}
		})
	}
}

func TestConcurrentReservationsAndUnsafeState(t *testing.T) {
	t.Run("concurrent", func(t *testing.T) {
		path := t.TempDir()
		var wg sync.WaitGroup
		var reserved atomic.Int32
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dir, err := statefile.OpenDir(path)
				if err != nil {
					return
				}
				defer dir.Close()
				if reserveAttempt(dir, time.Now()) {
					reserved.Add(1)
				}
			}()
		}
		wg.Wait()
		if reserved.Load() != 1 {
			t.Fatalf("reservations %d", reserved.Load())
		}
	})
	t.Run("symlink", func(t *testing.T) {
		path := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(path, "attempt.json")); err != nil {
			t.Fatal(err)
		}
		dir, err := statefile.OpenDir(path)
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		if reserveAttempt(dir, time.Now()) || string(readFile(t, victim)) != "untouched" {
			t.Fatal("followed symlink state")
		}
	})
	t.Run("busy", func(t *testing.T) {
		path := t.TempDir()
		dir, err := statefile.OpenDir(path)
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		unlock, err := dir.Lock("attempt.lock")
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if reserveAttempt(dir, time.Now()) {
			t.Fatal("ignored busy lock")
		}
	})
}

func TestVersionPolicy(t *testing.T) {
	for _, s := range []string{"0.9.0", "v0.10.0", "1.0.1"} {
		if _, err := parseStableVersion(s); err != nil {
			t.Fatalf("stable %s rejected", s)
		}
	}
	for _, s := range []string{"dev", "1.2", "1.2.3-beta.1", "1.2.3+meta", "v01.2.3", "1.2.3\n", "18446744073709551616.0.0", "v0.0.0-20261027120000-abc"} {
		if _, err := parseStableVersion(s); err == nil {
			t.Fatalf("unsafe version accepted %q", s)
		}
	}
	a, _ := parseStableVersion("0.10.0")
	b, _ := parseStableVersion("0.9.99")
	if !a.newer(b) || b.newer(a) || a.newer(a) {
		t.Fatal("version order")
	}
	info := &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.4.1"}}
	if v, ok := installedVersion("dev", info); !ok || v.String() != "0.4.1" {
		t.Fatal("tagged go install unavailable")
	}
	info.Main.Replace = &debug.Module{}
	if _, ok := installedVersion("dev", info); ok {
		t.Fatal("replacement auto-updates")
	}
	info.Main.Replace = nil
	info.Settings = []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}
	if _, ok := installedVersion("dev", info); ok {
		t.Fatal("modified development build auto-updates")
	}
	info.Settings = nil
	info.Main.Path = "other/module"
	if _, ok := installedVersion("dev", info); ok {
		t.Fatal("other module auto-updates")
	}
}

func TestMetadataErrorsAreSilentAndDoNotRetry(t *testing.T) {
	base := string(metadata("v0.4.2"))
	for _, tc := range []struct {
		name   string
		status int
		data   string
	}{
		{"forbidden", 403, ""}, {"rate-limit", 429, ""}, {"malformed", 200, "{"}, {"trailing", 200, base + "{}"}, {"oversized", 200, strings.Repeat(" ", maxMetadata+1)},
		{"prerelease", 200, strings.Replace(base, `"tag_name":"v0.4.2"`, `"tag_name":"v0.4.2","prerelease":true`, 1)},
		{"draft", 200, strings.Replace(base, `"tag_name":"v0.4.2"`, `"tag_name":"v0.4.2","draft":true`, 1)},
		{"missing-assets", 200, `{"tag_name":"v0.4.2","assets":[]}`},
		{"wrong-host", 200, strings.Replace(base, "https://github.com/", "https://evil.example/", 1)},
		{"duplicate-asset", 200, strings.Replace(base, `"assets":[`, fmt.Sprintf(`"assets":[{"name":%q,"browser_download_url":%q},`, archiveName, releaseAssetURL("v0.4.2", archiveName)), 1)},
		{"downgrade", 200, string(metadata("v0.3.0"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s, target := testService(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.data)
			})
			p := &testPrompt{yes: true}
			if err := s.Run(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || len(p.offers)+len(p.notices) != 0 || string(readFile(t, target)) != "original executable" {
				t.Fatalf("metadata error escaped %+v calls %d", p, calls.Load())
			}
		})
	}
}

func TestLookupTimeoutAndParentCancellation(t *testing.T) {
	s, _ := testService(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	s.lookupTimeout = 20 * time.Millisecond
	p := &testPrompt{}
	start := time.Now()
	if err := s.Run(context.Background(), p); err != nil || time.Since(start) > time.Second || len(p.offers) != 0 {
		t.Fatalf("offline lookup blocked command: %v", err)
	}
	var calls atomic.Int32
	s, _ = testService(t, fixtureHandler(nil, &calls))
	ctx, cancel := context.WithCancel(context.Background())
	p = &testPrompt{yes: true, confirm: cancel}
	if err := s.Run(ctx, p); err != context.Canceled || calls.Load() != 1 {
		t.Fatalf("cancel after offer %v requests %d", err, calls.Load())
	}
}

func TestPayloadFailuresKeepOriginal(t *testing.T) {
	valid := fatBinary(t)
	wrongArch := append([]byte(nil), valid...)
	binary.BigEndian.PutUint32(wrongArch[28:32], uint32(macho.CpuAmd64))
	wrongType := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(wrongType[4096+12:4096+16], uint32(macho.TypeObj))
	for _, tc := range []struct {
		name    string
		archive []byte
	}{
		{"corrupt", []byte("not gzip")},
		{"script", makeArchive(t, tarEntry{name: "lu-cleaner", data: []byte("#!/bin/sh\n")})},
		{"traversal", makeArchive(t, tarEntry{name: "../lu-cleaner", data: valid})},
		{"absolute", makeArchive(t, tarEntry{name: "/lu-cleaner", data: valid})},
		{"link", makeArchive(t, tarEntry{name: "lu-cleaner", kind: tar.TypeSymlink})},
		{"hardlink", makeArchive(t, tarEntry{name: "lu-cleaner", kind: tar.TypeLink})},
		{"duplicate", makeArchive(t, tarEntry{name: "lu-cleaner", data: valid}, tarEntry{name: "lu-cleaner", data: valid})},
		{"missing", makeArchive(t, tarEntry{name: "README.md", data: []byte("no binary")})},
		{"large-binary", makeArchive(t, tarEntry{name: "lu-cleaner", size: maxBinary + 1})},
		{"wrong-arch", makeArchive(t, tarEntry{name: "lu-cleaner", data: wrongArch})},
		{"wrong-type", makeArchive(t, tarEntry{name: "lu-cleaner", data: wrongType})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s, target := testService(t, fixtureHandler(tc.archive, &calls))
			p := &testPrompt{yes: true}
			if err := s.Run(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if string(readFile(t, target)) != "original executable" || len(p.notices) != 1 || !strings.Contains(p.notices[0], "could not be verified") {
				t.Fatalf("unsafe payload changed executable or lacked warning %+v", p.notices)
			}
			backups, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".lu-cleaner-backup-*"))
			if len(backups) != 0 {
				t.Fatal("backup created before verification")
			}
		})
	}
}

func TestChecksumRejection(t *testing.T) {
	archive := makeArchive(t, tarEntry{name: "lu-cleaner", data: fatBinary(t)})
	correct := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), archiveName)
	for _, data := range []string{correct + correct, strings.Repeat("0", 64) + "  " + archiveName + "\n", "bad checksum\n", strings.Replace(correct, archiveName, "elsewhere", 1), strings.Replace(correct, correct[:1], "g", 1)} {
		t.Run(data[:min(12, len(data))], func(t *testing.T) {
			var calls atomic.Int32
			handler := fixtureHandler(archive, &calls)
			s, target := testService(t, func(w http.ResponseWriter, r *http.Request) {
				if filepath.Base(r.URL.Path) == checksumsName {
					_, _ = io.WriteString(w, data)
					return
				}
				handler(w, r)
			})
			p := &testPrompt{yes: true}
			if err := s.Run(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if string(readFile(t, target)) != "original executable" || len(p.notices) != 1 {
				t.Fatal("bad checksum installed")
			}
		})
	}
}

func TestTargetPolicyAndChanges(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "unsafe-parent", "package-manager", "group-writable", "world-writable", "setuid", "setgid"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			s, target := testService(t, fixtureHandler(nil, &calls))
			switch kind {
			case "group-writable":
				_ = os.Chmod(target, 0o775)
			case "world-writable":
				_ = os.Chmod(target, 0o757)
			case "setuid":
				_ = os.Chmod(target, 0o755|os.ModeSetuid)
			case "setgid":
				_ = os.Chmod(target, 0o755|os.ModeSetgid)
			case "symlink":
				other := target + "-real"
				_ = os.Rename(target, other)
				_ = os.Symlink(other, target)
			case "hardlink":
				_ = os.Link(target, target+"-link")
			case "directory":
				_ = os.Remove(target)
				_ = os.Mkdir(target, 0o755)
			case "unsafe-parent":
				_ = os.Chmod(filepath.Dir(target), 0o777)
			case "package-manager":
				dir := filepath.Join(filepath.Dir(target), "Cellar")
				_ = os.Mkdir(dir, 0o700)
				newPath := filepath.Join(dir, "lu-cleaner")
				_ = os.Rename(target, newPath)
				s.executable = func() (string, error) { return newPath, nil }
			}
			p := &testPrompt{yes: true}
			if err := s.Run(context.Background(), p); err != nil || calls.Load() != 0 || len(p.offers) != 0 {
				t.Fatalf("unsafe target used: %v %d", err, calls.Load())
			}
		})
	}
	t.Run("swapped-after-consent", func(t *testing.T) {
		var calls atomic.Int32
		s, target := testService(t, fixtureHandler(nil, &calls))
		p := &testPrompt{yes: true, confirm: func() { _ = os.Remove(target); _ = os.WriteFile(target, []byte("another executable"), 0o755) }}
		if err := s.Run(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		if string(readFile(t, target)) != "another executable" || calls.Load() != 1 || len(p.notices) != 1 {
			t.Fatal("replaced changed target")
		}
	})
	t.Run("swapped-during-download", func(t *testing.T) {
		archive := makeArchive(t, tarEntry{name: "lu-cleaner", data: fatBinary(t)})
		var calls atomic.Int32
		var target string
		handler := fixtureHandler(archive, &calls)
		s, path := testService(t, func(w http.ResponseWriter, r *http.Request) {
			if filepath.Base(r.URL.Path) == archiveName {
				_ = os.WriteFile(target, []byte("changed in place"), 0o755)
			}
			handler(w, r)
		})
		target = path
		p := &testPrompt{yes: true}
		if err := s.Run(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		if string(readFile(t, target)) != "changed in place" || !strings.Contains(strings.Join(p.notices, "\n"), "could not be installed") {
			t.Fatal("changed executable replaced")
		}
	})
}

func TestTransportRejectsUnsafeRedirects(t *testing.T) {
	for _, raw := range []string{"http://github.com/a", "https://evil.example/a", "https://github.com:443/a", "https://user@github.com/a", "https://github.com/a#fragment"} {
		u, _ := url.Parse(raw)
		if allowedURL(u) {
			t.Fatalf("unsafe address accepted %s", raw)
		}
	}
	for _, host := range []string{"github.com", "release-assets.githubusercontent.com"} {
		u, _ := url.Parse("https://" + host + "/asset")
		if !allowedURL(u) {
			t.Fatalf("GitHub delivery refused %s", host)
		}
	}
	var calls atomic.Int32
	s, _ := testService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "http://github.com/unsafe", 302)
	})
	p := &testPrompt{}
	if err := s.Run(context.Background(), p); err != nil || calls.Load() != 1 || len(p.offers) != 0 {
		t.Fatalf("followed downgrade redirect: %v calls %d", err, calls.Load())
	}
}

func TestUnknownBuildsAndUnavailableStateSkipBeforeNetwork(t *testing.T) {
	for _, kind := range []string{"development", "unsupported", "state-file", "state-symlink"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			s, _ := testService(t, fixtureHandler(nil, &calls))
			switch kind {
			case "development":
				s.options.Version = "dev"
				s.buildInfo = func() (*debug.BuildInfo, bool) {
					return &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "(devel)"}}, true
				}
			case "unsupported":
				s.supported = func() bool { return false }
			case "state-file":
				if err := os.WriteFile(s.options.StateDir, []byte("untouched"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "state-symlink":
				if err := os.Symlink(t.TempDir(), s.options.StateDir); err != nil {
					t.Fatal(err)
				}
			}
			p := &testPrompt{yes: true}
			if err := s.Run(context.Background(), p); err != nil || calls.Load() != 0 || len(p.offers) != 0 {
				t.Fatalf("unsupported update ran %v %d", err, calls.Load())
			}
		})
	}
}

func TestBusyInstallerNeverPrompts(t *testing.T) {
	var calls atomic.Int32
	s, target := testService(t, fixtureHandler(nil, &calls))
	dir, err := statefile.OpenDir(s.options.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	unlock, err := dir.Lock("install.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	p := &testPrompt{yes: true}
	if err := s.Run(context.Background(), p); err != nil || calls.Load() != 1 || len(p.offers) != 0 || string(readFile(t, target)) != "original executable" {
		t.Fatalf("busy install prompted %v %d %+v", err, calls.Load(), p.offers)
	}
}

func TestUpdateKeepsPreviousBackups(t *testing.T) {
	var calls atomic.Int32
	archive := makeArchive(t, tarEntry{name: "lu-cleaner", data: fatBinary(t)})
	s, path := testService(t, fixtureHandler(archive, &calls))
	previous := filepath.Join(filepath.Dir(path), ".lu-cleaner-backup-existing")
	if err := os.WriteFile(previous, []byte("prior backup"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := &testPrompt{yes: true}
	if err := s.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, previous)) != "prior backup" {
		t.Fatal("older backup was removed or overwritten")
	}
}
