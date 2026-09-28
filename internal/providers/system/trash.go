package system

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// trash proposes to empty ~/.Trash: moving things to the Trash frees nothing
// until it is emptied. ~/.Trash is protected by TCC: without Full Disk Access
// it cannot even be listed, then Finder is asked to empty it instead (its
// size stays unknown).
func (s *scan) trash() {
	dir := s.home(".Trash")
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if !permissionDenied(err) {
			return
		}
		it := s.newItem("trash-finder", core.CatSystem, "Trash (size unknown — needs Full Disk Access)", core.RiskCaution)
		it.ID = itemID(it.Kind, dir)
		it.Location = dir
		it.Method = core.MethodCommand
		it.Command = []string{"osascript", "-e", `tell application "Finder" to empty trash`}
		it.Note = "Emptying the Trash through Finder permanently deletes what you already trashed (on every volume); moving files to the Trash frees nothing until then."
		it.Warn = "cannot read ~/.Trash: grant Full Disk Access to your terminal to measure it (Finder may ask for Automation permission)"
		it.NoRecommend = true // irreversible and not regenerable: only on explicit selection
		s.emitNow(it)
		return
	}
	it := s.newItem("trash", core.CatSystem, "", core.RiskModerate)
	it.ID = itemID(it.Kind, dir)
	it.AllowGitRepo = true // a trashed project may be a git repository: it was thrown away on purpose
	// Emptying the Trash is irreversible and its content is not regenerable:
	// never preselected by smart select, however old the newest entry is
	// (core.Recommend would otherwise pick a stale moderate item).
	it.NoRecommend = true
	for _, e := range ents {
		if e.Name() == ".DS_Store" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		it.Paths = append(it.Paths, p)
		// The time an item was moved to the Trash is its ctime (rename).
		if t := ctime(p); t.After(it.LastUsed) {
			it.LastUsed = t
		}
	}
	if len(it.Paths) == 0 {
		return
	}
	it.Location = dir + "/…"
	it.Name = "Trash (" + plural(len(it.Paths), "item", "items") + ")"
	it.Meta = map[string]string{"items": strconv.Itoa(len(it.Paths))}
	it.Note = "Emptying the Trash: permanently deletes what you already moved there (moving to the Trash frees nothing until it is emptied)."
	s.publish(it, pubOpts{placeholder: true})
}

// ctime returns the status-change time of p (zero if missing).
func ctime(p string) time.Time {
	fi, err := os.Lstat(p)
	if err != nil {
		return time.Time{}
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Ctimespec.Sec, st.Ctimespec.Nsec)
	}
	return fi.ModTime()
}
