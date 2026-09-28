package android

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// place says where a path really lives, relative to the home volume.
type place int

const (
	placeAbsent   place = iota // does not exist (and no symlink / volume involved)
	placeInternal              // exists, inside the home folder, on the home volume
	placeOutside               // exists on the home volume but outside the home folder (/Library, /opt...)
	placeExternal              // exists on another volume (external SSD...)
	placeMissing               // behind a dangling symlink or on an unmounted volume
)

func (p place) String() string {
	return [...]string{"absent", "internal", "outside-home", "external", "missing"}[p]
}

// location is the result of locate.
type location struct {
	Path   string // as given
	Real   string // resolved path (best effort when missing)
	Place  place
	IsLink bool   // Path itself is a symbolic link
	Volume string // "/Volumes/<name>" when the real path lives under /Volumes
	Ghost  bool   // Volume is a plain folder on the internal disk, not a mount point
}

// Deletable reports whether items at this location may be deleted.
func (l location) Deletable() bool { return l.Place == placeInternal && !l.IsLink }

// Exists reports whether the real location can be read now.
func (l location) Exists() bool {
	return l.Place == placeInternal || l.Place == placeOutside || l.Place == placeExternal
}

// volumeOf returns "/Volumes/<name>" for a path below /Volumes.
func volumeOf(p string) string {
	for _, prefix := range []string{"/Volumes/", "/System/Volumes/Data/Volumes/"} {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				rest = rest[:i]
			}
			if rest != "" {
				return "/Volumes/" + rest
			}
		}
	}
	return ""
}

// resolvePartial resolves symlinks component by component as far as the
// filesystem allows. It returns the resolved path (the tail that could not be
// resolved is appended lexically), whether it exists, and whether resolution
// stopped on a symlink whose target is missing (a dangling link, as opposed
// to a plain missing file below valid links such as /var -> /private/var).
func resolvePartial(p string) (resolved string, exists, dangling bool) {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, true, false
	}
	return resolveFrom("/", strings.Split(strings.TrimPrefix(p, "/"), "/"), 0)
}

func resolveFrom(cur string, parts []string, hops int) (string, bool, bool) {
	rest := func(i int) string { return filepath.Join(append([]string{cur}, parts[i:]...)...) }
	for i := 0; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		next := filepath.Join(cur, parts[i])
		fi, err := os.Lstat(next)
		if err != nil {
			return rest(i), false, false
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(next)
			if err != nil || hops > 40 {
				return rest(i), false, true
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(cur, target)
			}
			rt, ok, _ := resolveFrom("/", strings.Split(strings.TrimPrefix(filepath.Clean(target), "/"), "/"), hops+1)
			if !ok {
				return filepath.Join(append([]string{rt}, parts[i+1:]...)...), false, true
			}
			cur = rt
			continue
		}
		cur = next
	}
	return cur, true, false
}

// locate classifies p. Nothing is followed on disk beyond reading links.
func (s *scan) locate(p string) location {
	l := location{Path: p}
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		l.IsLink = true
	}
	real, exists, dangling := resolvePartial(p)
	l.Real = real
	l.Volume = volumeOf(real)
	if !exists {
		switch {
		case l.Volume != "" && !s.mounted(l.Volume):
			l.Place = placeMissing
		case dangling:
			l.Place = placeMissing
		default:
			l.Place = placeAbsent
		}
		return l
	}
	dev, err := s.p.devOf(real)
	if err != nil {
		l.Place = placeMissing
		return l
	}
	if l.Volume != "" && !s.mounted(l.Volume) {
		// A plain /Volumes/<name> folder: sudo tools may create it while the
		// disk is unplugged. Its content lives on the internal disk.
		l.Ghost = true
	}
	switch {
	case dev != s.homeDev:
		l.Place = placeExternal
	case fsx.Within(real, s.homeReal):
		l.Place = placeInternal
	default:
		l.Place = placeOutside
	}
	return l
}

// mounted reports whether vol ("/Volumes/<name>") is a mount point: it exists
// and its device differs from its parent's.
func (s *scan) mounted(vol string) bool {
	d, err := s.p.devOf(vol)
	if err != nil {
		return false
	}
	pd, err := s.p.devOf(filepath.Dir(vol))
	if err != nil {
		return true
	}
	return d != pd
}

// placeWarn is the warning shown on items that cannot be cleaned because of
// where they live.
func (s *scan) placeWarn(l location) string {
	switch {
	case l.Place == placeExternal:
		return "on external volume — no internal gain"
	case l.Place == placeMissing && l.Volume != "":
		return "on " + l.Volume + ", which is not mounted — nothing to clean here (link kept)"
	case l.Place == placeMissing:
		return "dangling symlink to " + l.Real + " — nothing to clean here (link kept)"
	case l.Ghost:
		return l.Volume + " is a plain folder on the internal disk, not a mounted volume — check it by hand"
	case l.Place == placeOutside:
		return "outside your home folder — remove it by hand if unused"
	case l.IsLink:
		return "symlink to " + l.Real + " — the link is never removed"
	}
	return ""
}
