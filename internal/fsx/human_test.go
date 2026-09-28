package fsx

import (
	"testing"
	"time"
)

// NaN, Inf and out-of-range ages used to convert to 0 (or a negative
// duration), which silently disabled --older-than and made every moderate
// item "stale" through the config's stale_after.
func TestParseAgeRejectsNaNAndOverflow(t *testing.T) {
	for _, s := range []string{"nan", "NaN", "NaNd", "nanh", "inf", "+Inf", "infd", "-inf", "infinity", "2000y", "1e300d", "300000y", "-1d", "d", "x"} {
		if d, err := ParseAge(s); err == nil {
			t.Errorf("ParseAge(%q) = %v, want an error", s, d)
		}
	}
	for s, want := range map[string]time.Duration{
		"":     0,
		"0":    0,
		"30":   30 * 24 * time.Hour,
		"30d":  30 * 24 * time.Hour,
		"2w":   14 * 24 * time.Hour,
		"12h":  12 * time.Hour,
		"1y":   365 * 24 * time.Hour,
		"1.5d": 36 * time.Hour,
		"200y": 200 * 365 * 24 * time.Hour,
	} {
		if d, err := ParseAge(s); err != nil || d != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", s, d, err, want)
		}
	}
}

func TestParseBytesRejectsOverflow(t *testing.T) {
	for _, s := range []string{"nan", "inf", "1e30", "10000000TB", "9223372036854775808", "-1", "big"} {
		if n, err := ParseBytes(s); err == nil {
			t.Errorf("ParseBytes(%q) = %d, want an error", s, n)
		}
	}
	for s, want := range map[string]int64{"500MB": 500e6, "2GiB": 2 << 30, "1024": 1024, "0": 0, "1.5G": 1.5e9, "8000000TB": 8e18} {
		if n, err := ParseBytes(s); err != nil || n != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", s, n, err, want)
		}
	}
}

func TestFoldPath(t *testing.T) {
	same := [][2]string{
		{"/Users/me/Dev/App", "/users/me/dev/app"},
		{"/Users/me/Café", "/Users/me/Café"}, // NFC vs NFD
		{"/Users/me/CAFÉ", "/users/me/café"}, // case + normalization
		{"/Users/me/Ünïcode", "/Users/me/Ünïcode"},
	}
	for _, c := range same {
		if !SamePath(c[0], c[1]) {
			t.Errorf("SamePath(%q, %q) = false", c[0], c[1])
		}
		if !WithinFold(c[1]+"/node_modules", c[0]) {
			t.Errorf("WithinFold(%q/node_modules, %q) = false", c[1], c[0])
		}
	}
	for _, c := range [][2]string{{"/a/app", "/a/app2"}, {"/a/cafe", "/a/café"}} {
		if SamePath(c[0], c[1]) {
			t.Errorf("SamePath(%q, %q) = true", c[0], c[1])
		}
	}
	if WithinFold("/a/App-web", "/a/app") {
		t.Error("sibling prefix matched")
	}
}
