package apple

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

type archive struct {
	path    string
	name    string
	bundle  string
	version string
	build   string
	team    string
	scheme  string
	created time.Time
}

// archives emits one item per .xcarchive (Xcode › Organizer). They hold the
// shipped binary and its dSYMs: caution, never recommended.
func (s *scan) archives() {
	root := s.lib("Developer", "Xcode", "Archives")
	var found []archive
	collect := func(p string) {
		if s.skipPath(p) {
			return
		}
		found = append(found, readArchive(p))
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		p := filepath.Join(root, e.Name())
		if !e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".xcarchive") {
			collect(p)
			continue
		}
		sub, err := os.ReadDir(p) // date folders: Archives/2026-09-28/*.xcarchive
		if err != nil {
			continue
		}
		for _, a := range sub {
			if a.IsDir() && strings.HasSuffix(a.Name(), ".xcarchive") {
				collect(filepath.Join(p, a.Name()))
			}
		}
	}

	// The newest archive of each app is the one current crashes come from.
	latest := map[string]string{}
	newest := map[string]time.Time{}
	for _, a := range found {
		k := a.bundle
		if k == "" {
			k = a.name
		}
		if a.created.After(newest[k]) || latest[k] == "" {
			latest[k], newest[k] = a.path, a.created
		}
	}

	for _, a := range found {
		it := s.item("xcode-archive", core.CatXcode, a.path)
		it.Path = a.path
		it.Name = a.name
		if a.version != "" {
			it.Name += " " + a.version
			if a.build != "" {
				it.Name += " (" + a.build + ")"
			}
		}
		it.Risk = core.RiskCaution
		it.Method = core.MethodTrash
		it.ProcessGuard = []string{"Xcode"}
		it.LastUsed = a.created
		it.Note = "App archive (binary + dSYMs) needed to symbolicate crash reports of this build unless the dSYMs were uploaded (App Store Connect, Sentry, Crashlytics); it cannot be rebuilt identically. Moved to the Trash."
		setMeta(it, "bundle_id", a.bundle)
		setMeta(it, "version", a.version)
		setMeta(it, "build", a.build)
		setMeta(it, "team", a.team)
		setMeta(it, "scheme", a.scheme)
		k := a.bundle
		if k == "" {
			k = a.name
		}
		if latest[k] == a.path {
			setMeta(it, "latest", "true")
			addWarn(it, "latest archive of this app — keep it (or its dSYMs) to symbolicate crashes")
		}
		if !s.applyPlace(it, a.path) {
			continue
		}
		s.guardWarn(it)
		s.sizeLater(it, []string{a.path}, nil)
	}
}

func readArchive(p string) archive {
	a := archive{path: p, name: strings.TrimSuffix(filepath.Base(p), ".xcarchive")}
	info, err := readPlistDict(filepath.Join(p, "Info.plist"))
	if err == nil {
		if n := pString(info, "Name"); n != "" {
			a.name = n
		}
		a.scheme = pString(info, "SchemeName")
		a.created = pTime(info, "CreationDate")
		if app := pDict(info, "ApplicationProperties"); app != nil {
			a.bundle = pString(app, "CFBundleIdentifier")
			a.version = pString(app, "CFBundleShortVersionString")
			a.build = pString(app, "CFBundleVersion")
			a.team = pString(app, "Team")
		}
	}
	if a.created.IsZero() {
		a.created = childrenNewest(p)
	}
	if a.name == "" {
		a.name = fmt.Sprintf("Archive %s", filepath.Base(p))
	}
	return a
}
