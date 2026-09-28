package fsx

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Bytes formats n with decimal units (like Finder / `df -H`): 1.2 GB, 340 MB.
func Bytes(n int64) string {
	if n < 0 {
		return "-" + Bytes(-n)
	}
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	if v >= 10 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

// ParseBytes parses "500MB", "1.5G", "2GiB", "1024" (bytes). Decimal units by
// default, binary with an "i" (KiB, MiB, GiB).
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	num := s
	mult := 1.0
	suffixes := []struct {
		s string
		m float64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
		{"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"B", 1},
	}
	for _, x := range suffixes {
		if strings.HasSuffix(s, x.s) {
			num = strings.TrimSpace(strings.TrimSuffix(s, x.s))
			mult = x.m
			break
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	// v*mult >= 2^63 would overflow int64 (and wrap to a negative size).
	if err != nil || !(v >= 0) || math.IsInf(v, 0) || v*mult >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q (examples: 500MB, 1.5GB, 2GiB)", s)
	}
	return int64(v * mult), nil
}

// ParseAge parses "30d", "2w", "6m" (months), "1y", "12h", "0". A bare number means days.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	var mult time.Duration
	switch unit {
	case 'h':
		mult = time.Hour
	case 'd':
		mult = 24 * time.Hour
	case 'w':
		mult = 7 * 24 * time.Hour
	case 'm':
		mult = 30 * 24 * time.Hour
	case 'y':
		mult = 365 * 24 * time.Hour
	default:
		num = s
		mult = 24 * time.Hour
	}
	v, err := strconv.ParseFloat(num, 64)
	// NaN (!(v >= 0)) and values beyond time.Duration's range would convert
	// to 0 or a negative duration, silently disabling the age filter.
	if err != nil || !(v >= 0) || math.IsInf(v, 0) || v*float64(mult) >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid age %q (examples: 7d, 2w, 3m, 1y)", s)
	}
	return time.Duration(v * float64(mult)), nil
}

// Age formats a duration compactly: "now", "5h", "3d", "6w", "4mo", "2y".
func Age(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Hour:
		return "now"
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%.1fy", d.Hours()/24/365)
	}
}
