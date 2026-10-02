package android

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

const daemonWarn = "Gradle daemon running — stop it first (./gradlew --stop or pkill -f GradleDaemon)"

// daemons describes the running Gradle daemons. They run as "java", so
// ProcessGuard cannot target them: items get a Warn instead.
type daemons struct {
	any      bool
	versions map[string]bool // Gradle versions of the running daemons
	unknown  bool            // a daemon runs whose version could not be read
}

func (d daemons) runs(ver string) bool { return d.versions[ver] || d.unknown }

var reDaemonVer = []*regexp.Regexp{
	regexp.MustCompile(`/gradle-(\d+\.\d+(?:\.\d+)?(?:-rc-\d+|-milestone-\d+)?)/lib/`),
	regexp.MustCompile(`gradle-(?:daemon-main|launcher)-(\d+\.\d+(?:\.\d+)?(?:-rc-\d+|-milestone-\d+)?)\.jar`),
}

func (s *scan) gradleDaemons() daemons {
	d := daemons{versions: map[string]bool{}}
	out, _ := s.env.OutputTimeout(s.ctx, 3*time.Second, "", "pgrep", "-lf", "GradleDaemon")
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "GradleDaemon") {
			continue
		}
		d.any = true
		found := false
		for _, re := range reDaemonVer {
			if m := re.FindStringSubmatch(line); m != nil {
				d.versions[m[1]] = true
				found = true
				break
			}
		}
		if !found {
			d.unknown = true
		}
	}
	return d
}

func (s *scan) warnDaemon(it *core.Item) {
	it.Warn = daemonWarn
	it.Recommended = false
}

// gradleHome returns GRADLE_USER_HOME (env, shell profile) or ~/.gradle.
func (s *scan) gradleHome() candidate {
	if c := s.varCandidates("GRADLE_USER_HOME", ""); len(c) > 0 {
		return c[0]
	}
	return candidate{filepath.Join(s.env.Home, ".gradle"), "default (~/.gradle)"}
}

// installedGradles returns Gradle versions installed globally (Homebrew,
// SDKMAN): their caches/<ver> are in use even without a wrapper.
func (s *scan) installedGradles() map[string][]string {
	out := map[string][]string{}
	for _, g := range []struct{ dir, who string }{
		{"/opt/homebrew/Cellar/gradle", "Homebrew gradle"},
		{"/usr/local/Cellar/gradle", "Homebrew gradle"},
		{filepath.Join(s.env.Home, ".sdkman", "candidates", "gradle"), "SDKMAN gradle"},
	} {
		for _, v := range dirNames(s.ctx, g.dir) {
			if v == "current" {
				continue
			}
			if i := strings.IndexByte(v, '_'); i > 0 { // Homebrew revision "8.12.1_1"
				v = v[:i]
			}
			out[v] = append(out[v], g.who)
		}
	}
	return out
}

func (s *scan) gradle(pi *projectInfo) {
	c := s.gradleHome()
	loc := s.locate(c.Path)
	if loc.Place == placeAbsent {
		return
	}
	if loc.Place != placeInternal {
		it := s.base("android-gradle-home", "Gradle user home", core.RiskModerate)
		it.Path = c.Path
		it.Warn = s.placeWarn(loc)
		it.Note = "Gradle user home stored outside the internal home volume; never cleaned from here."
		it.Meta["source"] = c.Source
		s.report(it)
		return
	}
	gh := c.Path
	d := s.gradleDaemons()
	used := pi.gradleVersions()
	for v, who := range s.installedGradles() {
		used[v] = append(used[v], who...)
	}
	s.gradleDists(gh, pi, d)
	s.gradleCaches(gh, pi, d, used)
	s.gradleDaemonLogs(gh, d)
	s.gradleTmp(gh)
	s.gradleJDKs(gh, d)
	s.gradleMisc(gh, d)
}

func daemonLogTime(ctx context.Context, gh, ver string) time.Time {
	return newestMtime(ctx, filepath.Join(gh, "daemon", ver, "daemon-*.out.log"))
}

func (s *scan) gradleDists(gh string, pi *projectInfo, d daemons) {
	dists := filepath.Join(gh, "wrapper", "dists")
	for _, name := range dirNames(s.ctx, dists) {
		m := reDistName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		ver, flavor := m[1], m[2]
		p := filepath.Join(dists, name)
		oks, _ := fsx.Glob(s.ctx, filepath.Join(p, "*", name+".zip.ok"))
		it := s.base("android-gradle-dist", "Gradle "+ver+" distribution ("+flavor+")", core.RiskModerate)
		it.Path = p
		// Daemon logs are the real usage signal; the download date is only a
		// fallback for distributions no project uses.
		it.LastUsed = daemonLogTime(s.ctx, gh, ver)
		it.Meta["version"] = ver
		it.Note = "Gradle wrapper distribution; ./gradlew re-downloads it (~130-200 MB) on the next build of a project that needs it."
		users := pi.wrappers[name]
		switch {
		case len(oks) == 0:
			it.LastUsed = maxTime(it.LastUsed, newestMtime(s.ctx, filepath.Join(p, "*", "*.part")), mtime(p))
			it.Risk = core.RiskSafe
			it.Name += " — interrupted download"
			it.Note = "Incomplete wrapper download (no .ok marker); ./gradlew starts the download over anyway."
			it.Recommended = true
			if part := newestMtime(s.ctx, filepath.Join(p, "*", "*.part")); !part.IsZero() && s.now().Sub(part) < time.Hour {
				it.Warn = "download may still be in progress"
				it.Recommended = false
			}
		case len(users) > 0:
			list := uniqSorted(users)
			it.Meta["used_by"] = s.projectList(list)
			it.Note += " Used by " + plural(len(list), "project", "projects") + "."
		case pi.known():
			it.Recommended = true
			it.LastUsed = maxTime(it.LastUsed, newestMtime(s.ctx, filepath.Join(p, "*", name+".zip.ok")))
		default:
			it.Meta["note"] = "no project roots scanned — usage unknown"
		}
		if d.runs(ver) {
			s.warnDaemon(it)
		}
		s.emitPkg(it, sizeOpt{})
	}
}

var (
	reCacheVer = regexp.MustCompile(`^\d+\.\d+(\.\d+)?(-rc-\d+|-milestone-\d+|-\d{14}\+\d{4})?$`)
	reCacheGen = regexp.MustCompile(`^(transforms|jars|build-cache)-(\d+)$`)
)

func (s *scan) gradleCaches(gh string, pi *projectInfo, d daemons, used map[string][]string) {
	caches := filepath.Join(gh, "caches")
	names := dirNames(s.ctx, caches)
	if len(names) == 0 {
		return
	}
	gens := map[string]int{} // prefix -> highest generation
	for _, n := range names {
		if m := reCacheGen.FindStringSubmatch(n); m != nil {
			if g := atoi(m[2]); g > gens[m[1]] {
				gens[m[1]] = g
			}
		}
	}
	var other []string
	for _, n := range names {
		p := filepath.Join(caches, n)
		switch {
		case reCacheVer.MatchString(n):
			it := s.base("android-gradle-version-cache", "Gradle "+n+" caches", core.RiskModerate)
			it.Path = p
			it.LastUsed = maxTime(daemonLogTime(s.ctx, gh, n), mtime(p))
			it.Meta["version"] = n
			it.Note = "Per-version Gradle caches (Kotlin DSL accessors, file hashes, transforms); rebuilt the first time Gradle " + n + " runs again."
			if who := used[n]; len(who) > 0 {
				sort.Strings(who)
				it.Meta["used_by"] = s.projectList(who)
			} else if pi.known() {
				it.Recommended = true
			} else {
				it.Meta["note"] = "no project roots scanned — usage unknown"
			}
			if d.runs(n) {
				s.warnDaemon(it)
			}
			s.emitPkg(it, sizeOpt{lastUsedFromNewest: true})
		case reCacheGen.MatchString(n):
			m := reCacheGen.FindStringSubmatch(n)
			g := atoi(m[2])
			label := map[string]string{"transforms": "Gradle transforms cache", "jars": "Gradle jars cache", "build-cache": "Gradle local build cache"}[m[1]]
			it := s.base("android-gradle-"+m[1], label+" ("+n+")", core.RiskModerate)
			it.Path = p
			switch {
			case g < gens[m[1]]:
				it.Risk = core.RiskSafe
				it.Recommended = true
				it.Name = label + " (old format " + n + ")"
				it.Note = "Cache format of older Gradle versions; the Gradle versions you run use " + m[1] + "-" + strconv.Itoa(gens[m[1]]) + "."
			case m[1] == "build-cache":
				it.Risk = core.RiskSafe
				it.Note = "Local build cache (org.gradle.caching); only speeds up rebuilds and refills itself."
			default:
				it.Note = "Artifact transform cache (dexing, jetifier, AAR extraction); recomputed locally on the next build (a few extra minutes)."
			}
			if d.any {
				s.warnDaemon(it)
			}
			s.emitPkg(it, sizeOpt{lastUsedFromNewest: true})
		case n == "modules-2":
			it := s.base("android-gradle-modules", "Gradle dependency cache (modules-2)", core.RiskModerate)
			it.Path = p
			it.LastUsed = mtime(filepath.Join(p, "modules-2.lock"))
			it.Note = "Downloaded dependencies (jars, AARs, POMs) shared by all projects; re-downloaded on the next build (often 1-3 GB), offline builds fail until then."
			if d.any {
				s.warnDaemon(it)
			}
			s.emitPkg(it, sizeOpt{lastUsedFromNewest: it.LastUsed.IsZero()})
		case n == "journal-1":
			// Cache access journal used by Gradle's own cleanup: keep it.
		default:
			if loc := s.locate(p); loc.Deletable() {
				other = append(other, p)
			}
		}
	}
	if len(other) > 0 {
		it := s.base("android-gradle-caches-other", "Other Gradle caches ("+strconv.Itoa(len(other))+")", core.RiskModerate)
		it.Location = caches + "/…"
		it.Paths = other
		var base []string
		for _, o := range other {
			base = append(base, filepath.Base(o))
		}
		it.Meta["dirs"] = strings.Join(base, ", ")
		it.Note = "Other shared Gradle caches (Kotlin DSL, scripts, keyrings...); rebuilt or re-downloaded by the next build."
		if d.any {
			s.warnDaemon(it)
		}
		s.add(it, nil, sizeOpt{lastUsedFromNewest: true})
	}
}

func (s *scan) gradleDaemonLogs(gh string, d daemons) {
	dir := filepath.Join(gh, "daemon")
	for _, ver := range dirNames(s.ctx, dir) {
		p := filepath.Join(dir, ver)
		logs, _ := fsx.Glob(s.ctx, filepath.Join(p, "daemon-*.out.log"))
		it := s.base("android-gradle-daemon-logs", "Gradle "+ver+" daemon logs", core.RiskSafe)
		it.LastUsed = maxTime(maxTime(mtimes(logs)...), mtime(p))
		it.Meta["version"] = ver
		it.Note = "Logs and registry of Gradle " + ver + " daemons; the next daemon writes new ones (Gradle itself prunes logs after 14 days)."
		if d.runs(ver) {
			var old []string
			for _, l := range logs {
				if s.now().Sub(mtime(l)) > 24*time.Hour {
					old = append(old, l)
				}
			}
			if len(old) == 0 {
				continue
			}
			it.Name = "Gradle " + ver + " old daemon logs"
			it.Location = p
			it.Paths = old
			it.Note = "Logs of past Gradle " + ver + " daemons (a daemon is running: its registry and current log are kept)."
			s.add(it, nil, sizeOpt{})
			continue
		}
		it.Path = p
		s.emitPkg(it, sizeOpt{})
	}
}

func (s *scan) gradleTmp(gh string) {
	dir := filepath.Join(gh, ".tmp")
	ents, err := fsx.ReadDir(s.ctx, dir)
	if err != nil {
		return
	}
	var files []string
	var last time.Time
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		mt := mtime(p)
		if s.now().Sub(mt) < 24*time.Hour {
			continue
		}
		files = append(files, p)
		if mt.After(last) {
			last = mt
		}
	}
	if len(files) == 0 || s.locate(dir).Place != placeInternal {
		return
	}
	it := s.base("android-gradle-tmp", "Gradle temp files & partial downloads ("+plural(len(files), "file", "files")+")", core.RiskSafe)
	it.Location = dir
	it.Paths = files
	it.LastUsed = last
	it.Recommended = true
	it.Note = "Orphaned temp files and interrupted dependency downloads (gradle_download*.bin) older than a day; neither Gradle nor other cleaners remove them."
	s.add(it, nil, sizeOpt{})
}

func (s *scan) gradleJDKs(gh string, d daemons) {
	dir := filepath.Join(gh, "jdks")
	for _, n := range dirNames(s.ctx, dir) {
		p := filepath.Join(dir, n)
		it := s.base("android-gradle-jdk", "Gradle toolchain JDK · "+n, core.RiskModerate)
		it.Path = p
		it.Meta["installed"] = mtime(p).Format(time.DateOnly)
		it.Note = "JDK auto-provisioned by Gradle toolchains (foojay); re-downloaded (~200-300 MB) when a build requests it."
		if d.any {
			s.warnDaemon(it)
		}
		s.emitPkg(it, sizeOpt{})
	}
}

func (s *scan) gradleMisc(gh string, d daemons) {
	var ps []string
	for _, n := range []string{"native", "kotlin-profile", "notifications", "workers", "build-scan-data"} {
		p := filepath.Join(gh, n)
		if fi, err := fsx.Lstat(s.ctx, p); err == nil && fi.IsDir() {
			ps = append(ps, p)
		}
	}
	if len(ps) == 0 {
		return
	}
	it := s.base("android-gradle-misc", "Gradle native libs, Kotlin build reports & workers", core.RiskSafe)
	it.Location = gh + "/…"
	it.Paths = ps
	it.Note = "Extracted native helpers, Kotlin *.profile build reports and worker state; re-created automatically."
	if d.any {
		s.warnDaemon(it)
	}
	s.add(it, nil, sizeOpt{lastUsedFromNewest: true})
}
