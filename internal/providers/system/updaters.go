package system

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// updaterMinAge: update downloads touched more recently may belong to an
// update in progress.
const updaterMinAge = 3 * 24 * time.Hour

// aiOwners are name fragments of apps whose updater leftovers belong to the
// aitools provider (Cursor ships through ToDesktop, Claude, Codex/ChatGPT...).
var aiOwners = []string{
	"cursor", "todesktop", "claude", "anthropic", "codex", "openai", "chatgpt", "conductor",
	"windsurf", "codeium", "kiro", "trae", "antigravity", "warp", "raycast",
}

func aiOwned(name string) bool {
	n := strings.ToLower(name)
	for _, o := range aiOwners {
		if strings.Contains(n, o) {
			return true
		}
	}
	return false
}

// updaters groups the update downloads that app updaters leave behind:
// electron-updater "pending" folders, Squirrel ShipIt caches and Sparkle
// download/installation folders. The updater downloads them again if an
// update is still pending.
func (s *scan) updaters() {
	var cands []string
	for _, e := range list(s.ctx, s.appSupport("Caches"), false) {
		if e.dir && strings.HasSuffix(e.name, "-updater") && !aiOwned(e.name) {
			if p := filepath.Join(e.path, "pending"); isDir(p) {
				cands = append(cands, p)
			}
		}
	}
	caches := s.home("Library/Caches")
	for _, e := range list(s.ctx, caches, false) {
		if !e.dir || aiOwned(e.name) {
			continue
		}
		if strings.HasSuffix(e.name, ".ShipIt") {
			cands = append(cands, e.path)
			continue
		}
		if p := filepath.Join(e.path, "org.sparkle-project.Sparkle"); isDir(p) {
			cands = append(cands, p)
		}
	}
	if len(cands) == 0 {
		return
	}
	it := s.newItem("app-update-downloads", core.CatSystem, "", core.RiskSafe)
	it.ID = itemID(it.Kind, caches)
	it.Recommended = true
	var apps []string
	for _, c := range cands {
		if s.ctx.Err() != nil {
			return
		}
		if s.env.Excluded(c) || s.env.IsProtectedContext(s.ctx, c) {
			continue
		}
		st, _ := fsx.Size(s.ctx, c, nil)
		newest := maxTime(st.Newest, fsx.ModTime(c))
		if st.Bytes == 0 || s.now.Sub(newest) < updaterMinAge {
			continue
		}
		it.Paths = append(it.Paths, c)
		it.Size += st.Bytes
		it.Files += st.Files
		it.LastUsed = maxTime(it.LastUsed, newest)
		apps = append(apps, updaterApp(c))
	}
	if len(it.Paths) == 0 {
		return
	}
	sort.Strings(apps)
	it.Name = "App update downloads (" + strconv.Itoa(len(it.Paths)) + ")"
	it.Location = s.home("Library") + "/…"
	it.Meta = map[string]string{"apps": strings.Join(apps, ", ")}
	it.Note = "Update packages downloaded by app updaters (electron-updater, Squirrel ShipIt, Sparkle) and left behind after the update; downloaded again if an update is still pending."
	s.emitNow(it)
}

// updaterApp returns a short app label for an updater folder.
func updaterApp(p string) string {
	switch {
	case filepath.Base(p) == "pending":
		return strings.TrimSuffix(filepath.Base(filepath.Dir(p)), "-updater")
	case strings.HasSuffix(p, ".ShipIt"):
		return strings.TrimSuffix(filepath.Base(p), ".ShipIt")
	default:
		return filepath.Base(filepath.Dir(p))
	}
}
