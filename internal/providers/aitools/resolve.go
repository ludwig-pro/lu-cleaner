package aitools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

type sysStat = syscall.Stat_t

// existence of the folder an AI tool's per-project data belongs to.
type existence int

const (
	existUnknown existence = iota // cannot tell (unmounted volume, unreadable dir, lossy name...)
	existYes
	existNo
)

// pathExistence tells whether the absolute path p exists. A path below an
// unmounted /Volumes/<name>, or one that cannot be stat'ed for another reason
// than "not found", is unknown.
func pathExistence(p string) existence {
	if p == "" || !filepath.IsAbs(p) {
		return existUnknown
	}
	_, err := os.Stat(p)
	switch {
	case err == nil:
		return existYes
	case missingVolume(filepath.Clean(p)):
		return existUnknown
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return existNo
	}
	return existUnknown
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

	mu    sync.Mutex
	lists map[string]*dirListing
}

type dirListing struct {
	names    []string
	nonASCII bool
	err      error
}

func newResolver(enc func(string) string) *resolver {
	return &resolver{enc: enc, root: "/", lists: map[string]*dirListing{}}
}

func (r *resolver) list(dir string) *dirListing {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.lists[dir]; ok {
		return l
	}
	l := &dirListing{}
	des, err := os.ReadDir(dir)
	if err != nil {
		l.err = err
	}
	for _, d := range des {
		n := d.Name()
		if !utf8.ValidString(n) || strings.IndexFunc(n, func(r rune) bool { return r >= utf8.RuneSelf }) >= 0 {
			l.nonASCII = true
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			l.names = append(l.names, n)
		}
	}
	r.lists[dir] = l
	return l
}

// maxEncodedName: Claude Code truncates longer names and appends a hash.
const maxEncodedName = 200

// resolve returns the directory whose encoding is exactly encoded.
func (r *resolver) resolve(encoded string) (string, existence) {
	if encoded == "" || len(encoded) > maxEncodedName {
		return "", existUnknown
	}
	if r.enc(r.root) == encoded {
		return r.root, existYes
	}
	unknown := false
	found := ""
	budget := 4000 // directory visits, keeps pathological names bounded
	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		budget--
		if depth > 64 || budget < 0 {
			unknown = true
			return false
		}
		l := r.list(dir)
		if l.err != nil {
			unknown = true
			return false
		}
		matched := false
		for _, n := range l.names {
			cand := filepath.Join(dir, n)
			e := r.enc(cand)
			if e == encoded {
				if isDir(cand) {
					found = cand
					return true
				}
				continue
			}
			if len(encoded) > len(e) && strings.HasPrefix(encoded, e) && encoded[len(e)] == '-' && isDir(cand) {
				matched = true
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
	if unknown {
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
// first maxBytes of the file (JSONL transcripts are too big to parse).
func readJSONStringField(path, field string, maxBytes int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, maxBytes)
	n, _ := io.ReadFull(f, buf)
	return findJSONString(buf[:n], field)
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
