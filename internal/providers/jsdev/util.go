package jsdev

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
)

const externalWarn = "on external volume — no internal gain"

// scanner holds the state of one Scan call.
type scanner struct {
	ctx  context.Context
	env  *core.Env
	emit core.Emit
	p    *Provider

	homeDev   uint64
	homeDevOK bool

	procs    *procSnapshot
	projects *projectInfo
	shells   []*multishellDir
}

func (s *scanner) logf(format string, args ...any) {
	if s.env.Logf != nil {
		s.env.Logf(format, args...)
	}
}

func (s *scanner) getenv(k string) string {
	if s.p.getenv == nil {
		return ""
	}
	return s.p.getenv(k)
}

// home joins path elements below the home directory.
func (s *scanner) home(rel ...string) string {
	return filepath.Join(append([]string{s.env.Home}, rel...)...)
}

// envDir returns $name when it is an absolute path.
func (s *scanner) envDir(name string, rel ...string) string {
	v := strings.TrimSpace(s.getenv(name))
	if v == "" {
		return ""
	}
	v = s.env.Expand(v)
	if !filepath.IsAbs(v) {
		return ""
	}
	return filepath.Join(append([]string{v}, rel...)...)
}

// uniqDirs keeps existing directories, deduplicated by real path.
func uniqDirs(paths ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
			continue
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, p)
	}
	return out
}

// allowed reports whether p may be proposed at all: it exists and is neither
// excluded by the user nor protected by the safety guard.
func (s *scanner) allowed(p string) bool {
	if p == "" || !filepath.IsAbs(p) || s.env.Excluded(p) || s.env.IsProtected(p) {
		return false
	}
	_, err := os.Lstat(p)
	return err == nil
}

// devOf returns the device of the real location of p.
func (s *scanner) devOf(p string) (uint64, bool) {
	if s.p.devOf != nil {
		return s.p.devOf(p)
	}
	var st unix.Stat_t
	if err := unix.Stat(p, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// locate resolves the item path p. It returns the path to delete (the real
// directory when p itself is a symlink on the same volume), whether the real
// location is on another volume (report only), and ok=false for dangling
// symlinks and missing mounts (never proposed).
func (s *scanner) locate(p string) (target string, external, ok bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", false, false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false, false
	}
	target = p
	if fi.Mode()&os.ModeSymlink != 0 {
		target = real
		if !s.allowed(target) {
			return "", false, false
		}
	}
	if s.homeDevOK {
		dev, ok := s.devOf(real)
		if !ok {
			return "", false, false
		}
		if dev != s.homeDev {
			return p, true, true
		}
	}
	return target, false, true
}

// sized emits it as a placeholder (Sizing=true), measures paths and emits the
// final item (same ID). The first path decides the volume: an item living on
// another volume becomes report-only. useNewest merges the newest mtime of
// the tree into LastUsed (right for caches, wrong for installs). It returns
// the emitted final item (read-only) or nil.
func (s *scanner) sized(it *core.Item, paths []string, useNewest bool) *core.Item {
	if len(paths) == 0 || s.ctx.Err() != nil {
		return nil
	}
	target, external, ok := s.locate(paths[0])
	if !ok {
		return nil
	}
	measure := append([]string(nil), paths...)
	if external {
		real, _ := filepath.EvalSymlinks(paths[0])
		measure = []string{real}
		it.Method = core.MethodReport
		it.Command = nil
		it.Recommended = false
		it.Selectable = false
		it.Warn = externalWarn
		if it.Meta == nil {
			it.Meta = map[string]string{}
		}
		it.Meta["real_path"] = real
	} else if target != paths[0] {
		measure[0] = target
		if it.Path == paths[0] {
			it.Path = target
		} else if len(it.Paths) > 0 && it.Paths[0] == paths[0] {
			it.Paths[0] = target
		}
	}
	it.Sizing = true
	s.emit(it.Clone())

	var bytes, reclaim, files int64
	var newest time.Time
	for _, p := range measure {
		st, _ := fsx.Size(s.ctx, p, &fsx.Options{CrossDevice: external})
		bytes += st.Bytes
		reclaim += st.Reclaim
		files += st.Files
		if st.Newest.After(newest) {
			newest = st.Newest
		}
	}
	if s.ctx.Err() != nil {
		return nil
	}
	it.Sizing = false
	it.Size, it.Files = bytes, files
	it.SetReclaim(reclaim)
	if useNewest && newest.After(it.LastUsed) && !newest.After(s.env.Now.Add(time.Hour)) {
		it.LastUsed = newest
	}
	s.emit(it)
	return it
}

// base returns a new item of this provider.
func (s *scanner) base(kind, key, name string, risk core.Risk) *core.Item {
	return &core.Item{
		ID:         "js:" + kind + ":" + key,
		Provider:   s.p.ID(),
		Category:   core.CatJS,
		Kind:       kind,
		Name:       name,
		Risk:       risk,
		Method:     core.MethodDelete,
		Selectable: true,
		Meta:       map[string]string{},
	}
}

// newestMtime returns the newest mtime among paths (missing ones ignored).
func newestMtime(paths ...string) time.Time {
	return fsx.NewestOf(paths...)
}

// listDir returns the entries of dir sorted by name (nil when unreadable).
func listDir(dir string) []os.DirEntry {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return ents
}

// isRealDir reports whether p is a directory (not a symlink to one).
func isRealDir(p string) bool { return fsx.IsDir(p) }

// isDirOrLink reports whether p is a directory or a symlink to one (sized
// then resolves the link to the real directory, or skips it when dangling).
func isDirOrLink(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// globalPackages lists user-installed global npm packages in a lib/node_modules
// directory (npm and corepack, bundled with Node, are ignored).
func globalPackages(lib string) []string {
	var out []string
	for _, e := range listDir(lib) {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "npm" || name == "corepack" {
			continue
		}
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		if strings.HasPrefix(name, "@") {
			for _, se := range listDir(filepath.Join(lib, name)) {
				if !strings.HasPrefix(se.Name(), ".") {
					out = append(out, name+"/"+se.Name())
				}
			}
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// joinLimit joins at most n values, with "+k more" when truncated.
func joinLimit(vals []string, n int) string {
	if len(vals) <= n {
		return strings.Join(vals, ", ")
	}
	return strings.Join(vals[:n], ", ") + ", +" + itoa(len(vals)-n) + " more"
}

func itoa(n int) string { return strconv.Itoa(n) }
