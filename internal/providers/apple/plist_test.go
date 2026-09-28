package apple

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const samplePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>WorkspacePath</key>
	<string>/Users/me/app/ios/App &amp; Co.xcworkspace</string>
	<key>LastAccessedDate</key>
	<date>2026-09-09T23:28:10Z</date>
	<key>ArchiveVersion</key>
	<integer>2</integer>
	<key>Ratio</key>
	<real>1.5</real>
	<key>isDeleted</key>
	<false/>
	<key>isEphemeral</key>
	<true/>
	<key>Blob</key>
	<data>aGVsbG8=</data>
	<key>Architectures</key>
	<array>
		<string>arm64</string>
		<string>x86_64</string>
	</array>
	<key>ApplicationProperties</key>
	<dict>
		<key>CFBundleIdentifier</key>
		<string>dev.example.app</string>
		<key>Unicode</key>
		<string>Café ✓</string>
	</dict>
</dict>
</plist>
`

func checkSample(t *testing.T, v any) {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("top level is %T", v)
	}
	if got := pString(m, "WorkspacePath"); got != "/Users/me/app/ios/App & Co.xcworkspace" {
		t.Errorf("WorkspacePath = %q", got)
	}
	if got := pTime(m, "LastAccessedDate"); !got.Equal(time.Date(2026, 9, 9, 23, 28, 10, 0, time.UTC)) {
		t.Errorf("LastAccessedDate = %v", got)
	}
	if m["ArchiveVersion"] != int64(2) || m["Ratio"] != 1.5 {
		t.Errorf("numbers: %#v %#v", m["ArchiveVersion"], m["Ratio"])
	}
	if pBool(m, "isDeleted") || !pBool(m, "isEphemeral") {
		t.Errorf("booleans wrong")
	}
	if !bytes.Equal(m["Blob"].([]byte), []byte("hello")) {
		t.Errorf("data = %q", m["Blob"])
	}
	if !reflect.DeepEqual(m["Architectures"], []any{"arm64", "x86_64"}) {
		t.Errorf("array = %#v", m["Architectures"])
	}
	app := pDict(m, "ApplicationProperties")
	if pString(app, "CFBundleIdentifier") != "dev.example.app" || pString(app, "Unicode") != "Café ✓" {
		t.Errorf("nested dict = %#v", app)
	}
}

func TestParseXMLPlist(t *testing.T) {
	v, err := parsePlist([]byte(samplePlist))
	if err != nil {
		t.Fatal(err)
	}
	checkSample(t, v)
}

// TestParseBinaryPlist converts the XML sample with plutil and checks both
// decoders agree (binary plists: simulator container metadata...).
func TestParseBinaryPlist(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "x.plist")
	os.WriteFile(p, []byte(samplePlist), 0o644)
	if out, err := exec.Command(plutil, "-convert", "binary1", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v %s", err, out)
	}
	data, _ := os.ReadFile(p)
	if !bytes.HasPrefix(data, []byte("bplist00")) {
		t.Fatal("not converted to binary")
	}
	v, err := readPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	checkSample(t, v)

	// Truncated or corrupted input must fail cleanly, never panic.
	for n := 0; n < len(data); n++ {
		_, _ = parsePlist(data[:n])
		bad := append([]byte(nil), data...)
		bad[n] ^= 0xFF
		_, _ = parsePlist(bad)
	}
}

func TestParsePlistRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "hello", "{}", "bplist00"} {
		if _, err := parsePlist([]byte(in)); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
	if _, err := readPlistDict(filepath.Join(t.TempDir(), "missing.plist")); err == nil {
		t.Error("missing file: expected an error")
	}
}
