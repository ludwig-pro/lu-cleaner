package worktrees

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	case w.offline != "" || w.external || w.unreadable != "" || w.repairAt != "":
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
		it.NoRecommend = true
		if w.copyOf != "" {
			warn = append(warn, "orphaned: copy of "+s.env.Pretty(w.copyOf)+" (git tracks that folder, not this one) — contents may be unrecoverable work")
		} else {
			warn = append(warn, "orphaned: git no longer tracks it — contents may be unrecoverable work")
		}
	case w.unreadable != "":
		caution = true
		it.Method = core.MethodReport
		warn = append(warn, w.unreadable+" — status unknown")
	case w.repairAt != "":
		caution = true
		it.Method = core.MethodReport
		warn = append(warn, "main repository moved to "+s.env.Pretty(w.repairAt)+" — run git -C "+w.repairAt+" worktree repair "+w.path)
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
	if w.gitUsable() && !w.external {
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
			// `git worktree remove` keeps the branch: local-only commits on a
			// named branch survive (moderate, but kept out of smart select by
			// the warning). Only a detached HEAD can lose commits (caution).
			switch {
			case !w.unpushedOK:
				if w.detached {
					caution = true
					warn = append(warn, "detached HEAD, unpushed commits unknown")
				} else {
					warn = append(warn, "unpushed commits unknown (branch "+w.branch+" is kept)")
				}
			case w.unpushed > 0:
				msg := plural(w.unpushed, "unpushed commit", "unpushed commits")
				switch {
				case w.detached:
					caution = true
					msg = plural(w.unpushed, "commit", "commits") + " on a detached HEAD in no branch — create a branch first or they are lost"
				case w.gone:
					msg += " (upstream branch deleted; branch " + w.branch + " is kept)"
				default:
					msg += " (branch " + w.branch + " is kept)"
				}
				warn = append(warn, msg)
			}
			// Ignored files are deleted with the worktree and git does not
			// refuse: secrets and personal settings found only here are user
			// data (caution).
			if len(w.envFiles) > 0 {
				caution = true
				warn = append(warn, "ignored secret/local files would be lost: "+strings.Join(limit(w.envFiles, 3), ", "))
			}
			if w.envErr != "" {
				caution = true
				warn = append(warn, w.envErr+" (secrets such as .env would be lost)")
			}
		}
		if w.movedAt != "" {
			caution = true
			warn = append(warn, "moved: git records it at "+s.env.Pretty(w.movedAt)+" — run git -C "+w.main+" worktree repair")
		}
	}
	if n := s.nestedKnown(w); len(n) > 0 {
		w.nested = n
		caution = true
		warn = append(warn, nestedWarn(s, n))
		if it.Method == core.MethodDelete {
			it.Method = core.MethodReport // rm -rf would take the other repository with it
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
	// The current directory may be spelled in another case or Unicode
	// normalization (APFS ignores both).
	if s.cwd != "" && fsx.Within(pathKey(s.cwd), pathKey(w.path)) {
		it.Selectable = false
		warn = append(warn, "current directory")
	}
	switch it.Method {
	case core.MethodReport:
		it.Selectable = false
	case core.MethodDelete:
		// Orphan: the checkout holds a .git file (the safety guard refuses git
		// repositories by default). Its orphan state rests on verified facts,
		// checked again right before removal.
		it.AllowGitRepo = true
		it.Recheck = s.orphanRecheck(w)
	case core.MethodWorktree:
		it.Recheck = s.sessionRecheck(w)
	}
	if caution {
		it.Risk = core.RiskCaution
	}
	it.Warn = strings.Join(warn, " · ")
	it.Note = s.note(w, repoName)
	s.recommend(w, it)
	return it
}

// nestedKnown returns the other worktrees and main repositories found by the
// scan inside w's checkout: removing w would delete them too.
func (s *scan) nestedKnown(w *worktree) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, o := range s.ordered {
		if o != w && o.path != w.path && fsx.Within(o.path, w.path) {
			out = append(out, o.path)
		}
	}
	for p := range s.repos {
		if p != w.path && fsx.Within(p, w.path) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func nestedWarn(s *scan, paths []string) string {
	pretty := make([]string, len(paths))
	for i, p := range paths {
		pretty[i] = s.env.Pretty(p)
	}
	return "contains another git repository or worktree: " + strings.Join(limit(pretty, 3), ", ")
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
	case w.unreadable != "":
		m["unreadable"] = w.unreadable
	case w.repairAt != "":
		m["repair_main"] = w.repairAt
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
	if len(w.nested) > 0 {
		m["nested"] = strings.Join(w.nested, ", ")
	}
	return m
}

// note explains what the worktree is and what removing it costs.
func (s *scan) note(w *worktree, repoName string) string {
	var n string
	switch {
	case w.orphan != "":
		n = "Leftover checkout whose git metadata is gone (" + w.orphan + "): a plain copy of files that git cannot inspect; recover the code by re-cloning the remote"
		switch {
		case w.orphan == orphanPruned:
			// `git worktree repair` cannot restore a deleted admin entry.
			n += "; to keep its changes, copy them into a new worktree (git -C " + w.main + " worktree add <folder> <branch>)"
		case w.orphan == orphanMainGone:
			n += "; if you moved or renamed " + w.main + ", re-link it with git -C <new location> worktree repair " + w.path + " instead"
		}
	case w.unreadable != "":
		n = "Linked worktree whose git data cannot be read by this process (" + w.unreadable + "): its state is unknown, so it is only reported; fix the access and rescan"
	case w.repairAt != "":
		n = "Linked worktree of " + repoName + " whose main repository was moved or renamed to " + w.repairAt + ": git still tracks it; re-link it with git -C " + w.repairAt + " worktree repair " + w.path + ", then rescan"
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
//
// git calls an entry prunable as soon as it cannot lstat <checkout>/.git,
// whatever the reason. An entry is only safe to drop when its checkout
// verifiably no longer exists (confirmedMissing: ENOENT under a readable
// folder), is not on an unmounted volume and its admin dir is not used by a
// checkout found elsewhere (moved). `git worktree prune` is all-or-nothing:
// it is proposed only when every admin dir it would drop is such a safe
// entry; otherwise only the safe admin dirs are deleted. Both variants are
// verified again right before cleaning (Recheck).
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
		verified := map[string]string{} // admin dir -> recorded checkout, for safe entries
		for _, e := range r.prunable {
			a := admins[e.path]
			switch {
			case a != "" && live[realPath(a)]:
				unsafe = append(unsafe, e) // moved checkout still using this entry
			case checkoutGone(e.path) != nil:
				unsafe = append(unsafe, e) // unreadable, unmounted, or still there
			default:
				safe = append(safe, e)
				if a != "" {
					paths = append(paths, a)
					verified[a] = e.path
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
		common := r.common
		if len(unsafe) > 0 || pruneCommandUnsafe(common, verified) != nil {
			// Remove only the verified admin dirs.
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
			it.Note = "Git bookkeeping for worktrees whose folder is already gone; only these entries are removed because `git worktree prune` would also drop the entries of worktrees that are unreadable, on an unmounted volume or moved elsewhere."
			if len(unsafe) > 0 {
				it.Meta["kept"] = strings.Join(limit(prettyAll(s, unsafe), 8), ", ")
			}
			it.Recheck = func(context.Context) error { return recheckAdminDirs(verified) }
		} else {
			it.Recheck = func(context.Context) error { return pruneCommandUnsafe(common, verified) }
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

// checkoutGone returns nil when the recorded checkout path verifiably no
// longer exists (so its admin entry holds nothing anyone can still use).
func checkoutGone(path string) error {
	if v := offlineVolume(path); v != "" {
		return fmt.Errorf("%s: volume %s is not mounted", path, v)
	}
	gone, err := confirmedMissing(path)
	switch {
	case err != nil:
		return fmt.Errorf("%s: cannot verify that it is gone (%s)", path, errText(err))
	case !gone:
		return fmt.Errorf("%s still exists", path)
	}
	return nil
}

// adminEntry is one <common>/worktrees/<id> entry, as `git worktree prune`
// sees it.
type adminEntry struct {
	dir      string // the admin dir
	notDir   bool   // not a directory (git removes it)
	locked   bool   // git never prunes locked entries
	recorded string // checkout recorded in its gitdir file ("" if missing/unreadable/empty)
	gitFile  string // <checkout>/.git as recorded
}

// readAdminEntries lists the admin dirs of the common dir.
func readAdminEntries(common string) ([]adminEntry, error) {
	dir := filepath.Join(common, "worktrees")
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []adminEntry
	for _, de := range des {
		a := adminEntry{dir: filepath.Join(dir, de.Name())}
		if !isDirFollow(a.dir) {
			a.notDir = true
			out = append(out, a)
			continue
		}
		a.locked = fsx.Exists(filepath.Join(a.dir, "locked"))
		if g := readTrim(filepath.Join(a.dir, "gitdir")); g != "" {
			if !filepath.IsAbs(g) {
				g = filepath.Join(a.dir, g)
			}
			a.gitFile = filepath.Clean(g)
			a.recorded = filepath.Dir(a.gitFile)
		}
		out = append(out, a)
	}
	return out, nil
}

// pruneCommandUnsafe returns nil when `git worktree prune` on common would
// remove only admin dirs of verified (admin dir -> recorded checkout) whose
// checkout is still verifiably gone. It mirrors git's rules (expire = now):
// an entry is pruned when it is not a directory, or not locked and its
// gitdir file is missing or empty, or the recorded <checkout>/.git cannot be
// lstat'ed, or when it duplicates another entry's checkout (git keeps one).
func pruneCommandUnsafe(common string, verified map[string]string) error {
	entries, err := readAdminEntries(common)
	if err != nil {
		return fmt.Errorf("cannot read the worktree entries of %s: %v — rescan", common, err)
	}
	// git keeps a single entry per checkout (and never one for the main
	// working tree): the others are pruned as duplicates. Locked entries
	// take no part in this.
	seen := map[string]int{pathKey(realPath(common)): 1}
	for _, a := range entries {
		if a.recorded != "" && !a.locked {
			seen[pathKey(a.gitFile)]++
		}
	}
	for _, a := range entries {
		if a.notDir || a.locked {
			continue // stray file (harmless), or never pruned
		}
		pruned := a.recorded == "" || seen[pathKey(a.gitFile)] > 1
		if !pruned {
			if _, err := os.Lstat(a.gitFile); err != nil {
				pruned = true
			}
		}
		if !pruned {
			continue
		}
		rec, ok := verified[a.dir]
		if !ok || rec != a.recorded {
			what := a.recorded
			if what == "" {
				what = a.dir
			}
			return fmt.Errorf("git worktree prune would also drop the entry of %s, not verified as deleted — rescan", what)
		}
		if err := checkoutGone(a.recorded); err != nil {
			return fmt.Errorf("worktree entry no longer safe to prune: %v — rescan", err)
		}
	}
	return nil
}

// recheckAdminDirs verifies, right before deleting them, that the admin dirs
// still record the same checkout, are not locked, and that this checkout is
// still verifiably gone.
func recheckAdminDirs(verified map[string]string) error {
	for dir, rec := range verified {
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			continue // already pruned
		}
		if fsx.Exists(filepath.Join(dir, "locked")) {
			return fmt.Errorf("%s is locked now — rescan", dir)
		}
		g := readTrim(filepath.Join(dir, "gitdir"))
		if g != "" && !filepath.IsAbs(g) {
			g = filepath.Join(dir, g)
		}
		if g == "" || filepath.Dir(filepath.Clean(g)) != rec {
			return fmt.Errorf("%s changed since the scan — rescan", dir)
		}
		if err := checkoutGone(rec); err != nil {
			return fmt.Errorf("worktree entry no longer safe to prune: %v — rescan", err)
		}
	}
	return nil
}

// adminDirs maps each recorded checkout path to its admin dir
// (<common>/worktrees/<name>, whose "gitdir" file is "<checkout>/.git").
func adminDirs(common string) map[string]string {
	out := map[string]string{}
	entries, _ := readAdminEntries(common)
	for _, a := range entries {
		if !a.notDir && a.recorded != "" {
			out[a.recorded] = a.dir
		}
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
