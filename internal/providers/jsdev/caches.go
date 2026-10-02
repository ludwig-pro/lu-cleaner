package jsdev

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// ------------------------------------------------------------------ Expo Go

var expoGoName = regexp.MustCompile(`^Expo-Go-([0-9]+)\.([0-9]+)\.([0-9]+)`)

// expoMinSDK: Expo Go builds numbered like the SDK (56.0.3 = SDK 56); older
// builds (2.33.x) cannot be mapped to an SDK.
const expoMinSDK = 40

// expoGo emits the Expo Go iOS simulator builds cached by `expo start --ios`
// (~/.expo/ios-simulator-app-cache). A build superseded by a newer one of the
// same major (SDK) is safe and recommended; the newest per SDK is moderate.
// warmExpoGo measures the Expo Go builds while the project walk that finds
// the Expo SDKs in use runs.
func (s *scanner) warmExpoGo() {
	root := s.home(".expo", "ios-simulator-app-cache")
	var paths []string
	for _, e := range listDir(s.ctx, root) {
		if p := filepath.Join(root, e.Name()); e.IsDir() && expoGoName.MatchString(e.Name()) && s.allowed(p) {
			paths = append(paths, p)
		}
	}
	s.warm(paths)
}

func (s *scanner) expoGo() {
	root := s.home(".expo", "ios-simulator-app-cache")
	type build struct {
		path string
		ver  version
	}
	byMajor := map[int][]build{}
	for _, e := range listDir(s.ctx, root) {
		m := expoGoName.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		if !s.allowed(p) {
			continue
		}
		v, ok := parseVersion(m[1] + "." + m[2] + "." + m[3])
		if !ok {
			continue
		}
		byMajor[v.Major] = append(byMajor[v.Major], build{p, v})
	}
	for _, builds := range byMajor {
		sort.Slice(builds, func(i, j int) bool { return builds[j].ver.less(builds[i].ver) }) // newest first
		for i, b := range builds {
			if s.ctx.Err() != nil {
				return
			}
			it := s.base("expo-go-ios", b.path, "Expo Go "+strings.TrimPrefix(b.ver.String(), "v")+" (iOS simulator build)", core.RiskSafe)
			it.Path = b.path
			it.LastUsed = newestMtime(b.path)
			if i == 0 {
				it.Risk = core.RiskModerate
				it.Note = "Newest cached Expo Go " + itoa(b.ver.Major) + ".x; `expo start --ios` downloads it again (~250 MB) when a project needs it."
				if users := s.projects.expoSDK[b.ver.Major]; b.ver.Major >= expoMinSDK && len(users) > 0 {
					it.Warn = "Expo SDK " + itoa(b.ver.Major) + " is used by " + itoa(len(users)) + " project(s)"
					it.Meta["used_by"] = joinLimit(prettyAll(s.env, users), 3)
				}
			} else {
				it.Recommended = true
				it.Note = "Superseded by Expo Go " + strings.TrimPrefix(builds[0].ver.String(), "v") + " (same SDK); re-downloaded on demand."
				it.Meta["superseded_by"] = strings.TrimPrefix(builds[0].ver.String(), "v")
			}
			s.sized(it, []string{b.path}, false)
		}
	}
}

// ------------------------------------------------------------------ Playwright

var playwrightRev = regexp.MustCompile(`^([a-z][a-z0-9_]*)-([0-9]+)$`)

// playwright emits one item per browser revision in ms-playwright. A revision
// that no installed playwright-core (.links) needs any more is recommended.
func (s *scanner) playwright() {
	roots := []string{s.home("Library", "Caches", "ms-playwright")}
	if v := s.getenv("PLAYWRIGHT_BROWSERS_PATH"); v != "" && v != "0" {
		roots = append([]string{s.envDir("PLAYWRIGHT_BROWSERS_PATH")}, roots...)
	}
	for _, root := range uniqDirs(roots...) {
		needed, links, haveLinks := playwrightNeeded(s.ctx, root)
		type rev struct {
			path, family string
			n            int
		}
		var revs []rev
		newest := map[string]int{}
		for _, e := range listDir(s.ctx, root) {
			m := playwrightRev.FindStringSubmatch(e.Name())
			if m == nil || !e.IsDir() {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			revs = append(revs, rev{filepath.Join(root, e.Name()), m[1], n})
			if n > newest[m[1]] {
				newest[m[1]] = n
			}
		}
		for _, r := range revs {
			if s.ctx.Err() != nil {
				return
			}
			if !s.allowed(r.path) {
				continue
			}
			name := "Playwright " + strings.ReplaceAll(r.family, "_", " ") + " r" + itoa(r.n)
			it := s.base("playwright-browser", r.path, name, core.RiskModerate)
			it.Path = r.path
			it.LastUsed = newestMtime(r.path)
			base := filepath.Base(r.path)
			switch {
			case haveLinks && len(needed[base]) > 0:
				it.Note = "Browser used by an installed Playwright; `npx playwright install` downloads it again."
				it.Meta["used_by"] = joinLimit(prettyAll(s.env, needed[base]), 3)
				it.Warn = "needed by " + itoa(len(needed[base])) + " installed Playwright(s)"
			case haveLinks:
				it.Recommended = true
				it.Note = "No installed Playwright needs this revision any more (checked " + itoa(links) + " install(s) in .links); `npx playwright install` brings it back if needed."
			case r.n == newest[r.family]:
				it.Note = "Newest " + r.family + " revision; `npx playwright install` downloads it again."
			default:
				it.Recommended = true
				it.Note = "Older " + r.family + " revision (a newer one is installed); `npx playwright install` brings it back if needed."
			}
			if n := s.procs.usesDir(r.path); n > 0 {
				it.Warn = "browser running (" + itoa(n) + " process(es))"
				it.Recommended = false
			}
			s.sized(it, []string{r.path}, false)
		}
	}
}

// playwrightNeeded reads <root>/.links: each file holds the path of a
// playwright-core install whose browsers.json lists the revisions it needs.
// It returns dir name -> installs needing it, the number of live installs,
// and whether the .links information is usable.
func playwrightNeeded(ctx context.Context, root string) (map[string][]string, int, bool) {
	linksDir := filepath.Join(root, ".links")
	ents, err := fsx.ReadDir(ctx, linksDir)
	if err != nil {
		return nil, 0, false
	}
	needed := map[string][]string{}
	live := 0
	for _, e := range ents {
		data, err := readSmall(filepath.Join(linksDir, e.Name()))
		if err != nil {
			continue
		}
		pkg := strings.TrimSpace(data)
		if !filepath.IsAbs(pkg) {
			continue
		}
		bj, err := readSmall(filepath.Join(pkg, "browsers.json"))
		if err != nil {
			continue // install gone: its revisions are no longer needed
		}
		var doc struct {
			Browsers []struct {
				Name              string            `json:"name"`
				Revision          string            `json:"revision"`
				RevisionOverrides map[string]string `json:"revisionOverrides"`
			} `json:"browsers"`
		}
		if json.Unmarshal([]byte(bj), &doc) != nil {
			return nil, 0, false // unknown format: fall back to "keep newest"
		}
		live++
		for _, b := range doc.Browsers {
			dir := strings.ReplaceAll(b.Name, "-", "_")
			revs := []string{b.Revision}
			for _, r := range b.RevisionOverrides {
				revs = append(revs, r)
			}
			for _, r := range revs {
				if r != "" {
					k := dir + "-" + r
					needed[k] = append(needed[k], pkg)
				}
			}
		}
	}
	return needed, live, true
}

// ------------------------------------------------------------------ $TMPDIR signatures

const tmpMinAge = 2 * time.Hour

var (
	codegenName = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)[A-Za-z0-9]{6}$`)
	nanoidName  = regexp.MustCompile(`^[A-Za-z0-9_-]{21}$`)
)

// tmpSignatures finds orphan temp dirs that need a content check (names alone
// are too generic): React Native codegen outputs and Vitest module-runner dirs.
func (s *scanner) tmpSignatures() {
	tmp := s.env.TmpDir
	if tmp == "" {
		return
	}
	var codegen, vitest []string
	var codegenNewest, vitestNewest time.Time
	for _, e := range listDir(s.ctx, tmp) {
		if s.ctx.Err() != nil {
			return
		}
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		p := filepath.Join(tmp, name)
		isCodegen := codegenName.MatchString(name) && isCodegenDir(s.ctx, p, name)
		if !isCodegen && !(nanoidName.MatchString(name) && isVitestDir(s.ctx, p)) {
			continue
		}
		mt := newestMtime(p)
		if s.env.Now.Sub(mt) < tmpMinAge || !s.allowed(p) {
			continue
		}
		if isCodegen {
			codegen = append(codegen, p)
			if mt.After(codegenNewest) {
				codegenNewest = mt
			}
		} else {
			vitest = append(vitest, p)
			if mt.After(vitestNewest) {
				vitestNewest = mt
			}
		}
	}
	emitGroup := func(kind, name, note string, paths []string, newest time.Time) {
		if len(paths) == 0 {
			return
		}
		it := s.base(kind, "$TMPDIR", name+" ("+itoa(len(paths))+")", core.RiskSafe)
		if len(paths) == 1 {
			it.Path = paths[0]
		} else {
			it.Paths = paths
			it.Location = s.env.Pretty(tmp) + "/…"
		}
		it.LastUsed = newest
		it.Recommended = true
		it.Note = note
		s.sized(it, paths, false)
	}
	emitGroup("rn-codegen-tmp", "React Native codegen temp dirs",
		"Output dirs of React Native codegen (pod install / Gradle), created per run and never removed.", codegen, codegenNewest)
	emitGroup("vitest-tmp", "Vitest module-runner temp dirs",
		"Transformed modules written by Vitest/Vite in $TMPDIR, recreated on each run and never removed.", vitest, vitestNewest)
}

// isCodegenDir: <lib><6 random chars>/out containing <lib>JSI.h or react/renderer.
func isCodegenDir(ctx context.Context, p, name string) bool {
	ents := listDir(ctx, p)
	if len(ents) != 1 || ents[0].Name() != "out" || !ents[0].IsDir() {
		return false
	}
	lib := name[:len(name)-6]
	out := filepath.Join(p, "out")
	if fi, err := fsx.Lstat(ctx, filepath.Join(out, lib+"JSI.h")); err == nil && fi.Mode().IsRegular() {
		return true
	}
	return isRealDir(filepath.Join(out, "react", "renderer"))
}

// isVitestDir: 21-char nanoid dir whose only children are client/ and/or ssr/.
func isVitestDir(ctx context.Context, p string) bool {
	ents := listDir(ctx, p)
	if len(ents) == 0 || len(ents) > 2 {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() || (e.Name() != "client" && e.Name() != "ssr") {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------ watchman

// watchman reports watches on directories that no longer exist (deleted
// worktrees). Frees memory and FSEvents load, not disk. `--no-spawn` never
// starts the server.
//
// Only the stale roots are removed (`watchman watch-del <root>`, once per
// root): `watch-del-all` would also cancel the subscriptions of a running
// Metro or `jest --watch`, which stop seeing file changes until restarted.
// watchman resolves a root that no longer exists by its exact watch-list
// string, so the roots are passed back verbatim.
func (s *scanner) watchman() {
	if !s.env.Has("watchman") {
		return
	}
	out, err := output(s.ctx, s.env, "", 3*time.Second, "watchman", "--no-spawn", "--no-pretty", "watch-list")
	if err != nil {
		return
	}
	var res struct {
		Roots []string `json:"roots"`
	}
	if json.Unmarshal(out, &res) != nil {
		return
	}
	var stale []string
	for _, r := range res.Roots {
		if filepath.IsAbs(r) && pathMissing(r) {
			stale = append(stale, r)
		}
	}
	if len(stale) == 0 {
		return
	}
	sort.Strings(stale)
	it := s.base("watchman-stale-watches", "watchman", "Watchman: "+itoa(len(stale))+" watch(es) on deleted dirs", core.RiskSafe)
	it.Method = core.MethodCommand
	it.Command = watchDel(stale[0])
	for _, r := range stale[1:] {
		it.PostCommands = append(it.PostCommands, watchDel(r))
	}
	it.Location = "watchman (" + itoa(len(res.Roots)) + " watches)"
	it.Recommended = true
	it.Note = "Removes only the watches whose directory no longer exists (`watchman watch-del`); live watches (running Metro, Jest) are untouched. Frees memory and CPU, not disk."
	it.Meta["stale_roots"] = joinLimit(prettyAll(s.env, stale), 5)
	// Re-check right before running: a root that exists again (worktree
	// re-created at the same path) may be watched by a live tool by now, and
	// a watch already dropped would make its watch-del fail (and stop the
	// following ones).
	env := s.env
	it.Recheck = func(ctx context.Context) error {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		out, err := env.Output(c, "", "watchman", "--no-spawn", "--no-pretty", "watch-list")
		if err != nil {
			return errors.New("cannot list watchman watches: " + err.Error())
		}
		var now struct {
			Roots []string `json:"roots"`
		}
		if err := json.Unmarshal(out, &now); err != nil {
			return errors.New("cannot read watchman watch-list: " + err.Error())
		}
		listed := map[string]bool{}
		for _, r := range now.Roots {
			listed[r] = true
		}
		for _, r := range stale {
			if !listed[r] {
				return errors.New("watch on " + r + " is already gone — rescan")
			}
			if !pathMissing(r) {
				return errors.New(r + " exists again — rescan")
			}
		}
		return nil
	}
	s.emit(it)
}

// watchDel is the command removing one watch without ever starting the server.
func watchDel(root string) []string {
	return []string{"watchman", "--no-spawn", "watch-del", root}
}
