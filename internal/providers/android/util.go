package android

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func sortStrings(s []string) { sort.Strings(s) }

// versionParts splits "27.1.12297006", "android-34", "35.0.0-rc3" into
// comparable numbers (non numeric chunks are ignored).
func versionParts(v string) []int {
	var out []int
	cur, in := 0, false
	for _, r := range v {
		if r >= '0' && r <= '9' {
			cur = cur*10 + int(r-'0')
			in = true
			continue
		}
		if in {
			out = append(out, cur)
			cur, in = 0, false
		}
	}
	if in {
		out = append(out, cur)
	}
	return out
}

// compareVersions compares two version strings numerically.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}

// newest returns the n greatest versions of vs.
func newest(vs []string, n int) map[string]bool {
	c := append([]string(nil), vs...)
	sort.Slice(c, func(i, j int) bool { return compareVersions(c[i], c[j]) > 0 })
	out := map[string]bool{}
	for i := 0; i < n && i < len(c); i++ {
		out[c[i]] = true
	}
	return out
}

// mtime returns the modification time of p (zero if missing).
func mtime(p string) time.Time {
	fi, err := os.Lstat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// newestMtime returns the newest mtime among the files matching glob.
func newestMtime(glob string) time.Time {
	var t time.Time
	ms, _ := filepath.Glob(glob)
	for _, m := range ms {
		if mt := mtime(m); mt.After(t) {
			t = mt
		}
	}
	return t
}

func maxTime(ts ...time.Time) time.Time {
	var t time.Time
	for _, x := range ts {
		if x.After(t) {
			t = x
		}
	}
	return t
}

// projectList formats up to 4 project paths for Meta/Note.
func (s *scan) projectList(projs []string) string {
	sort.Strings(projs)
	var out []string
	for i, p := range projs {
		if i == 4 {
			out = append(out, "+"+strconv.Itoa(len(projs)-4)+" more")
			break
		}
		out = append(out, s.env.Pretty(p))
	}
	return strings.Join(out, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
