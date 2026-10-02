package aitools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanmemo"
)

type sysStat = syscall.Stat_t

// existence of the folder an AI tool's per-project data belongs to.
type existence int

const (
	existUnknown existence = iota // cannot tell (unmounted volume, unreadable dir, lossy name...)
	existYes
	existNo
)

// pathExistence tells whether the absolute path p exists. p is reported
// missing (existNo) only when stat says ENOENT/ENOTDIR AND the nearest
// existing ancestor can be listed: anything else (EACCES, EPERM from TCC, an
// unmounted /Volumes/<name>, a logged-out ~/Library/CloudStorage provider,
// an unreadable parent) is unknown, and unknown is never cleaned.
func pathExistence(p string) existence {
	if p == "" || !filepath.IsAbs(p) {
		return existUnknown
	}
	p = filepath.Clean(p)
	_, err := os.Stat(p)
	switch {
	case err == nil:
		return existYes
	case missingVolume(p):
		return existUnknown
	case isNotExist(err):
		if ancestorReadable(p) {
			return existNo
		}
	}
	return existUnknown
}

// isNotExist reports ENOENT or ENOTDIR (a file sits where a parent dir was).
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// ancestorReadable reports whether the nearest existing ancestor of p can be
// listed. A directory we may not read (permissions, TCC-protected folders)
// cannot prove that p is absent.
func ancestorReadable(p string) bool {
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err == nil {
			if !fi.IsDir() {
				return true // a regular file: nothing can live below it
			}
			f, err := os.Open(d)
			if err != nil {
				return false
			}
			_, err = f.Readdirnames(1)
			f.Close()
			return err == nil || errors.Is(err, io.EOF)
		}
		if !isNotExist(err) || d == "/" || d == "." {
			return false
		}
	}
}

// claudeEncode is Claude Code's project directory naming: every character
// that is not [A-Za-z0-9] becomes '-' (JS works on UTF-16 units, so a
// character outside the BMP becomes two dashes).
func claudeEncode(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for _, r := range p {
		switch {
		case r < utf8.RuneSelf && isAlnum(byte(r)):
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// cursorEncode is Cursor's ~/.cursor/projects naming: runs of characters that
// are not [A-Za-z0-9] collapse into one '-', leading/trailing dashes trimmed
// ("/Users/me/.codex/x" -> "Users-me-codex-x").
func cursorEncode(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	dash := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if isAlnum(c) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteByte(c)
			continue
		}
		dash = true
	}
	return b.String()
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// resolver maps a lossy encoded project directory name back to a real path
// by walking the filesystem from "/" and following only the children whose
// encoded path is a prefix of the name. Directory listings are cached.
type resolver struct {
	enc  func(string) string
	root string // "/" (tests may use another root)

	lists scanmemo.Cache[string, *dirListing]
}

type dirListing struct {
	names    []string
	nonASCII bool
	err      error
}

func newResolver(enc func(string) string) *resolver {
	return &resolver{enc: enc, root: "/"}
}

func (r *resolver) list(ctx context.Context, dir string) *dirListing {
	l, err := r.lists.Get(ctx, dir, func(ctx context.Context) (*dirListing, error) {
		l := &dirListing{}
		des, err := fsx.ReadDir(ctx, dir)
		l.err = err
		for _, d := range des {
			n := d.Name()
			if !utf8.ValidString(n) || strings.IndexFunc(n, func(r rune) bool { return r >= utf8.RuneSelf }) >= 0 {
				l.nonASCII = true
			}
			if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
				l.names = append(l.names, n)
			}
		}
		return l, nil
	})
	if err != nil {
		return &dirListing{err: err}
	}
	return l
}

// maxEncodedName: Claude Code truncates longer names and appends a hash.
const maxEncodedName = 200

// resolve returns the directory whose encoding is encoded.
//
// Encodings are compared case-insensitively: APFS volumes are (by default)
// case-insensitive, so a folder opened as ~/work/app is the folder ~/Work/App
// on disk. On a case-sensitive volume this can only turn a "missing" into a
// "found", the safe direction.
//
// existNo is only returned when the name demonstrably encodes a path below
// root (at least its first component matched a child of root) and no
// candidate exists; a name that does not look like a path at all ("empty-window",
// a chat id...) is unknown.
func (r *resolver) resolve(ctx context.Context, encoded string) (string, existence) {
	if ctx.Err() != nil || encoded == "" || len(encoded) > maxEncodedName {
		return "", existUnknown
	}
	if strings.EqualFold(r.enc(r.root), encoded) {
		return r.root, existYes
	}
	unknown := false
	rootMatched := false
	found := ""
	budget := 4000 // directory visits, keeps pathological names bounded
	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		budget--
		if ctx.Err() != nil || depth > 64 || budget < 0 {
			unknown = true
			return false
		}
		l := r.list(ctx, dir)
		if l.err != nil {
			unknown = true
			return false
		}
		matched := false
		for _, n := range l.names {
			cand := filepath.Join(dir, n)
			e := r.enc(cand)
			if strings.EqualFold(e, encoded) {
				if isDir(cand) {
					found = cand
					return true
				}
				continue
			}
			if len(encoded) > len(e) && strings.EqualFold(encoded[:len(e)], e) && encoded[len(e)] == '-' && isDir(cand) {
				matched = true
				if depth == 0 {
					rootMatched = true
				}
				if walk(cand, depth+1) {
					return true
				}
			}
		}
		if !matched && (l.nonASCII || dir == "/Volumes") {
			// the name may encode a character we do not reproduce, or an unmounted volume
			unknown = true
		}
		return false
	}
	if walk(r.root, 0) {
		return found, existYes
	}
	if unknown || !rootMatched {
		return "", existUnknown
	}
	return "", existNo
}

// decodeNaive turns an encoded name back into a display path, best effort
// ("-Users-me-foo" -> "/Users/me/foo").
func decodeNaive(name string, leadingSlash bool) string {
	p := strings.ReplaceAll(name, "-", "/")
	if leadingSlash && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/.")
	}
	return p
}

// readJSONStringField returns the first value of "field":"..." found in the
// first maxBytes of the file (JSONL transcripts are too big to parse). The
// file is read in chunks so that the common case (the field is near the
// top) does not read maxBytes of every transcript.
func readJSONStringField(path, field string, maxBytes int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const chunk = 32 << 10
	buf := make([]byte, 0, min(maxBytes, 4*chunk))
	for len(buf) < maxBytes {
		n := min(chunk, maxBytes-len(buf))
		start := len(buf)
		buf = append(buf, make([]byte, n)...)
		got, err := io.ReadFull(f, buf[start:])
		buf = buf[:start+got]
		// a value cut by the chunk boundary is not found yet and is found
		// complete on the next round
		if v := findJSONString(buf, field); v != "" {
			return v
		}
		if err != nil {
			break // EOF or read error
		}
	}
	return ""
}

func findJSONString(buf []byte, field string) string {
	for _, key := range []string{`"` + field + `":"`, `"` + field + `": "`} {
		idx := bytes.Index(buf, []byte(key))
		if idx < 0 {
			continue
		}
		rest := buf[idx+len(key)-1:] // starts at the opening quote
		for i := 1; i < len(rest); i++ {
			switch rest[i] {
			case '\\':
				i++
			case '"':
				var v string
				if json.Unmarshal(rest[:i+1], &v) == nil {
					return v
				}
				return ""
			}
		}
	}
	return ""
}
