package android

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// jdkRelease reads the key="value" pairs of a JDK "release" file.
func jdkRelease(home string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(readSmall(filepath.Join(home, "release")), "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			out[strings.TrimSpace(line[:i])] = strings.Trim(strings.TrimSpace(line[i+1:]), `"`)
		}
	}
	return out
}

func jdkVendor(impl string) string {
	for k, v := range map[string]string{
		"Azul": "Zulu", "Adoptium": "Temurin", "AdoptOpenJDK": "AdoptOpenJDK", "Amazon": "Corretto",
		"JetBrains": "JBR", "Microsoft": "Microsoft", "GraalVM": "GraalVM", "Oracle": "Oracle",
		"BellSoft": "Liberica", "SAP": "SapMachine", "Homebrew": "Homebrew",
	} {
		if strings.Contains(impl, k) {
			return v
		}
	}
	return impl
}

var reStudioJDK = regexp.MustCompile(`homePath value="([^"]+)"`)

// javaHomesInUse returns the JDK homes configured for builds: $JAVA_HOME
// (environment and shell profiles) and org.gradle.java.home (user and project
// gradle.properties), with where each one comes from.
func (s *scan) javaHomesInUse(pi *projectInfo) map[string]string {
	out := map[string]string{}
	add := func(p, why string) {
		if !filepath.IsAbs(p) {
			return
		}
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if out[p] == "" {
			out[p] = why
		}
	}
	for _, c := range s.varCandidates("JAVA_HOME", "") {
		add(c.Path, "JAVA_HOME")
	}
	props := parseProperties(readSmall(filepath.Join(s.gradleHome().Path, "gradle.properties")))
	add(props["org.gradle.java.home"], "org.gradle.java.home")
	if pi != nil {
		for h := range pi.javaHomes {
			add(h, "org.gradle.java.home (project)")
		}
	}
	return out
}

// studioJDKs returns the JDK homes registered in Android Studio (jdk.table.xml).
func (s *scan) studioJDKs() map[string]bool {
	out := map[string]bool{}
	ms, _ := fsx.Glob(s.ctx, filepath.Join(s.env.Home, "Library", "Application Support", "Google", "AndroidStudio*", "options", "jdk.table.xml"))
	for _, m := range ms {
		for _, sm := range reStudioJDK.FindAllStringSubmatch(readSmall(m), -1) {
			v := strings.ReplaceAll(sm[1], "$USER_HOME$", s.env.Home)
			if filepath.IsAbs(v) && v != "/" {
				if r, err := filepath.EvalSymlinks(v); err == nil {
					v = r
				}
				out[filepath.Clean(v)] = true
			}
		}
	}
	return out
}

// jdks reports installed JDKs. System ones (/Library/Java) need admin rights
// and are report-only; JDKs under the home folder (IntelliJ downloads,
// SDKMAN, mise, asdf) can be deleted when not in use. Gradle toolchain JDKs
// are handled with ~/.gradle.
func (s *scan) jdks() {
	type src struct {
		dir, who string
		system   bool
	}
	h := s.env.Home
	srcs := []src{
		{s.p.systemJVMDir, "system", true},
		{filepath.Join(h, "Library", "Java", "JavaVirtualMachines"), "IDE download", false},
		{filepath.Join(h, ".sdkman", "candidates", "java"), "SDKMAN", false},
		{filepath.Join(h, ".local", "share", "mise", "installs", "java"), "mise", false},
		{filepath.Join(h, ".asdf", "installs", "java"), "asdf", false},
	}
	var inUse map[string]string
	var studio map[string]bool
	for _, sr := range srcs {
		names := dirNames(s.ctx, sr.dir)
		if len(names) == 0 {
			continue
		}
		if inUse == nil {
			// Project gradle.properties are not known yet at this stage: user
			// level settings and JAVA_HOME are what matters for JDKs.
			inUse = s.javaHomesInUse(nil)
			studio = s.studioJDKs()
		}
		for _, n := range names {
			if n == "current" {
				continue
			}
			dir := filepath.Join(sr.dir, n)
			home := dir
			if exists(filepath.Join(dir, "Contents", "Home")) {
				home = filepath.Join(dir, "Contents", "Home")
			}
			rel := jdkRelease(home)
			ver, arch := rel["JAVA_VERSION"], rel["OS_ARCH"]
			if ver == "" && rel["IMPLEMENTOR"] == "" {
				continue // not a JDK
			}
			label := "JDK " + ver
			if v := jdkVendor(rel["IMPLEMENTOR"]); v != "" {
				label += " (" + v + ")"
			}
			if arch != "" {
				label += " · " + arch
			}
			realDir := dir
			if r, err := filepath.EvalSymlinks(dir); err == nil {
				realDir = r
			}
			why := ""
			for p, w := range inUse {
				if fsx.Within(p, realDir) {
					why = w
					break
				}
			}
			known := false
			for p := range studio {
				if fsx.Within(p, realDir) {
					known = true
				}
			}
			it := s.base("android-jdk", label, core.RiskModerate)
			it.Path = dir
			it.Meta["installed_by"] = sr.who
			it.Meta["installed"] = mtime(dir).Format(time.DateOnly)
			if why != "" {
				it.Meta["in_use"] = why
			}
			if known {
				it.Meta["android_studio"] = "registered in Android Studio's JDK table"
			}
			if major := versionParts(ver); len(major) > 0 && major[0] < 17 && !(major[0] == 1 && len(major) > 1 && major[1] >= 17) {
				it.Meta["note"] = "older than JDK 17 (React Native 0.73+ needs 17)"
			}
			if s.arm64 && (arch == "x86_64" || arch == "amd64") {
				it.Warn = "x86_64 JDK: runs only under Rosetta"
			}
			if sr.system {
				it.Kind = "android-jdk-system"
				it.Method = core.MethodReport
				it.Selectable = false
				it.Note = "System JDK (admin rights needed): if unused, remove it with `brew uninstall --cask <cask>` or `sudo rm -rf " + dir + "`."
				if why != "" {
					it.Note = "System JDK in use (" + why + "); keep it."
				}
				s.add(it, nil, sizeOpt{})
				continue
			}
			it.Note = "JDK installed by " + sr.who + "; reinstall it with " + sr.who + " if a project needs it again."
			if why != "" {
				it.Selectable = false
				it.Note = "JDK in use (" + why + "); keep it."
			}
			s.emitPkg(it, sizeOpt{})
		}
	}
}
