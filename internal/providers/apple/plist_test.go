package apple

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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

// craftBinaryPlist builds a one-object binary plist whose only object is obj.
func craftBinaryPlist(obj []byte) []byte {
	data := append([]byte("bplist00"), obj...)
	tableOff := uint64(len(data))
	data = append(data, 8) // offset of object 0 (offSize 1)
	tr := make([]byte, 32)
	tr[6], tr[7] = 1, 1                             // offSize, refSize
	binary.BigEndian.PutUint64(tr[8:16], 1)         // numObjects
	binary.BigEndian.PutUint64(tr[16:24], 0)        // top object
	binary.BigEndian.PutUint64(tr[24:32], tableOff) // offset table
	return append(data, tr...)
}

// TestParseBinaryPlistHugeLengths feeds object lengths whose byte size
// overflows uint64: the decoder must reject them, never panic.
func TestParseBinaryPlistHugeLengths(t *testing.T) {
	lengths := []uint64{0x8000000000000001, 0xFFFFFFFFFFFFFFFF, 0x4000000000000001, 1 << 62}
	for _, marker := range []byte{0x6F, 0x5F, 0x4F, 0xAF, 0xCF, 0xDF} { // utf16, ascii, data, array, set, dict
		for _, n := range lengths {
			obj := []byte{marker, 0x13} // length follows as an 8-byte integer
			obj = binary.BigEndian.AppendUint64(obj, n)
			obj = append(obj, 0, 0)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("marker %#x length %#x: panic %v", marker, n, r)
					}
				}()
				// parseBinaryPlist directly: parsePlist would hide a panic behind its recover.
				if _, err := parseBinaryPlist(craftBinaryPlist(obj)); err == nil {
					t.Errorf("marker %#x length %#x: expected an error", marker, n)
				}
			}()
		}
	}
}

// buildBinaryPlist assembles a binary plist from raw objects (object i at
// objs[i], object 0 on top) with the given reference size.
func buildBinaryPlist(refSize int, objs ...[]byte) []byte {
	data := []byte("bplist00")
	offs := make([]uint64, len(objs))
	for i, o := range objs {
		offs[i] = uint64(len(data))
		data = append(data, o...)
	}
	tableOff := uint64(len(data))
	for _, o := range offs {
		data = binary.BigEndian.AppendUint32(data, uint32(o)) // offSize 4
	}
	tr := make([]byte, 32)
	tr[6], tr[7] = 4, byte(refSize)
	binary.BigEndian.PutUint64(tr[8:16], uint64(len(objs)))
	binary.BigEndian.PutUint64(tr[24:32], tableOff)
	return append(data, tr...)
}

// parseWithin fails the test when parseBinaryPlist does not return in time
// (a runaway decode keeps running in the background until the test binary exits).
func parseWithin(t *testing.T, d time.Duration, data []byte) (any, error) {
	t.Helper()
	type res struct {
		v   any
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := parseBinaryPlist(data)
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-time.After(d):
		t.Fatalf("decoding a %d-byte plist did not finish within %v", len(data), d)
		return nil, nil
	}
}

// Regression: objects are decoded once per reference, so arrays that each
// reference the next one twice expanded exponentially — 130 bytes took
// seconds, a few more bytes hung the scan and ended in an out-of-memory crash
// (which the recover in parsePlist cannot catch).
func TestParseBinaryPlistSharedReferenceBomb(t *testing.T) {
	for _, depth := range []int{22, 60} {
		objs := make([][]byte, 0, depth+1)
		for i := 0; i < depth; i++ {
			objs = append(objs, []byte{0xA2, byte(i + 1), byte(i + 1)}) // array [next, next]
		}
		objs = append(objs, []byte{0x09}) // true
		if _, err := parseWithin(t, 5*time.Second, buildBinaryPlist(1, objs...)); err == nil {
			t.Errorf("depth %d: expected an error for an exponentially shared object graph", depth)
		}
	}
}

// Distinct objects overlapping the same bytes (every third byte starts a
// 255-byte string) must not multiply the memory copied.
func TestParseBinaryPlistOverlappingStrings(t *testing.T) {
	const k = 2000
	var region []byte
	for i := 0; i < k+100; i++ {
		region = append(region, 0x5F, 0x10, 0xFF) // ASCII string, length 255
	}
	data := []byte("bplist00")
	top := []byte{0xAF, 0x11} // array, 2-byte count
	top = binary.BigEndian.AppendUint16(top, k)
	for i := 1; i <= k; i++ {
		top = binary.BigEndian.AppendUint16(top, uint16(i))
	}
	offs := []uint64{uint64(len(data))}
	data = append(data, top...)
	base := uint64(len(data))
	data = append(data, region...)
	for i := 0; i < k; i++ {
		offs = append(offs, base+uint64(3*i))
	}
	tableOff := uint64(len(data))
	for _, o := range offs {
		data = binary.BigEndian.AppendUint32(data, uint32(o))
	}
	tr := make([]byte, 32)
	tr[6], tr[7] = 4, 2
	binary.BigEndian.PutUint64(tr[8:16], uint64(len(offs)))
	binary.BigEndian.PutUint64(tr[24:32], tableOff)
	data = append(data, tr...)
	if _, err := parseWithin(t, 5*time.Second, data); err == nil {
		t.Errorf("expected an error: %d overlapping strings copy %d bytes out of a %d-byte file", k, k*255, len(data))
	}
}

// A value referenced many times (Apple's writer stores equal strings once)
// is decoded once: the work budgets must not reject such genuine files.
func TestParseBinaryPlistRepeatedString(t *testing.T) {
	const n, size = 3000, 64 << 10
	arr := []byte{0xAF, 0x11}
	arr = binary.BigEndian.AppendUint16(arr, n)
	for i := 0; i < n; i++ {
		arr = append(arr, 1)
	}
	str := []byte{0x5F, 0x12}
	str = binary.BigEndian.AppendUint32(str, size)
	str = append(str, bytes.Repeat([]byte("a"), size)...)
	v, err := parseWithin(t, 5*time.Second, buildBinaryPlist(1, arr, str))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := v.([]any)
	if len(a) != n {
		t.Fatalf("got %d elements, want %d", len(a), n)
	}
	for i, x := range a {
		if s, _ := x.(string); len(s) != size {
			t.Fatalf("element %d: %T of length %d", i, x, len(s))
		}
	}
}

// TestParseBinaryPlistLargeGenuine: a large plist written by Apple's own
// encoder (plutil), full of repeated values, decodes like its XML source.
func TestParseBinaryPlistLargeGenuine(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available")
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>devices</key><array>`)
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&sb, `<dict><key>name</key><string>iPhone 16 Pro</string><key>index</key><integer>%d</integer>`+
			`<key>arch</key><array><string>arm64</string><string>x86_64</string></array><key>ok</key><true/>`+
			`<key>blob</key><data>aGVsbG8=</data><key>nested</key><dict><key>état</key><string>Café ✓ %d</string></dict></dict>`, i%7, i)
	}
	sb.WriteString(`</array></dict></plist>`)
	want, err := parsePlist([]byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "big.plist")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-convert", "binary1", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v %s", err, out)
	}
	data, _ := os.ReadFile(p)
	got, err := parseWithin(t, 10*time.Second, data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Error("binary and XML decodings differ")
	}
}
