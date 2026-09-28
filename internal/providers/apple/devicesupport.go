package apple

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// DeviceSupport folders hold debug symbols copied from physical devices, one
// per OS version: "17.0 (21A329)", "iPhone15,2 17.0 (21A329)", "16.4 (20E247) arm64e".
var dsNameRe = regexp.MustCompile(`^(?:(\S+) )?(\d+(?:\.\d+)*) \(([^)]+)\)(?: (\S+))?$`)

// deviceSupportDirs maps the folder name to the platform it serves.
var deviceSupportDirs = []struct{ dir, platform string }{
	{"iOS DeviceSupport", "iOS"},
	{"watchOS DeviceSupport", "watchOS"},
	{"tvOS DeviceSupport", "tvOS"},
	{"xrOS DeviceSupport", "visionOS"},
	{"visionOS DeviceSupport", "visionOS"},
	{"macOS DeviceSupport", "macOS"},
}

const deviceSupportStale = 90 * day

type dsVersion struct {
	path, platform, model, version, build, arch string
}

func parseDeviceSupport(name string) (model, version, build, arch string, ok bool) {
	m := dsNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", "", "", false
	}
	return m[1], m[2], m[3], m[4], true
}

func (s *scan) deviceSupport() {
	byPlatform := map[string][]dsVersion{}
	for _, d := range deviceSupportDirs {
		root := s.lib("Developer", "Xcode", d.dir)
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, e.Name())
			if s.skipPath(p) {
				continue
			}
			v := dsVersion{path: p, platform: d.platform}
			if model, ver, build, arch, ok := parseDeviceSupport(e.Name()); ok {
				v.model, v.version, v.build, v.arch = model, ver, build, arch
			}
			byPlatform[d.platform] = append(byPlatform[d.platform], v)
		}
	}
	for _, platform := range sortedKeys(byPlatform) {
		vs := byPlatform[platform]
		// Newest = highest OS version (ties: most recently touched).
		newest := -1
		for i, v := range vs {
			if newest < 0 {
				newest = i
				continue
			}
			c := compareVersions(v.version, vs[newest].version)
			if c > 0 || (c == 0 && childrenNewest(v.path).After(childrenNewest(vs[newest].path))) {
				newest = i
			}
		}
		for i, v := range vs {
			s.deviceSupportItem(v, i == newest)
		}
	}
}

func (s *scan) deviceSupportItem(v dsVersion, newest bool) {
	it := s.item("xcode-device-support", core.CatXcode, v.path)
	it.Path = v.path
	if v.version != "" {
		it.Name = v.platform + " " + v.version + " (" + v.build + ")"
		if v.model != "" {
			it.Name += " · " + v.model
		}
	} else {
		it.Name = v.platform + " · " + filepath.Base(v.path)
	}
	it.Risk = core.RiskModerate
	it.Method = core.MethodDelete
	it.ProcessGuard = []string{"Xcode"}
	it.LastUsed = childrenNewest(v.path)
	it.Note = "Debug symbols copied from a device running " + v.platform + " " + v.version +
		"; Xcode copies them again (several minutes, 2-6 GB) the next time such a device is used for debugging."
	setMeta(it, "platform", v.platform)
	setMeta(it, "version", v.version)
	setMeta(it, "build", v.build)
	setMeta(it, "model", v.model)
	if newest {
		setMeta(it, "newest", "true")
		addWarn(it, "newest "+v.platform+" symbols — kept for on-device debugging")
	} else if age := it.Age(s.env.Now); age >= deviceSupportStale {
		it.Recommended = true
	}
	if !s.applyPlace(it, v.path) {
		return
	}
	s.guardWarn(it)
	s.sizeLater(it, []string{v.path}, nil)
}

// compareVersions compares dotted versions numerically ("17.10" > "17.9").
// Non-numeric parts compare as strings. Empty versions sort first.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	if a == "" || b == "" {
		switch {
		case a == b:
			return 0
		case a == "":
			return -1
		}
		return 1
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		if x == "" {
			nx, ex = 0, nil
		}
		if y == "" {
			ny, ey = 0, nil
		}
		switch {
		case ex == nil && ey == nil:
			if nx != ny {
				if nx < ny {
					return -1
				}
				return 1
			}
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return 0
}
