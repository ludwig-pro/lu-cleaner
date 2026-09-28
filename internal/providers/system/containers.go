package system

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
)

// ------------------------------------------------------------------ docker engine

// dockerDF is one line of `docker system df --format json`.
type dockerDF struct {
	Type        string `json:"Type"`
	TotalCount  any    `json:"TotalCount"`
	Active      any    `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// parseDockerDF parses the JSON lines (or a JSON array) printed by
// `docker system df --format json`, keyed by Type.
func parseDockerDF(out []byte) map[string]dockerDF {
	res := map[string]dockerDF{}
	trim := bytes.TrimSpace(out)
	if bytes.HasPrefix(trim, []byte("[")) {
		var arr []dockerDF
		if json.Unmarshal(trim, &arr) == nil {
			for _, d := range arr {
				res[d.Type] = d
			}
		}
		return res
	}
	sc := bufio.NewScanner(bytes.NewReader(trim))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var d dockerDF
		if json.Unmarshal(sc.Bytes(), &d) == nil && d.Type != "" {
			res[d.Type] = d
		}
	}
	return res
}

// parseHumanSize parses sizes printed by docker ("1.089GB (60%)", "512.5MB",
// "0B", "12kB"; decimal units) and Homebrew ("2.0GB", "45.6MB", "12KB";
// binary units when binary is true). It returns -1 when unparsable.
func parseHumanSize(s string, binary bool) int64 {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if s == "" {
		return -1
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return -1
	}
	unit := strings.ToUpper(strings.TrimSpace(s[i:]))
	base := 1000.0
	if binary || strings.HasSuffix(unit, "IB") {
		base = 1024
	}
	unit = strings.TrimSuffix(strings.TrimSuffix(unit, "IB"), "B")
	exp := map[string]float64{"": 0, "K": 1, "M": 2, "G": 3, "T": 4, "P": 5}
	e, ok := exp[unit]
	if !ok {
		return -1
	}
	return int64(math.Round(num * math.Pow(base, e)))
}

// docker proposes the Docker engine's own cleanup commands when the current
// context answers: build cache (safe), unused images (moderate), stopped
// containers (caution); unused volumes are only reported (databases!).
func (s *scan) docker() {
	if !s.has("docker") {
		return
	}
	if _, err := s.run(5*time.Second, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		return // daemon not running / VM stopped: disks are reported by vms()
	}
	out, err := s.run(30*time.Second, "docker", "system", "df", "--format", "json")
	if err != nil {
		return
	}
	df := parseDockerDF(out)
	ctxName := ""
	if b, err := s.run(3*time.Second, "docker", "context", "show"); err == nil {
		ctxName = strings.TrimSpace(string(b))
	}
	var post [][]string
	shrink := "Docker Desktop trims its disk image by itself afterwards."
	if profile, ok := colimaProfile(ctxName); ok {
		shrink = "The colima disk on macOS only shrinks after TRIM: `colima ssh -- sudo fstrim -av` is run right after."
		if s.has("colima") {
			post = [][]string{{"colima", "ssh", "-p", profile, "--", "sudo", "fstrim", "-av"}}
		}
	} else if !strings.Contains(ctxName, "desktop") && ctxName != "" && ctxName != "default" {
		shrink = "Space is freed inside the engine's VM; its disk file on macOS may only shrink after TRIM."
	}
	where := "docker context " + ctxName
	if ctxName == "" {
		where = "docker engine"
	}
	add := func(kind, typ, name string, risk core.Risk, cmd []string, note string) {
		d, ok := df[typ]
		if !ok {
			return
		}
		size := parseHumanSize(d.Reclaimable, false)
		if size <= 0 {
			return
		}
		it := s.newItem(kind, core.CatContainers, name, risk)
		it.ID = itemID(kind, where)
		it.Location = where
		it.Method = core.MethodCommand
		it.Command = cmd
		it.PostCommands = post
		it.Size = size
		it.Meta = map[string]string{"context": ctxName, "total": d.Size, "count": toString(d.TotalCount), "active": toString(d.Active)}
		it.Note = note + " " + shrink
		s.emitNow(it)
	}
	add("docker-build-cache", "Build Cache", "Docker build cache (unused)", core.RiskSafe,
		[]string{"docker", "builder", "prune", "-a", "-f"},
		"BuildKit cache not used by a running build; rebuilt by the next `docker build` (slower first build).")
	add("docker-unused-images", "Images", "Docker images not used by any container", core.RiskModerate,
		[]string{"docker", "image", "prune", "-a", "-f"},
		"Images that no container uses; pulled or rebuilt again when needed (bandwidth, time).")
	add("docker-stopped-containers", "Containers", "Docker stopped containers", core.RiskCaution,
		[]string{"docker", "container", "prune", "-f"},
		"Stopped containers and their writable layer (files written inside them are lost; volumes are kept).")
	if d, ok := df["Local Volumes"]; ok {
		if size := parseHumanSize(d.Reclaimable, false); size > 0 {
			it := s.newItem("docker-unused-volumes", core.CatContainers, "Docker volumes not used by any container", core.RiskCaution)
			it.ID = itemID(it.Kind, where)
			it.Location = where
			it.Method = core.MethodReport
			it.Selectable = false
			it.Size = size
			it.Meta = map[string]string{"context": ctxName, "total": d.Size, "count": toString(d.TotalCount)}
			it.Note = "Volumes hold databases and other persistent data: review them with `docker volume ls` and remove the ones you do not need (`docker volume rm`); never pruned automatically."
			s.emitNow(it)
		}
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// colimaProfile returns the colima profile of a docker context name
// ("colima" = default profile, "colima-<p>" = profile p).
func colimaProfile(ctx string) (string, bool) {
	switch {
	case ctx == "colima":
		return "default", true
	case strings.HasPrefix(ctx, "colima-"):
		return strings.TrimPrefix(ctx, "colima-"), true
	}
	return "", false
}

// ------------------------------------------------------------------ VM disks

// vmDisk is one disk file of a VM.
type vmDisk struct {
	path     string
	place    place
	apparent int64
}

// vms reports the disks of container VMs, whose files are sparse (the
// allocated size, not the apparent one, is what they use): colima and Lima
// instances, Docker Desktop's Docker.raw, OrbStack's data image and Apple's
// container CLI data. They hold images AND volumes (databases): report only,
// with the commands to use.
func (s *scan) vms() {
	s.colima()
	s.limaInstances()
	s.dockerDesktop()
	s.orbstack()
	s.appleContainer()
}

// colimaList parses `colima list -j` (one JSON object per line).
func (s *scan) colimaList() map[string]map[string]any {
	res := map[string]map[string]any{}
	if !s.has("colima") {
		return res
	}
	out, err := s.run(10*time.Second, "colima", "list", "-j")
	if err != nil {
		return res
	}
	for _, l := range strings.Split(string(out), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(l)), &m) == nil {
			if n, _ := m["name"].(string); n != "" {
				res[n] = m
			}
		}
	}
	return res
}

func (s *scan) colima() {
	lima := s.home(".colima/_lima")
	if !isDir(lima) {
		return
	}
	statuses := s.colimaList()
	for _, e := range list(lima, false) {
		if !e.dir || strings.HasPrefix(e.name, "_") {
			continue
		}
		profile := "default"
		if e.name != "colima" {
			if !strings.HasPrefix(e.name, "colima-") {
				continue
			}
			profile = strings.TrimPrefix(e.name, "colima-")
		}
		disks := s.vmDisks(e.path, filepath.Join(lima, "_disks", e.name, "datadisk"))
		it := s.newItem("colima-vm", core.CatContainers, "Colima VM · "+profile, core.RiskCaution)
		it.Path = e.path
		it.Meta = map[string]string{"profile": profile}
		if st, ok := statuses[profile]; ok {
			if v, _ := st["status"].(string); v != "" {
				it.Meta["status"] = strings.ToLower(v)
				it.Name += " (" + strings.ToLower(v) + ")"
			}
			if v, ok := st["disk"].(float64); ok && v > 0 {
				it.Meta["disk_limit"] = fsx.Bytes(int64(v))
			}
		}
		it.Meta["delete_command"] = "colima delete -p " + profile
		it.Note = "Docker VM of colima profile " + profile + " (images, containers AND volumes). Shrink it with `docker system prune` then `colima ssh -p " + profile + " -- sudo fstrim -av`, or remove it entirely with `colima delete -p " + profile + "`."
		s.reportVM(it, disks)
	}
}

func (s *scan) limaInstances() {
	root := s.home(".lima")
	if !isDir(root) {
		return
	}
	for _, e := range list(root, false) {
		if !e.dir || strings.HasPrefix(e.name, "_") {
			continue
		}
		if !exists(filepath.Join(e.path, "lima.yaml")) {
			continue
		}
		disks := s.vmDisks(e.path, filepath.Join(root, "_disks", e.name, "datadisk"))
		it := s.newItem("lima-vm", core.CatContainers, "Lima VM · "+e.name, core.RiskCaution)
		it.Path = e.path
		it.Meta = map[string]string{"instance": e.name, "delete_command": "limactl delete " + e.name}
		it.Note = "Lima virtual machine " + e.name + " and its disks; remove it with `limactl delete " + e.name + "` (everything inside is lost)."
		s.reportVM(it, disks)
	}
}

// vmDisks returns the basedisk / diffdisk of an instance dir plus an extra
// datadisk, resolved.
func (s *scan) vmDisks(instDir string, extra ...string) []vmDisk {
	var out []vmDisk
	for _, p := range append([]string{filepath.Join(instDir, "basedisk"), filepath.Join(instDir, "diffdisk")}, extra...) {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		d := vmDisk{path: p, place: s.locate(p)}
		if d.place.Exists {
			var st unix.Stat_t
			if unix.Stat(d.place.Real, &st) == nil {
				d.apparent = st.Size
			}
		}
		out = append(out, d)
	}
	return out
}

// reportVM measures the VM dir plus its internal disks (symlinked disks on
// other volumes do not count) and emits a report item.
func (s *scan) reportVM(it *core.Item, disks []vmDisk) {
	measure := []string{it.Path}
	var apparent int64
	var outside []string
	for _, d := range disks {
		switch {
		case d.place.Dangling:
			vol := d.place.Volume
			if vol == "" {
				vol = d.place.Real
			}
			outside = append(outside, filepath.Base(d.path)+" on "+vol+" (not mounted)")
		case d.place.External:
			outside = append(outside, filepath.Base(d.path)+" on "+nonEmpty(d.place.Volume, "another volume"))
		default:
			apparent += d.apparent
			if !fsx.Within(d.path, it.Path) {
				measure = append(measure, d.path)
			}
		}
	}
	if it.Meta == nil {
		it.Meta = map[string]string{}
	}
	if apparent > 0 {
		it.Meta["disks_apparent"] = fsx.Bytes(apparent)
	}
	if len(outside) > 0 {
		it.Meta["external_disks"] = strings.Join(outside, ", ")
		it.Warn = "some disks live on another volume (" + strings.Join(outside, ", ") + ") — no internal gain for them"
	}
	s.report(it, pubOpts{measure: measure, newest: true, placeholder: true})
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// dockerDesktop reports Docker Desktop's VM disk (sparse Docker.raw).
func (s *scan) dockerDesktop() {
	raw := s.home("Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw")
	if _, err := os.Lstat(raw); err != nil {
		return
	}
	it := s.newItem("docker-desktop-disk", core.CatContainers, "Docker Desktop disk image (Docker.raw)", core.RiskCaution)
	it.Path = raw
	pl := s.locate(raw)
	it.Meta = map[string]string{}
	var st unix.Stat_t
	if pl.Exists && unix.Stat(pl.Real, &st) == nil {
		it.Meta["apparent"] = fsx.Bytes(st.Size)
	}
	if pl.External || pl.Dangling {
		it.Warn = warnExternal
	}
	it.Note = "Docker Desktop's VM disk (images, containers, volumes). Reclaim with `docker system prune` (Docker Desktop trims the file afterwards) or Troubleshoot > Clean / Purge data."
	s.report(it, pubOpts{newest: true})
}

// orbstack reports OrbStack's data image.
func (s *scan) orbstack() {
	imgs, _ := filepath.Glob(s.home("Library/Group Containers/*.dev.orbstack/data/data.img"))
	for _, img := range imgs {
		it := s.newItem("orbstack-data", core.CatContainers, "OrbStack data image", core.RiskCaution)
		it.Path = img
		var st unix.Stat_t
		if unix.Stat(img, &st) == nil {
			it.Meta = map[string]string{"apparent": fsx.Bytes(st.Size)}
		}
		it.Note = "OrbStack's Linux machines and Docker data (sparse image). Reclaim space from OrbStack (docker system prune, `orb delete <machine>`)."
		s.report(it, pubOpts{newest: true})
	}
}

// appleContainer reports the data of Apple's `container` CLI (images and
// snapshots, sparse files).
func (s *scan) appleContainer() {
	dir := s.appSupport("com.apple.container")
	if !isDir(dir) {
		return
	}
	var parts []string
	for _, sub := range []string{"content", "snapshots", "kernels", "builder", "containers", "volumes"} {
		if p := filepath.Join(dir, sub); isDir(p) {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return
	}
	it := s.newItem("apple-container-data", core.CatContainers, "Apple container CLI images & snapshots", core.RiskModerate)
	it.Location = dir
	it.ID = itemID(it.Kind, dir)
	it.Meta = map[string]string{}
	if s.has("container") {
		if out, err := s.run(5*time.Second, "container", "system", "status"); err == nil && !strings.Contains(string(out), "not running") {
			it.Meta["apiserver"] = "running"
		} else {
			it.Meta["apiserver"] = "stopped"
		}
	}
	it.Note = "Images, snapshots and kernels of Apple's `container` tool. Reclaim with `container system start && container image prune -a` (volumes: `container volume prune`)."
	s.report(it, pubOpts{measure: parts, newest: true})
}
