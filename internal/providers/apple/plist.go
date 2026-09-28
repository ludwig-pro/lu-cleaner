package apple

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Property lists are read natively (XML and binary "bplist00"), so scanning
// hundreds of simulator containers never spawns plutil. Values decode to
// map[string]any, []any, string, int64, float64, bool, time.Time, []byte.

const maxPlistSize = 16 << 20

var errNotPlist = errors.New("not a property list")

// readPlist parses the property list at path.
func readPlist(path string) (any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPlistSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPlistSize {
		return nil, fmt.Errorf("%s: property list too large", path)
	}
	return parsePlist(data)
}

// readPlistDict parses path and returns its top-level dictionary.
func readPlistDict(path string) (map[string]any, error) {
	v, err := readPlist(path)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: top-level object is not a dictionary", path)
	}
	return m, nil
}

// parsePlist decodes a binary or XML property list. The input is untrusted
// (simulator containers, archives, truncated writes): a decoder bug must fail
// this one file, never abort the whole scan part through a panic.
func parsePlist(data []byte) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, err = nil, fmt.Errorf("plist: malformed input (%v)", r)
		}
	}()
	if bytes.HasPrefix(data, []byte("bplist00")) {
		return parseBinaryPlist(data)
	}
	if bytes.Contains(data[:min(len(data), 512)], []byte("<plist")) || bytes.HasPrefix(bytes.TrimSpace(data), []byte("<?xml")) {
		return parseXMLPlist(data)
	}
	return nil, errNotPlist
}

// ---------------------------------------------------------------- accessors

func pString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func pTime(m map[string]any, key string) time.Time {
	switch v := m[key].(type) {
	case time.Time:
		return v
	case string: // some tools store ISO dates as strings
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func pDict(m map[string]any, key string) map[string]any {
	d, _ := m[key].(map[string]any)
	return d
}

func pBool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

// ---------------------------------------------------------------- XML

func parseXMLPlist(data []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return nil, errNotPlist
			}
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "plist" {
			continue
		}
		return xmlValue(d, se, 0)
	}
}

func xmlValue(d *xml.Decoder, se xml.StartElement, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("plist: nesting too deep")
	}
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		var key string
		haveKey := false
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					s, err := xmlText(d)
					if err != nil {
						return nil, err
					}
					key, haveKey = s, true
					continue
				}
				v, err := xmlValue(d, t, depth+1)
				if err != nil {
					return nil, err
				}
				if haveKey {
					m[key] = v
					haveKey = false
				}
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		var a []any
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := xmlValue(d, t, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			case xml.EndElement:
				return a, nil
			}
		}
	case "true", "false":
		if err := d.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	}
	s, err := xmlText(d)
	if err != nil {
		return nil, err
	}
	switch se.Name.Local {
	case "string":
		return s, nil
	case "integer":
		s = strings.TrimSpace(s)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return int64(u), nil
		}
		return int64(0), nil
	case "real":
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, nil
	case "date":
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
		if err != nil {
			return time.Time{}, nil
		}
		return t, nil
	case "data":
		b, _ := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		return b, nil
	}
	return s, nil // unknown element: keep its text
}

// xmlText reads character data up to the end of the current element.
func xmlText(d *xml.Decoder) (string, error) {
	var sb strings.Builder
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 {
				return sb.String(), nil
			}
			depth--
		}
	}
}

// ---------------------------------------------------------------- binary

type bplist struct {
	data     []byte
	offsets  []uint64
	refSize  int
	visiting map[uint64]bool

	// Work budgets (see parseBinaryPlist): object references still allowed
	// to be followed, and string/data payload bytes still allowed to be copied.
	refsLeft  uint64
	bytesLeft uint64
	// strCache holds decoded strings and data by object offset, so a value
	// referenced many times (Apple's writer stores equal strings once) is
	// decoded and charged once. Cached []byte values are shared: read only.
	strCache map[uint64]any
}

// appleEpoch is the reference date of binary plist dates (2001-01-01 UTC).
var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

func parseBinaryPlist(data []byte) (any, error) {
	if len(data) < 8+32 {
		return nil, errors.New("bplist: too short")
	}
	tr := data[len(data)-32:]
	offSize := int(tr[6])
	refSize := int(tr[7])
	numObjects := binary.BigEndian.Uint64(tr[8:16])
	top := binary.BigEndian.Uint64(tr[16:24])
	tableOff := binary.BigEndian.Uint64(tr[24:32])
	if offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8 {
		return nil, errors.New("bplist: bad trailer")
	}
	if numObjects == 0 || top >= numObjects || numObjects > uint64(len(data)) {
		return nil, errors.New("bplist: bad object count")
	}
	end := tableOff + numObjects*uint64(offSize)
	if tableOff < 8 || end > uint64(len(data)-32) || end < tableOff {
		return nil, errors.New("bplist: bad offset table")
	}
	// Objects are decoded once per reference, so a crafted file can make a
	// tiny input expand exponentially (arrays each referencing the next one
	// twice: 130 bytes already take seconds, 200 bytes never finish and end
	// in an out-of-memory crash that recover cannot catch). Apple's writer
	// never shares containers and each reference occupies refSize bytes of
	// its own, so a genuine file follows at most len/refSize references and
	// copies at most len payload bytes (equal strings are stored once and
	// cached below): these budgets never reject a well-formed file.
	p := &bplist{data: data, refSize: refSize, visiting: map[uint64]bool{},
		refsLeft:  uint64(len(data))/uint64(refSize) + 16,
		bytesLeft: uint64(len(data)),
		strCache:  map[uint64]any{},
	}
	p.offsets = make([]uint64, numObjects)
	for i := uint64(0); i < numObjects; i++ {
		o := tableOff + i*uint64(offSize)
		p.offsets[i] = readUint(data[o : o+uint64(offSize)])
		if p.offsets[i] >= tableOff {
			return nil, errors.New("bplist: object offset out of range")
		}
	}
	return p.object(top, 0)
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// span returns data[off:off+n] or an error when out of bounds.
func (p *bplist) span(off, n uint64) ([]byte, error) {
	if n > uint64(len(p.data)) || off > uint64(len(p.data))-n {
		return nil, errors.New("bplist: truncated object")
	}
	return p.data[off : off+n], nil
}

// spanN returns the n elements of size bytes starting at off. n is checked
// before multiplying: a corrupted length (up to 2^64-1) must not wrap n*size
// around to a small value that passes the bounds check.
func (p *bplist) spanN(off, n, size uint64) ([]byte, error) {
	if size == 0 || n > uint64(len(p.data))/size {
		return nil, errors.New("bplist: truncated object")
	}
	return p.span(off, n*size)
}

// count decodes the length of a data/string/array/dict object starting at
// off (marker included) and returns it with the offset of its payload.
func (p *bplist) count(off uint64, info byte) (uint64, uint64, error) {
	if info != 0x0F {
		return uint64(info), off + 1, nil
	}
	hdr, err := p.span(off+1, 1)
	if err != nil {
		return 0, 0, err
	}
	if hdr[0]>>4 != 0x1 {
		return 0, 0, errors.New("bplist: bad length marker")
	}
	n := uint64(1) << (hdr[0] & 0x0F)
	if n > 8 {
		return 0, 0, errors.New("bplist: length too large")
	}
	b, err := p.span(off+2, n)
	if err != nil {
		return 0, 0, err
	}
	return readUint(b), off + 2 + n, nil
}

// refs reads the n object references of a container at off. They are
// charged to the reference budget before anything is allocated, so nested
// containers cannot each pre-allocate a huge slice either.
func (p *bplist) refs(off, n uint64) ([]uint64, error) {
	if n > p.refsLeft {
		return nil, errors.New("bplist: too many object references")
	}
	b, err := p.spanN(off, n, uint64(p.refSize))
	if err != nil {
		return nil, err
	}
	p.refsLeft -= n
	out := make([]uint64, n)
	for i := range out {
		out[i] = readUint(b[uint64(i)*uint64(p.refSize) : uint64(i+1)*uint64(p.refSize)])
	}
	return out, nil
}

func (p *bplist) object(ref uint64, depth int) (any, error) {
	if ref >= uint64(len(p.offsets)) {
		return nil, errors.New("bplist: bad object reference")
	}
	if depth > 64 || p.visiting[ref] {
		return nil, errors.New("bplist: cycle or nesting too deep")
	}
	off := p.offsets[ref]
	m, err := p.span(off, 1)
	if err != nil {
		return nil, err
	}
	typ, info := m[0]>>4, m[0]&0x0F
	switch typ {
	case 0x0:
		switch info {
		case 0x8:
			return false, nil
		case 0x9:
			return true, nil
		}
		return nil, nil
	case 0x1:
		n := uint64(1) << info
		if n > 16 {
			return nil, errors.New("bplist: bad integer")
		}
		b, err := p.span(off+1, n)
		if err != nil {
			return nil, err
		}
		if n == 16 { // 128-bit integers: keep the low 64 bits
			b = b[8:]
		}
		return int64(readUint(b)), nil
	case 0x2:
		n := uint64(1) << info
		b, err := p.span(off+1, n)
		if err != nil {
			return nil, err
		}
		switch n {
		case 4:
			return float64(math.Float32frombits(uint32(readUint(b)))), nil
		case 8:
			return math.Float64frombits(readUint(b)), nil
		}
		return nil, errors.New("bplist: bad real")
	case 0x3:
		b, err := p.span(off+1, 8)
		if err != nil {
			return nil, err
		}
		secs := math.Float64frombits(readUint(b))
		if math.IsNaN(secs) || math.IsInf(secs, 0) {
			return time.Time{}, nil
		}
		return appleEpoch.Add(time.Duration(secs * float64(time.Second))), nil
	case 0x4, 0x5, 0x6:
		if v, ok := p.strCache[off]; ok {
			return v, nil
		}
		n, start, err := p.count(off, info)
		if err != nil {
			return nil, err
		}
		size := uint64(1)
		if typ == 0x6 {
			size = 2
		}
		b, err := p.spanN(start, n, size)
		if err != nil {
			return nil, err
		}
		// Distinct objects may overlap in a crafted file: bound the copies.
		if uint64(len(b)) > p.bytesLeft {
			return nil, errors.New("bplist: too much string data")
		}
		p.bytesLeft -= uint64(len(b))
		var v any
		switch typ {
		case 0x6:
			u := make([]uint16, n)
			for i := range u {
				u[i] = binary.BigEndian.Uint16(b[i*2:])
			}
			v = string(utf16.Decode(u))
		case 0x5:
			v = string(b)
		default:
			v = append([]byte(nil), b...)
		}
		p.strCache[off] = v
		return v, nil
	case 0x8:
		b, err := p.span(off+1, uint64(info)+1)
		if err != nil {
			return nil, err
		}
		return int64(readUint(b)), nil
	case 0xA, 0xC: // array, set
		n, start, err := p.count(off, info)
		if err != nil {
			return nil, err
		}
		rs, err := p.refs(start, n)
		if err != nil {
			return nil, err
		}
		p.visiting[ref] = true
		defer delete(p.visiting, ref)
		a := make([]any, 0, len(rs))
		for _, r := range rs {
			v, err := p.object(r, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		return a, nil
	case 0xD:
		n, start, err := p.count(off, info)
		if err != nil {
			return nil, err
		}
		keys, err := p.refs(start, n)
		if err != nil {
			return nil, err
		}
		vals, err := p.refs(start+n*uint64(p.refSize), n)
		if err != nil {
			return nil, err
		}
		p.visiting[ref] = true
		defer delete(p.visiting, ref)
		d := make(map[string]any, n)
		for i := range keys {
			k, err := p.object(keys[i], depth+1)
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, errors.New("bplist: non-string dictionary key")
			}
			v, err := p.object(vals[i], depth+1)
			if err != nil {
				return nil, err
			}
			d[ks] = v
		}
		return d, nil
	}
	return nil, fmt.Errorf("bplist: unknown object type 0x%x", typ)
}
