package android

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// qemuProcesses are the emulator engine binaries (ps comm names).
var qemuProcesses = []string{"qemu-system-aarch64", "qemu-system-x86_64", "qemu-system-i386", "qemu-system-armel"}

type avdHome struct {
	loc     location
	sources []string
	avds    []*avd
}

type avd struct {
	Name    string // file name (NAME.ini / NAME.avd)
	Home    string // AVD home it was found in
	Ini     string // NAME.ini path ("" if none)
	Dir     string // NAME.avd path
	DirLoc  location
	Config  map[string]string
	Target  string // "android-34" from NAME.ini
	Image   string // "system-images/android-34/google_apis/arm64-v8a"
	ABI     string
	API     string
	Tag     string
	Display string
	Last    time.Time
	Running bool
}

// avdSet is everything known about emulators.
type avdSet struct {
	homes    []*avdHome
	images   map[string][]string // normalized image rel path -> AVD names
	complete bool                // every AVD home could be read
}

// avdHomeCandidates follows the emulator's own resolution order, then keeps
// every other existing location (leftover AVDs still take space).
func (s *scan) avdHomeCandidates() []candidate {
	var c []candidate
	c = append(c, s.varCandidates("ANDROID_AVD_HOME", "")...)
	c = append(c, s.varCandidates("ANDROID_EMULATOR_HOME", "avd")...)
	c = append(c, s.varCandidates("ANDROID_USER_HOME", "avd")...)
	c = append(c, s.varCandidates("ANDROID_PREFS_ROOT", filepath.Join(".android", "avd"))...)
	c = append(c, candidate{Path: filepath.Join(s.env.Home, ".android", "avd"), Source: "default (~/.android/avd)"})
	return c
}

func (s *scan) scanAVDs() *avdSet {
	set := &avdSet{images: map[string][]string{}, complete: true}
	running, qemuAlive := s.runningAVDs()
	seen := map[string]*avdHome{}
	seenDirs := map[string]bool{}
	for _, c := range s.avdHomeCandidates() {
		loc := s.locate(c.Path)
		if loc.Place == placeAbsent {
			continue
		}
		if h := seen[loc.Real]; h != nil {
			if !containsStr(h.sources, c.Source) {
				h.sources = append(h.sources, c.Source)
			}
			continue
		}
		h := &avdHome{loc: loc, sources: []string{c.Source}}
		seen[loc.Real] = h
		set.homes = append(set.homes, h)
		if !loc.Exists() {
			set.complete = false
			continue
		}
		h.avds = s.readAVDHome(c.Path, running, qemuAlive, seenDirs)
		for _, a := range h.avds {
			if a.Image != "" {
				set.images[a.Image] = append(set.images[a.Image], a.Name)
			}
		}
	}
	return set
}

var (
	reAvdArg = regexp.MustCompile(`(?:^|\s)-avd\s+(\S+)`)
	reAvdAt  = regexp.MustCompile(`(?:^|\s)@(\S+)`)
)

// runningAVDs returns the names of running AVDs (from the emulator command
// lines) and whether any emulator engine process is alive.
func (s *scan) runningAVDs() (map[string]bool, bool) {
	names := map[string]bool{}
	procs, err := s.runningNames(qemuProcesses...)
	alive := err != nil || len(procs) > 0
	if !alive {
		return names, false
	}
	out, _ := s.env.OutputTimeout(s.ctx, 3*time.Second, "", "pgrep", "-lf", "qemu-system")
	for _, line := range strings.Split(string(out), "\n") {
		for _, re := range []*regexp.Regexp{reAvdArg, reAvdAt} {
			if m := re.FindStringSubmatch(line); m != nil {
				names[m[1]] = true
			}
		}
	}
	return names, true
}

// parseIni parses the key=value files of the emulator (NAME.ini, config.ini).
func parseIni(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		if i := strings.IndexByte(t, '='); i > 0 {
			out[strings.TrimSpace(t[:i])] = strings.TrimSpace(t[i+1:])
		}
	}
	return out
}

// normImage normalizes image.sysdir.1 to "system-images/android-34/tag/abi".
func normImage(v string) string {
	v = strings.ReplaceAll(strings.TrimSpace(v), `\`, "/")
	if i := strings.Index(v, "system-images/"); i >= 0 {
		v = v[i:]
	}
	return strings.Trim(filepath.Clean(v), "/")
}

func (s *scan) readAVDHome(home string, running map[string]bool, qemuAlive bool, seenDirs map[string]bool) []*avd {
	ents, err := fsx.ReadDir(s.ctx, home)
	if err != nil {
		return nil
	}
	byName := map[string]*avd{}
	get := func(name string) *avd {
		if a := byName[name]; a != nil {
			return a
		}
		a := &avd{Name: name, Home: home}
		byName[name] = a
		return a
	}
	for _, e := range ents {
		n := e.Name()
		switch {
		case strings.HasSuffix(n, ".ini") && !e.IsDir():
			name := strings.TrimSuffix(n, ".ini")
			a := get(name)
			a.Ini = filepath.Join(home, n)
			ini := parseIni(readSmall(a.Ini))
			a.Target = ini["target"]
			dir := ini["path"]
			if !filepath.IsAbs(dir) {
				dir = ""
			}
			if dir == "" || !exists(dir) {
				if rel := ini["path.rel"]; rel != "" && exists(filepath.Join(filepath.Dir(home), rel)) {
					dir = filepath.Join(filepath.Dir(home), rel)
				} else if exists(filepath.Join(home, name+".avd")) {
					dir = filepath.Join(home, name+".avd")
				}
			}
			if dir == "" {
				dir = filepath.Join(home, name+".avd")
			}
			if a.Dir == "" {
				a.Dir = filepath.Clean(dir)
			}
		case strings.HasSuffix(n, ".avd") && (e.IsDir() || e.Type()&os.ModeSymlink != 0):
			a := get(strings.TrimSuffix(n, ".avd"))
			if a.Dir == "" {
				a.Dir = filepath.Join(home, n)
			}
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []*avd
	for _, n := range names {
		a := byName[n]
		a.DirLoc = s.locate(a.Dir)
		if a.DirLoc.Exists() {
			if seenDirs[a.DirLoc.Real] {
				continue
			}
			seenDirs[a.DirLoc.Real] = true
			cfg := parseIni(readSmall(filepath.Join(a.Dir, "config.ini")))
			a.Config = cfg
			a.Image = normImage(cfg["image.sysdir.1"])
			a.ABI = cfg["abi.type"]
			a.Tag = cfg["tag.id"]
			a.Display = cfg["avd.ini.displayname"]
			a.Last = maxTime(
				mtime(filepath.Join(a.Dir, "hardware-qemu.ini")),
				mtime(filepath.Join(a.Dir, "snapshots", "default_boot", "snapshot.pb")),
				mtime(filepath.Join(a.Dir, "userdata-qemu.img")),
				mtime(filepath.Join(a.Dir, "userdata-qemu.img.qcow2")),
			)
			a.Running = running[a.Name] ||
				(qemuAlive && len(running) == 0 && exists(filepath.Join(a.Dir, "hardware-qemu.ini.lock")))
		}
		if a.Image != "" {
			parts := strings.Split(a.Image, "/")
			if len(parts) >= 4 {
				a.API = strings.TrimPrefix(parts[1], "android-")
				if a.Tag == "" {
					a.Tag = parts[2]
				}
				if a.ABI == "" {
					a.ABI = parts[3]
				}
			}
		}
		if a.API == "" && strings.HasPrefix(a.Target, "android-") {
			a.API = strings.TrimPrefix(a.Target, "android-")
		}
		if a.Display == "" {
			a.Display = strings.ReplaceAll(a.Name, "_", " ")
		}
		out = append(out, a)
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isX86(abi string) bool { return abi == "x86" || abi == "x86_64" }

// imageInstalled reports whether image exists in one of the readable SDKs.
// known is false when no SDK could be read (unmounted...).
func imageInstalled(image string, sdks []*sdkRoot) (installed, known bool) {
	for _, r := range sdks {
		if !r.loc.Exists() {
			continue
		}
		known = true
		if exists(filepath.Join(r.loc.Path, image)) {
			return true, true
		}
	}
	return false, known
}

func (s *scan) emitAVDs(set *avdSet, sdks []*sdkRoot) {
	for _, h := range set.homes {
		if h.loc.Place != placeInternal {
			s.avdHomeReport(h)
			continue
		}
		for _, a := range h.avds {
			s.emitAVD(a, sdks)
		}
	}
}

func (s *scan) avdHomeReport(h *avdHome) {
	it := s.base("android-avd-home", "Android emulators", core.RiskCaution)
	it.Path = h.loc.Path
	it.Warn = s.placeWarn(h.loc)
	it.Meta["source"] = strings.Join(h.sources, ", ")
	it.Meta["real_path"] = h.loc.Real
	if h.loc.Exists() {
		var names []string
		for _, a := range h.avds {
			names = append(names, a.Name)
		}
		it.Name = "Android emulators (" + plural(len(names), "AVD", "AVDs") + ")"
		if len(names) > 0 {
			it.Meta["avds"] = strings.Join(names, ", ")
		}
		if h.loc.Volume != "" {
			it.Name += " on " + strings.TrimPrefix(h.loc.Volume, "/Volumes/")
		}
	} else {
		it.Name = "Android emulators (AVD folder not reachable)"
	}
	it.Note = "AVD folder outside the internal home volume; its emulators are listed for information and never deleted from here."
	if h.loc.Place == placeOutside && !h.loc.Ghost {
		// Internal disk: measure it so the user sees what is at stake.
		it.Method = core.MethodReport
		it.Selectable = false
		s.add(it, []string{h.loc.Real}, sizeOpt{})
		return
	}
	s.report(it)
}

func (s *scan) emitAVD(a *avd, sdks []*sdkRoot) {
	label := a.Display
	if a.API != "" {
		label += " · API " + a.API
	}
	if a.ABI != "" {
		label += " · " + a.ABI
	}

	switch {
	case a.DirLoc.Place == placeAbsent && a.Ini != "":
		// NAME.ini pointing to a folder that no longer exists.
		it := s.base("android-avd-orphan-ini", "Broken AVD entry · "+a.Display, core.RiskSafe)
		it.Path = a.Ini
		it.Recommended = true
		it.Note = "AVD definition whose folder " + s.env.Pretty(a.Dir) + " was deleted; the emulator lists it as broken and it holds no data."
		it.Meta["missing_dir"] = a.Dir
		it.LastUsed = mtime(a.Ini)
		s.add(it, nil, sizeOpt{})
		return
	case !a.DirLoc.Deletable():
		it := s.base("android-avd", "AVD "+label, core.RiskCaution)
		it.Path = a.Dir
		it.Warn = s.placeWarn(a.DirLoc)
		it.Note = "Emulator stored outside the internal home volume; listed for information only."
		it.Meta["real_path"] = a.DirLoc.Real
		s.report(it)
		return
	}

	guard := qemuProcesses
	meta := map[string]string{"avd_home": s.env.Pretty(a.Home)}
	for k, v := range map[string]string{"api": a.API, "abi": a.ABI, "tag": a.Tag, "image": a.Image,
		"device": a.Config["hw.device.name"], "target": a.Target} {
		if v != "" {
			meta[k] = v
		}
	}

	// Whole AVD.
	it := s.base("android-avd", "AVD "+label, core.RiskCaution)
	it.Location = a.Dir
	it.Paths = []string{a.Dir}
	if a.Ini != "" {
		it.Paths = append(it.Paths, a.Ini)
	}
	it.ProcessGuard = guard
	it.LastUsed = a.Last
	if it.LastUsed.IsZero() {
		it.LastUsed = mtime(filepath.Join(a.Dir, "config.ini"))
	} else {
		meta["last_boot"] = a.Last.Format(time.DateOnly)
	}
	it.Meta = meta
	it.Note = "Emulator with its installed apps, data and snapshots; recreate it in Device Manager (apps and data are lost)."
	switch {
	case a.Running:
		it.Warn = "emulator is running — close it first"
	case s.arm64 && isX86(a.ABI):
		it.Risk = core.RiskModerate
		it.Recommended = true
		// a reason to delete it, not a danger: kept out of Warn (which blocks smart select)
		it.Note = a.ABI + " AVD: cannot boot on Apple Silicon. " + it.Note
	case a.Ini == "":
		it.Warn = "no " + a.Name + ".ini next to it: the emulator cannot see this AVD"
	case a.Image != "":
		if ok, known := imageInstalled(a.Image, sdks); known && !ok {
			it.Warn = "system image " + a.Image + " is not installed — this AVD cannot start"
		}
	}
	s.add(it, nil, sizeOpt{})

	// Quick Boot / saved snapshots.
	snapDir := filepath.Join(a.Dir, "snapshots")
	snaps := dirNames(s.ctx, snapDir)
	if len(snaps) == 0 {
		return
	}
	var named []string
	for _, n := range snaps {
		if n != "default_boot" {
			named = append(named, n)
		}
	}
	sn := s.base("android-avd-snapshots", "Quick Boot snapshots · "+a.Display, core.RiskModerate)
	sn.Path = snapDir
	sn.ProcessGuard = guard
	sn.LastUsed = newestMtime(s.ctx, filepath.Join(snapDir, "*", "snapshot.pb"))
	sn.Meta = map[string]string{"avd": a.Name, "snapshots": strings.Join(snaps, ", ")}
	sn.Note = "Saved emulator RAM/device state for fast boot; the next launch cold-boots (30-90 s), apps and data stay (always drop snapshots together with any qcow2/userdata image, never the images alone)."
	if len(named) > 0 {
		sn.Risk = core.RiskCaution
		sn.Name = "Emulator snapshots · " + a.Display
		sn.Warn = "includes " + plural(len(named), "snapshot you saved", "snapshots you saved") + ": " + strings.Join(named, ", ")
	}
	if a.Running {
		sn.Warn = "emulator is running — close it first"
	}
	s.add(sn, nil, sizeOpt{})
}
