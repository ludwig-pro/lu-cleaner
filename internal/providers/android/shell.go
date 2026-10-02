package android

import (
	"path/filepath"
	"regexp"
	"strings"
)

// shellFiles are the startup files where developers export ANDROID_HOME & co.
// GUI apps and agents do not inherit them, so they are parsed.
var shellFiles = []string{".zshenv", ".zprofile", ".zshrc", ".bash_profile", ".bashrc", ".profile"}

var shellVarsOfInterest = map[string]bool{
	"ANDROID_HOME": true, "ANDROID_SDK_ROOT": true, "ANDROID_AVD_HOME": true,
	"ANDROID_USER_HOME": true, "ANDROID_EMULATOR_HOME": true, "ANDROID_PREFS_ROOT": true,
	"GRADLE_USER_HOME": true, "JAVA_HOME": true,
}

var (
	reAssign = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	reVarRef = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
)

// parseShellExports extracts simple assignments of the variables of interest
// from shell startup file contents. Values referencing $HOME, ~ or variables
// already parsed are expanded; anything dynamic ($(...), backticks, unknown
// variables) is dropped. Every value is kept, in order of appearance.
func parseShellExports(home string, contents ...string) map[string][]string {
	vars := map[string][]string{}
	last := map[string]string{"HOME": home}
	for _, c := range contents {
		for _, line := range strings.Split(c, "\n") {
			m := reAssign.FindStringSubmatch(line)
			if m == nil || !shellVarsOfInterest[m[1]] {
				continue
			}
			v, ok := shellValue(m[2], last)
			if !ok {
				continue
			}
			last[m[1]] = v
			vars[m[1]] = append(vars[m[1]], v)
		}
	}
	return vars
}

func shellValue(raw string, known map[string]string) (string, bool) {
	v := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(v, `"`):
		end := strings.Index(v[1:], `"`)
		if end < 0 {
			return "", false
		}
		v = v[1 : end+1]
	case strings.HasPrefix(v, `'`):
		end := strings.Index(v[1:], `'`)
		if end < 0 {
			return "", false
		}
		return cleanShellPath(v[1:end+1], known["HOME"])
	default:
		if i := strings.IndexAny(v, " \t;#&|"); i >= 0 {
			v = v[:i]
		}
	}
	if strings.ContainsAny(v, "`") || strings.Contains(v, "$(") {
		return "", false
	}
	bad := false
	v = reVarRef.ReplaceAllStringFunc(v, func(ref string) string {
		name := reVarRef.FindStringSubmatch(ref)[1]
		if val, ok := known[name]; ok {
			return val
		}
		bad = true
		return ref
	})
	if bad {
		return "", false
	}
	return cleanShellPath(v, known["HOME"])
}

func cleanShellPath(v, home string) (string, bool) {
	if v == "~" {
		v = home
	} else if strings.HasPrefix(v, "~/") {
		v = filepath.Join(home, v[2:])
	}
	if !filepath.IsAbs(v) {
		return "", false
	}
	return filepath.Clean(v), true
}

// shell returns the variables exported by the user's shell startup files.
func (s *scan) shell() map[string][]string {
	if err := s.shellOnce.Do(s.ctx, func() {
		var contents []string
		for _, f := range shellFiles {
			if c := readSmall(filepath.Join(s.env.Home, f)); c != "" {
				contents = append(contents, c)
			}
		}
		s.shellVars = parseShellExports(s.env.Home, contents...)
	}); err != nil {
		return nil
	}
	return s.shellVars
}

// candidate is a path found for a setting, with where it came from.
type candidate struct {
	Path   string
	Source string
}

// varCandidates returns the values of name from the environment then the
// shell startup files (suffix is joined to each, e.g. "avd").
func (s *scan) varCandidates(name, suffix string) []candidate {
	var out []candidate
	addV := func(v, src string) {
		if v == "" {
			return
		}
		if strings.HasPrefix(v, "~/") {
			v = filepath.Join(s.env.Home, v[2:])
		}
		if !filepath.IsAbs(v) {
			return
		}
		if suffix != "" {
			v = filepath.Join(v, suffix)
		}
		out = append(out, candidate{Path: filepath.Clean(v), Source: src})
	}
	addV(s.p.getenv(name), "$"+name)
	vals := s.shell()[name]
	for i := len(vals) - 1; i >= 0; i-- { // last assignment wins in a shell
		addV(vals[i], "$"+name+" in shell profile")
	}
	return out
}
