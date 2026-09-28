package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

const (
	idleRecommend       = 7 * 24 * time.Hour // clean & pushed worktree untouched for a week
	idleRecommendMerged = 24 * time.Hour     // merged into the default branch, untouched for a day
)

// Status values (Meta "status"), by priority.
const (
	statusOrphan   = "orphan"
	statusLocked   = "locked"
	statusDirty    = "dirty"
	statusUnpushed = "unpushed"
	statusMerged   = "merged"
	statusClean    = "clean"
	statusUnknown  = "unknown"
)

func (w *worktree) status() string {
	switch {
	case w.orphan != "":
		return statusOrphan
	case w.offline != "" || w.external:
		if w.locked {
			return statusLocked
		}
		return statusUnknown
	case w.locked:
		return statusLocked
	case !w.gitOK:
		return statusUnknown
	case w.dirty > 0:
		return statusDirty
	case !w.unpushedOK || w.unpushed > 0:
		return statusUnpushed
	case w.merged:
		return statusMerged
	}
	return statusClean
}

// itemID is stable across scans (and shared by the Sizing placeholder).
func itemID(path string) string { return "worktrees:worktree:" + path }

// item builds the (unsized) item of a worktree.
func (s *scan) item(w *worktree) *core.Item {
	repoName := repoLabel(w.main, w.bare)
	label := w.branch
	if label == "" {
		label = dirLabel(w.path, repoName)
	}
	st := w.status()
	it := &core.Item{
		ID:         itemID(w.path),
		Provider:   "worktrees",
		Category:   core.CatWorktrees,
		Kind:       w.tool + "-worktree",
		Name:       repoName + " · " + label,
		Path:       w.path,
		Project:    w.main,
		LastUsed:   w.activity,
		Risk:       core.RiskModerate,
		Method:     core.MethodWorktree,
		Selectable: true,
		Meta:       s.meta(w, st),
	}

	var warn []string
	caution := false
	switch {
	case w.orphan != "":
		caution = true
		it.Method = core.MethodDelete
		if w.copyOf != "" {
			warn = append(warn, "orphaned: copy of "+s.env.Pretty(w.copyOf)+" (git tracks that folder, not this one) — contents may be unrecoverable work")
		} else {
			warn = append(warn, "orphaned: git no longer tracks it — contents may be unrecoverable work")
		}
	case w.offline != "":
		caution = true
		it.Method = core.MethodReport
		warn = append(warn, "main repo on unmounted volume "+w.offline+" — status unknown")
	case w.external:
		it.Method = core.MethodReport
		warn = append(warn, "on external volume — no internal gain")
	}
	if w.outside && it.Method != core.MethodReport {
		it.Method = core.MethodReport
		msg := "outside your home — remove it yourself: git -C " + w.main + " worktree remove " + w.path
		if fsx.Within(w.path, "/private/tmp") || fsx.Within(w.path, "/tmp") {
			msg = "in /tmp (macOS purges it after 3 days unused) — remove it yourself: git -C " + w.main + " worktree remove " + w.path
		}
		warn = append(warn, msg)
	}
	if w.locked {
		caution = true
		if w.lockReason != "" {
			warn = append(warn, "locked: "+w.lockReason)
		} else {
			warn = append(warn, "locked")
		}
	}
	if w.orphan == "" && w.offline == "" && !w.external {
		switch {
		case !w.gitOK:
			caution = true
			if w.gitErr == "" {
				w.gitErr = "git status failed"
			}
			warn = append(warn, w.gitErr)
		default:
			if w.dirty > 0 {
				caution = true
				warn = append(warn, plural(w.dirty, "uncommitted change", "uncommitted changes"))
			}
			switch {
			case !w.unpushedOK:
				caution = true
				warn = append(warn, "unpushed commits unknown")
			case w.unpushed > 0:
				caution = true
				msg := plural(w.unpushed, "unpushed commit", "unpushed commits")
				if w.detached {
					msg += " on a detached HEAD"
				} else if w.gone {
					msg += " (upstream branch deleted)"
				}
				warn = append(warn, msg)
			}
		}
		if w.movedAt != "" {
			caution = true
			warn = append(warn, "moved: git records it at "+s.env.Pretty(w.movedAt)+" — run git -C "+w.main+" worktree repair")
		}
	}
	if len(w.inUse) > 0 {
		caution = true
		warn = append(warn, "in use by "+strings.Join(limit(w.inUse, 4), ", "))
	}
	if len(w.editors) > 0 {
		caution = true
		warn = append(warn, "open in "+strings.Join(w.editors, ", "))
	}
	if w.session != "" {
		caution = true
		warn = append(warn, w.session)
	}
	if len(w.envFiles) > 0 {
		warn = append(warn, "ignored .env files would be lost: "+strings.Join(limit(w.envFiles, 3), ", "))
	}
	if s.cwd != "" && fsx.Within(s.cwd, w.path) {
		it.Selectable = false
		warn = append(warn, "current directory")
	}
	if it.Method == core.MethodReport {
		it.Selectable = false
	}
	if caution {
		it.Risk = core.RiskCaution
	}
	it.Warn = strings.Join(warn, " · ")
	it.Note = s.note(w, repoName)
	s.recommend(w, it)
	return it
}

// recommend applies the smart-selection rule: clean & pushed (or merged)
// worktrees idle for a week, or merged ones idle for a day.
func (s *scan) recommend(w *worktree, it *core.Item) {
	it.Recommended = false
	if it.Risk != core.RiskModerate || it.Warn != "" || it.Method != core.MethodWorktree || !it.Selectable || w.activity.IsZero() {
		return
	}
	idle := s.now.Sub(w.activity)
	it.Recommended = idle >= idleRecommend || (w.merged && idle >= idleRecommendMerged)
}

func (s *scan) meta(w *worktree, st string) map[string]string {
	m := map[string]string{
		"tool":   w.tool,
		"status": st,
		"main":   w.main,
		"locked": strconv.FormatBool(w.locked),
	}
	for k, v := range w.toolMeta {
		m[k] = v
	}
	if w.lockReason != "" {
		m["lock_reason"] = w.lockReason
	}
	switch {
	case w.orphan != "":
		m["orphan"] = w.orphan
		if w.copyOf != "" {
			m["copy_of"] = w.copyOf
		}
	case w.offline != "":
		m["offline_volume"] = w.offline
	case w.external:
		m["volume"] = "external"
	}
	if w.gitOK {
		if w.detached {
			m["branch"] = "(detached)"
		} else {
			m["branch"] = w.branch
		}
		m["dirty"] = strconv.Itoa(w.dirty)
		if w.commit != "" {
			m["commit"] = w.commit
		}
		if !w.headDate.IsZero() {
			m["head_date"] = w.headDate.Format("2006-01-02 15:04")
		}
		if w.upstream != "" {
			m["upstream"] = w.upstream
			if w.gone {
				m["upstream"] += " (gone)"
			}
		}
		if w.repo != nil && w.repo.defName != "" {
			m["default_branch"] = w.repo.defName
		}
	} else {
		m["dirty"] = "?"
	}
	if w.unpushedOK {
		m["unpushed"] = strconv.Itoa(w.unpushed)
	} else {
		m["unpushed"] = "?"
	}
	switch {
	case w.mergedOK:
		m["merged"] = strconv.FormatBool(w.merged)
	default:
		m["merged"] = "?"
	}
	if w.movedAt != "" {
		m["moved_from"] = w.movedAt
	}
	if len(w.inUse) > 0 {
		m["in_use"] = strings.Join(w.inUse, ", ")
	}
	if len(w.editors) > 0 {
		m["editors"] = strings.Join(w.editors, ", ")
	}
	if w.session != "" {
		m["session"] = w.session
	}
	if len(w.envFiles) > 0 {
		m["env_files"] = strings.Join(w.envFiles, ", ")
	}
	return m
}

// note explains what the worktree is and what removing it costs.
func (s *scan) note(w *worktree, repoName string) string {
	var n string
	switch {
	case w.orphan != "":
		n = "Leftover checkout whose git metadata is gone (" + w.orphan + "): a plain copy of files that git cannot inspect; recover the code by re-cloning the remote"
		if w.copyOf == "" && w.repo != nil {
			n += ", or re-link it with git -C " + w.main + " worktree repair " + w.path
		}
	case w.offline != "":
		n = "Linked worktree whose main repository lives on volume " + w.offline + ": plug the volume in to inspect or remove it with git"
	default:
		branch := "its branch"
		if w.branch != "" {
			branch = "branch " + w.branch
		}
		switch w.tool {
		case toolCodex:
			n = "Codex worktree: git worktree remove deletes the checkout and keeps " + branch + " in " + repoName + "; Codex threads using it lose their folder (archiving them in Codex snapshots first)"
		case toolCursor:
			n = "Cursor agent worktree: unapplied agent changes live only here; " + branch + " stays in " + repoName + " and Cursor recreates worktrees on demand"
		case toolConductor:
			n = "Conductor workspace: prefer Archive in Conductor (keeps its archive commit); removed here, Conductor will show the workspace as missing"
		case toolClaudeDesktop:
			n = "Claude desktop Code worktree (kept by the app after a session is archived): " + branch + " stays in " + repoName + "; ignored files (.env, node_modules) are lost"
		case toolClaude:
			n = "Claude Code worktree (claude --worktree / subagent isolation): committed work stays on " + branch + " in " + repoName + "; ignored files (.env, node_modules) are lost"
		case toolVibeKanban:
			n = "vibe-kanban task worktree in the temp folder (macOS purges it after 3 days unused): " + branch + " stays in " + repoName
		case toolMultica:
			n = "Multica task worktree (main is Multica's bare repository cache): " + branch + " stays in the bare repository"
		default:
			n = "Linked git worktree of " + repoName + ": git worktree remove deletes the checkout and keeps " + branch + "; ignored files (.env, node_modules, native builds) are lost"
		}
	}
	if len(w.toolNotes) > 0 {
		n += " (" + strings.Join(w.toolNotes, "; ") + ")"
	}
	return n + "."
}

// repoLabel is the display name of a main repository.
func repoLabel(main string, bare bool) string {
	name := filepath.Base(main)
	if bare {
		name = strings.TrimSuffix(name, ".git")
		if i := strings.LastIndex(name, "+"); i >= 0 && i < len(name)-1 {
			name = name[i+1:] // Multica: github.com+owner+repo.git
		}
	}
	return name
}

// dirLabel names a detached worktree after its folder (or the parent folder
// for the <id>/<repo> layout of Codex).
func dirLabel(path, repoName string) string {
	b := filepath.Base(path)
	if b == repoName {
		return filepath.Base(filepath.Dir(path))
	}
	return b
}

func limit(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	out := append([]string(nil), list[:n]...)
	return append(out, fmt.Sprintf("+%d", len(list)-n))
}

// ---------------------------------------------------------------- prune

// pruneItems returns one item per main repository whose worktree list has
// prunable entries (checkout deleted by rm, Cursor, Conductor, /tmp cleanup).
func (s *scan) pruneItems() []*core.Item {
	s.mu.Lock()
	repos := make([]*repo, 0, len(s.repos))
	for _, r := range s.repos {
		if len(r.prunable) > 0 {
			repos = append(repos, r)
		}
	}
	live := map[string]bool{} // admin dirs still used by a checkout we found
	for _, w := range s.ordered {
		if w.gitdir != "" {
			live[realPath(w.gitdir)] = true
		}
	}
	s.mu.Unlock()
	sort.Slice(repos, func(i, j int) bool { return repos[i].path < repos[j].path })

	var out []*core.Item
	for _, r := range repos {
		if s.ctx.Err() != nil {
			break
		}
		if s.env.Excluded(r.path) || offlineVolume(r.path) != "" {
			continue
		}
		admins := adminDirs(r.common)
		var safe, unsafe []listEntry
		var paths []string
		for _, e := range r.prunable {
			a := admins[e.path]
			switch {
			case offlineVolume(e.path) != "":
				unsafe = append(unsafe, e) // checkout on an unmounted disk: keep its metadata
			case a != "" && live[realPath(a)]:
				unsafe = append(unsafe, e) // moved checkout still using this entry
			default:
				safe = append(safe, e)
				if a != "" {
					paths = append(paths, a)
				}
			}
		}
		if len(safe) == 0 {
			continue
		}
		repoName := repoLabel(r.path, r.bare)
		it := &core.Item{
			ID:          "worktrees:prune:" + r.path,
			Provider:    "worktrees",
			Category:    core.CatWorktrees,
			Kind:        "worktree-prune",
			Name:        repoName + " · prune " + plural(len(safe), "stale worktree entry", "stale worktree entries"),
			Location:    r.path,
			Project:     r.path,
			Risk:        core.RiskSafe,
			Method:      core.MethodCommand,
			Command:     []string{"git", "-C", r.path, "worktree", "prune"},
			Selectable:  true,
			AlwaysShow:  true,
			Recommended: true,
			Note:        "Git bookkeeping for worktrees whose folder is already gone (removed with rm, by Cursor/Conductor or by /tmp cleanup); pruning it also lets their branches be checked out or deleted again.",
			Meta: map[string]string{
				"count":   strconv.Itoa(len(safe)),
				"entries": strings.Join(limit(prettyAll(s, safe), 8), ", "),
			},
		}
		if len(unsafe) > 0 {
			// `git worktree prune` is all-or-nothing: remove only the safe admin dirs.
			if len(paths) != len(safe) {
				continue
			}
			for _, p := range paths {
				if s.env.IsProtected(p) || s.env.Excluded(p) {
					paths = nil
					break
				}
			}
			if len(paths) == 0 {
				continue
			}
			it.Method = core.MethodDelete
			it.Command = nil
			it.Paths = paths
			it.Note = "Git bookkeeping for worktrees whose folder is already gone; only these entries are removed because `git worktree prune` would also drop " +
				plural(len(unsafe), "entry", "entries") + " of worktrees on an unmounted volume or moved elsewhere."
			it.Meta["kept"] = strings.Join(limit(prettyAll(s, unsafe), 8), ", ")
		}
		for _, p := range paths {
			st, _ := fsx.Size(s.ctx, p, nil)
			it.Size += st.Bytes
			it.Files += st.Files
			if st.Newest.After(it.LastUsed) {
				it.LastUsed = st.Newest
			}
		}
		out = append(out, it)
	}
	return out
}

// adminDirs maps each recorded checkout path to its admin dir
// (<common>/worktrees/<name>, whose "gitdir" file is "<checkout>/.git").
func adminDirs(common string) map[string]string {
	out := map[string]string{}
	dir := filepath.Join(common, "worktrees")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		a := filepath.Join(dir, e.Name())
		g := readTrim(filepath.Join(a, "gitdir"))
		if g == "" {
			continue
		}
		if !filepath.IsAbs(g) {
			g = filepath.Join(a, g)
		}
		out[filepath.Dir(filepath.Clean(g))] = a
	}
	return out
}

func prettyAll(s *scan, es []listEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = s.env.Pretty(e.path)
	}
	return out
}
