package system

import (
	"encoding/binary"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanio"
	"golang.org/x/sys/unix"
)

// downloadsMinAge: installers / archives untouched for this long are proposed.
const downloadsMinAge = 30 * 24 * time.Hour

// downloadsMaxDepth bounds the walk below ~/Downloads.
const downloadsMaxDepth = 3

type dlFamily struct {
	kind, name, note string
	exts             []string
}

var dlFamilies = []dlFamily{
	{"downloads-installers", "Old installers & disk images in Downloads",
		"Disk images and installer packages downloaded more than 30 days ago; once the app is installed they are only needed to reinstall it (download them again).",
		[]string{".dmg", ".pkg", ".mpkg", ".iso", ".xip"}},
	{"downloads-archives", "Old archives in Downloads",
		"Compressed archives downloaded more than 30 days ago; check that their content was extracted or is not needed anymore — they are not regenerated.",
		[]string{".zip", ".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".tbz2", ".tar", ".rar", ".7z"}},
	{"downloads-mobile-builds", "Old app builds in Downloads (.ipa/.apk/.aab)",
		"Mobile app binaries (CI artifacts, store builds) downloaded more than 30 days ago; download or rebuild them again if needed.",
		[]string{".ipa", ".apk", ".aab", ".apks", ".xapk"}},
}

func dlFamilyOf(name string) int {
	n := strings.ToLower(name)
	for i, f := range dlFamilies {
		for _, x := range f.exts {
			if strings.HasSuffix(n, x) && len(n) > len(x) {
				return i
			}
		}
	}
	return -1
}

type dlFile struct {
	path string
	size int64
	used time.Time
}

// downloads groups, per file family, installers / archives / app builds in
// ~/Downloads that were not downloaded nor opened for 30 days. Loose files
// and files the user sorted into sub-folders are separate items. Caution:
// these are user files and are never preselected.
func (s *scan) downloads() {
	root := s.home("Downloads")
	if !isDir(root) {
		return
	}
	pl := s.locate(root)
	if !pl.Exists || pl.External {
		return
	}
	// groups[family][0] = loose files, [1] = files in sub-folders
	groups := make([][2][]dlFile, len(dlFamilies))
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if s.ctx.Err() != nil || s.env.Excluded(dir) {
			return
		}
		entries := list(s.ctx, dir, false)
		var candidates []string
		for _, e := range entries {
			if !e.link && !e.dir && dlFamilyOf(e.name) >= 0 {
				candidates = append(candidates, e.path)
			}
		}
		stats, err := scanio.UnixLstats(s.ctx, candidates)
		if err != nil {
			return
		}
		byPath := make(map[string]scanio.Stat, len(candidates))
		for i, p := range candidates {
			byPath[p] = stats[i]
		}
		for _, e := range entries {
			if e.link {
				continue
			}
			if e.dir {
				n := strings.ToLower(e.name)
				if depth < downloadsMaxDepth && !strings.HasSuffix(n, ".app") && n != "node_modules" &&
					!strings.HasSuffix(n, ".xcarchive") && !strings.HasSuffix(n, ".photoslibrary") {
					walk(e.path, depth+1)
				}
				continue
			}
			fam := dlFamilyOf(e.name)
			if fam < 0 {
				continue
			}
			info := byPath[e.path]
			st := info.File
			if info.Err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
				continue
			}
			alloc := st.Blocks * 512
			if alloc == 0 {
				continue // iCloud "dataless" placeholder: deleting it frees nothing here
			}
			// Age = the most recent of: last modification, last opened
			// (Finder) and the time the file arrived here. An archive
			// extracted today, `curl -R`, AirDrop or a copy preserving times
			// all leave an old mtime on a file added today; the arrival time
			// is its ctime (creation or rename into the folder both set it).
			used := time.Unix(st.Mtim.Sec, st.Mtim.Nsec)
			if t, ok := s.p.lastUsed(e.path); ok && t.After(used) {
				used = t
			}
			if t := s.p.added(e.path, &st); t.After(used) {
				used = t
			}
			if s.now.Sub(used) < downloadsMinAge {
				continue
			}
			if s.env.Excluded(e.path) || s.env.IsProtectedContext(s.ctx, e.path) {
				continue
			}
			sub := 0
			if depth > 1 {
				sub = 1
			}
			groups[fam][sub] = append(groups[fam][sub], dlFile{path: e.path, size: alloc, used: used})
		}
	}
	walk(root, 1)

	for i, byPlace := range groups {
		for sub, files := range byPlace {
			if len(files) == 0 {
				continue
			}
			f := dlFamilies[i]
			name, where := f.name, root
			if sub == 1 {
				name += " sub-folders"
				where = root + "/*/"
			}
			it := s.newItem(f.kind, core.CatSystem, name+" ("+strconv.Itoa(len(files))+", > 30d)", core.RiskCaution)
			it.ID = itemID(f.kind, where)
			it.Location = root + "/…"
			it.Note = f.note
			if sub == 1 {
				it.Note += " These were sorted into sub-folders: check them one by one."
			}
			sort.Slice(files, func(a, b int) bool { return files[a].size > files[b].size })
			var top []string
			for j, df := range files {
				it.Paths = append(it.Paths, df.path)
				it.Size += df.size
				it.LastUsed = maxTime(it.LastUsed, df.used)
				if j < 5 {
					rel, _ := filepath.Rel(root, df.path)
					top = append(top, rel+" ("+fsx.Bytes(df.size)+")")
				}
			}
			it.Files = int64(len(files))
			sort.Strings(it.Paths)
			it.Meta = map[string]string{"files": strconv.Itoa(len(files)), "largest": strings.Join(top, ", ")}
			s.emitNow(it)
		}
	}
}

// fileAdded returns when a file arrived at its current place: its ctime,
// which creation and rename set (utimes cannot move it back). It is always at
// least the APFS "date added" (ATTR_CMN_ADDEDTIME); it also moves on
// metadata changes (xattr, chmod), which only makes a file look younger.
func fileAdded(_ string, st *unix.Stat_t) time.Time {
	return time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
}

// finderLastUsed returns the "Last opened" date macOS records on a file
// (xattr com.apple.lastuseddate#PS, a struct timespec).
func finderLastUsed(path string) (time.Time, bool) {
	buf := make([]byte, 16)
	n, err := unix.Getxattr(path, "com.apple.lastuseddate#PS", buf)
	if err != nil || n < 8 {
		return time.Time{}, false
	}
	sec := int64(binary.LittleEndian.Uint64(buf[0:8]))
	var nsec int64
	if n >= 16 {
		nsec = int64(binary.LittleEndian.Uint64(buf[8:16]))
	}
	if sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, nsec), true
}
