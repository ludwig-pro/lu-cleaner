package fsx

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// FoldPath returns a comparison key for p that follows how a default APFS
// volume looks names up: case-insensitively and normalization-insensitively
// ("Café" typed in NFC and "café" from a Finder-created NFD name are the
// same directory). Use it for protection, exclusion and "in use" checks, where
// a missed match means deleting something that should have been kept. The
// key is only for comparisons: never open or print it.
//
// Folding may merge names that are distinct on a case-sensitive volume; for
// the checks above that errs on the safe side (more is protected / busy).
func FoldPath(p string) string {
	if isASCII(p) {
		return strings.ToLower(p) // no allocation when already lower-case
	}
	// Lower-case first (it keeps combining marks), then compose: NFD "É"
	// and NFC "É" both end up as NFC "é".
	return norm.NFC.String(strings.ToLower(norm.NFC.String(p)))
}

// SamePath reports whether a and b name the same path on a case- and
// normalization-insensitive volume.
func SamePath(a, b string) bool {
	return a == b || FoldPath(a) == FoldPath(b)
}

// WithinFold is Within on folded paths: p equals parent or is below it,
// ignoring case and Unicode normalization.
func WithinFold(p, parent string) bool {
	return Within(p, parent) || Within(FoldPath(p), FoldPath(parent))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
