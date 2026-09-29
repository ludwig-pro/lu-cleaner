package artifacts

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// itemID is stable across scans: provider, kind and path.
func itemID(kind, path string) string { return "artifacts:" + kind + ":" + path }

// prefix is the name prefix telling where a checkout comes from ("codex:").
func (c *cand) prefix() string {
	if c.tool != "" {
		return c.tool + ":"
	}
	if c.root.label != "" {
		return c.root.label + ":"
	}
	return ""
}

// label returns "<tool:><project> › <relative path>".
func label(prefix, project, path string) string {
	rel, err := filepath.Rel(project, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(path)
	}
	return prefix + filepath.Base(project) + " › " + rel
}

// baseItem builds the item of an accepted (or pending) candidate, without
// size nor dangers.
func (s *scan) baseItem(c *cand) *core.Item {
	project, pkg := s.projectOf(c)
	c.project, c.pkg = project, pkg
	r := c.rule
	it := &core.Item{
		ID:             itemID(c.kind, c.path),
		Provider:       "artifacts",
		Category:       core.CatArtifacts,
		Kind:           c.kind,
		Name:           label(c.prefix(), project, c.path),
		Path:           c.path,
		LastUsed:       s.activity(c, project, pkg),
		Risk:           r.Risk,
		Method:         core.MethodDelete,
		ProcessGuard:   append([]string(nil), r.ProcessGuard...),
		RequireSibling: r.requireSibling(),
		Selectable:     true,
		Project:        project,
		Meta:           map[string]string{},
		Note:           r.Note,
	}
	if t := s.projectType(pkg, project); t != "" {
		it.Meta["project_type"] = t
	}
	if pkg != project {
		if rel, err := filepath.Rel(project, pkg); err == nil {
			it.Meta["package"] = rel
		}
	}
	if c.tool != "" {
		it.Meta["in_worktree"] = c.tool
	}
	if c.root.label != "" {
		it.Meta["scan_root"] = c.root.label
	}
	if c.git != nil {
		switch {
		case c.ignored == 1:
			it.Meta["git"] = "ignored"
		case c.ignored == 0 && c.tracked == 0:
			it.Meta["git"] = "untracked"
		default:
			it.Meta["git"] = "unknown"
		}
	} else {
		it.Meta["git"] = "none"
	}
	s.kindDetails(c, it)
	if c.weak {
		// Outside git, *.js / index.html / assets are what hand-written
		// sources look like too (webpack configs in build/, a starter's
		// dist/index.html): shown, never preselected nor cleaned by --yes.
		it.Risk = max(it.Risk, core.RiskCaution)
		it.NoRecommend = true
		it.Meta["evidence"] = "weak"
		it.Note += " Not under git and nothing generator-specific inside (source maps, hashed bundles, asset-manifest.json...): it may hold hand-written files."
	}
	it.Recheck = noCheckout([]string{c.path})
	if s.inICloud(c.path) {
		it.Meta["icloud"] = "true"
		it.Note += " In iCloud Drive (Desktop & Documents): deleting also removes it from iCloud; evicted files take no local space."
	}
	return it
}

// kindDetails adds the kind-specific notes, risks and metadata.
func (s *scan) kindDetails(c *cand, it *core.Item) {
	switch c.rule.Kind {
	case "node_modules":
		pm, cmd, dir := s.pkgManager(c.pkg, c.project)
		if pm != "" {
			it.Meta["package_manager"] = pm
		}
		where := ""
		if dir != c.parent {
			if rel, err := filepath.Rel(c.project, dir); err == nil && rel != "." {
				where = " at " + rel
			} else {
				where = " at the workspace root"
			}
		}
		it.Note = fmt.Sprintf("Installed JS dependencies; reinstall with `%s`%s when you need the project again (minutes, network).", cmd, where)
	case "ios-build":
		lock, _ := readSmall(filepath.Join(c.parent, "Podfile.lock"), 4<<20)
		if bytes.Contains(lock, []byte("build/generated/ios")) {
			it.Risk = core.RiskModerate
			it.Note = "Xcode build products plus React Native codegen pods (build/generated/ios, referenced by Podfile.lock): run `pod install` after deleting, then rebuild."
			it.Meta["codegen"] = "true"
		}
	case "js-build", "dist", "out":
		name := filepath.Base(c.path)
		if s.libraryOutput(c.parent, name) {
			it.Risk = core.RiskModerate
			it.Meta["library_output"] = "true"
			it.Note = "Library build output referenced by package.json (main/exports/types): linked apps and config plugins break until the build / `prepare` script runs again."
		}
	case "yarn-cache":
		rc, _ := readSmall(filepath.Join(c.parent, ".yarnrc.yml"), 1<<20)
		if yarnGlobalCache(rc) {
			it.Note = "Yarn Berry project cache left over: .yarnrc.yml has enableGlobalCache: true, so installs no longer use it."
			it.Meta["leftover"] = "true"
		}
	case "python-venv":
		if cfg, err := readSmall(filepath.Join(c.path, "pyvenv.cfg"), 64<<10); err == nil {
			for _, l := range strings.Split(string(cfg), "\n") {
				if k, v, ok := strings.Cut(l, "="); ok {
					if k = strings.TrimSpace(k); k == "version" || k == "version_info" {
						it.Meta["python"] = strings.TrimSpace(v)
					}
				}
			}
		}
	}
}

// yarnGlobalCache reports whether a .yarnrc.yml enables the global cache:
// the last top-level enableGlobalCache key, comments stripped, must be true
// (a commented-out or nested line, or a later false, does not count).
func yarnGlobalCache(rc []byte) bool {
	on := false
	for _, l := range strings.Split(string(rc), "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || l[0] == ' ' || l[0] == '\t' || l[0] == '#' {
			continue // blank, nested key, comment
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok || strings.Trim(strings.TrimSpace(k), `"'`) != "enableGlobalCache" {
			continue
		}
		if i := strings.IndexByte(v, '#'); i >= 0 && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i] // trailing comment
		}
		on = strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"'`), "true")
	}
	return on
}

// noCheckout is the Recheck of artifact items: a target that became a
// checkout since the scan (git worktree add, git init, git clone into it) is
// left alone.
func noCheckout(paths []string) func(context.Context) error {
	paths = append([]string(nil), paths...)
	return func(context.Context) error {
		for _, p := range paths {
			if hasGitEntry(p) {
				return fmt.Errorf("%s now holds a .git entry (a git checkout): left alone", p)
			}
		}
		return nil
	}
}

// groupItem builds the item grouping several small caches of one project.
func (s *scan) groupItem(cs []*cand) *core.Item {
	sort.Slice(cs, func(i, j int) bool { return cs[i].path < cs[j].path })
	it := s.baseItem(cs[0])
	if len(cs) == 1 {
		return it
	}
	it.ID = itemID(cs[0].kind, cs[0].project)
	it.Path = ""
	it.RequireSibling = nil
	for _, c := range cs {
		it.Paths = append(it.Paths, c.path)
		c.project, c.pkg = cs[0].project, cs[0].pkg
	}
	it.Recheck = noCheckout(it.Paths)
	it.Location = cs[0].project + "/…"
	it.Name = fmt.Sprintf("%s%s › %s (%d dirs)", cs[0].prefix(), filepath.Base(cs[0].project), cs[0].rule.Label, len(cs))
	return it
}

// rejected replaces a placeholder whose candidate failed verification.
func (s *scan) rejected(c *cand, why string) *core.Item {
	it := s.baseItem(c)
	it.Method = core.MethodReport
	it.Selectable = false
	it.Risk = core.RiskNever
	it.Warn = "not proposed: " + why
	it.Note = "Looks like " + c.rule.Label + " but " + why + " — part of the project, left alone."
	return it
}

// externalItem reports an artifact living on another volume.
func (s *scan) externalItem(c *cand) *core.Item {
	it := s.baseItem(c)
	it.Method = core.MethodReport
	it.Selectable = false
	it.Warn = "on external volume — no internal gain"
	return it
}

// reclaimNote explains why deleting an artifact frees less than its size:
// part of its data is shared with other files, through hardlinks or APFS
// clones (fsx detects both). For pnpm, bun and Yarn Berry installs the other
// copies are, most likely, the package manager's global store.
func reclaimNote(pm string, reclaim int64) string {
	of := ""
	switch pm {
	case "pnpm":
		of = " of the pnpm store"
	case "bun":
		of = " of the bun cache"
	case "yarn-berry":
		of = " of the Yarn global cache"
	}
	return fmt.Sprintf("Shared with other files (hardlinks or APFS clones%s): only %s is really freed.", of, fsx.Bytes(reclaim))
}

// finish fills size, dangers and recommendation of a measured item.
func (s *scan) finish(c *cand, it *core.Item, size, reclaim, files, apparent int64) {
	it.Sizing = false
	it.Size, it.Files = size, files
	if reclaim < size {
		it.Reclaim = max(reclaim, 1) // 0 would mean "same as Size"
		it.Meta["reclaim"] = fsx.Bytes(reclaim)
		it.Note += " " + reclaimNote(it.Meta["package_manager"], reclaim)
	}
	if apparent > 0 && it.Meta["icloud"] == "true" && apparent > 4*size && apparent-size > 100<<20 {
		it.Meta["cloud_bytes"] = fsx.Bytes(apparent - size)
	}
	// Top-level mtimes miss deep edits and uncommitted work.
	if t := s.deepActivity(c); t.After(it.LastUsed) {
		it.LastUsed = t
	}
	var warns []string
	if w := s.inUseWarn(c); w != "" {
		warns = append(warns, w)
	}
	if w := s.runningWarn(it.ProcessGuard); w != "" {
		warns = append(warns, w) // the executor would skip it anyway: keep it out of smart select
	}
	if c.weak {
		warns = append(warns, "not under git: nothing proves it is build output — check before deleting")
	}
	if b := binaries(c.content); b != "" {
		warns = append(warns, "contains built app binaries ("+b+") — keep them if you still need that build")
	}
	if c.rule.Kind == "cmake-build" {
		if x, ok := matchAny([]string{"*.xcframework"}, c.content); ok {
			warns = append(warns, "contains "+x+" that other projects may link against")
		}
	}
	it.Warn = strings.Join(warns, "; ")
	if it.Warn != "" || size == 0 {
		return
	}
	switch {
	case it.NoRecommend:
	case c.rule.Generic && c.git == nil:
		// Generic names outside git: no forcing, core.Recommend's size and
		// age rules still apply.
	case it.Risk == core.RiskSafe && !it.LastUsed.IsZero() && s.now.Sub(it.LastUsed) >= idleForRecommend:
		it.Recommended = true // build output of a project idle for a day
	case it.Meta["leftover"] == "true":
		it.Recommended = true // Yarn cache nobody reads anymore
	}
}

// binaries lists built app binaries among entries ("a.apk, b.ipa").
func binaries(names map[string]bool) string {
	var out []string
	for n := range names {
		switch strings.ToLower(filepath.Ext(n)) {
		case ".apk", ".aab", ".ipa":
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if len(out) > 3 {
		out = append(out[:3], "…")
	}
	return strings.Join(out, ", ")
}

// runningWarn tells when a ProcessGuard process runs right now: cleaning
// would be refused, so the item must not be preselected (cached per scan).
func (s *scan) runningWarn(guard []string) string {
	if len(guard) == 0 || s.p.running == nil {
		return ""
	}
	key := strings.Join(guard, "\x00")
	s.runMu.Lock()
	names, ok := s.runCache[key]
	if !ok {
		names = s.p.running(guard...)
		s.runCache[key] = names
	}
	s.runMu.Unlock()
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, ", ") + " is running — quit it before cleaning"
}

// inUseWarn tells when a process (dev server, agent, shell) runs inside the
// project, which would break if its dependencies vanished.
func (s *scan) inUseWarn(c *cand) string {
	dir := c.project
	if c.git != nil {
		dir = c.git.path
	}
	if dir == "" || s.p.cwdInside == nil {
		return ""
	}
	s.useMu.Lock()
	pids, ok := s.inUse[dir]
	s.useMu.Unlock()
	if !ok {
		var keep []string
		for _, pid := range strings.Split(s.p.cwdInside(dir), ",") {
			if pid = strings.TrimSpace(pid); pid != "" && pid != s.self {
				keep = append(keep, pid)
			}
		}
		pids = strings.Join(keep, ",")
		s.useMu.Lock()
		s.inUse[dir] = pids
		s.useMu.Unlock()
	}
	if pids == "" {
		return ""
	}
	list := strings.Split(pids, ",")
	if len(list) == 1 {
		return "in use: process " + pids + " runs inside the project (dev server, agent, shell?)"
	}
	shown := strings.Join(list[:min(3, len(list))], ", ")
	if len(list) > 3 {
		shown += "…"
	}
	return fmt.Sprintf("in use: %d processes (%s) run inside the project (dev server, agent, shell?)", len(list), shown)
}

// ignoredItem is a heavy git-ignored folder that no rule knows.
// nestedGit is a checkout the size walk met below it ("" when none).
func (s *scan) ignoredItem(d *ignDir, st fsx.Stats, nested []*cand, gits []*gitRoot, nestedGit string) *core.Item {
	g := d.git
	c := &cand{path: d.path, root: d.root, git: g, tool: g.tool, rule: &rule{Kind: kindIgnoredDir}}
	if c.tool == "" {
		c.tool = d.root.tool
	}
	project := g.path
	rel := strings.TrimPrefix(d.path, project+"/")
	it := &core.Item{
		ID:         itemID(kindIgnoredDir, d.path),
		Provider:   "artifacts",
		Category:   core.CatArtifacts,
		Kind:       kindIgnoredDir,
		Name:       c.prefix() + filepath.Base(project) + " › " + rel + " (git-ignored)",
		Path:       d.path,
		Risk:       core.RiskCaution,
		Method:     core.MethodDelete,
		Selectable: true,
		Project:    project,
		Meta:       map[string]string{"git": "ignored", "residual": fsx.Bytes(st.Bytes)},
		Note:       "Ignored by git, not a known artifact — check before deleting (agent QA caches, staging copies, recordings...).",
	}
	if c.tool != "" {
		it.Meta["in_worktree"] = c.tool
	}
	size, reclaim, files := st.Bytes, st.Reclaim, st.Files
	var nestedBytes int64
	for _, n := range nested {
		if n.item != nil && !n.item.Sizing {
			nestedBytes += n.item.Size
			size += n.item.Size
			reclaim += n.item.Freed()
			files += n.item.Files
		}
	}
	it.Size, it.Files = size, files
	if reclaim < size {
		it.Reclaim = max(reclaim, 1)
	}
	if nestedBytes > 0 {
		it.Meta["artifacts_inside"] = fsx.Bytes(nestedBytes)
		it.Note += fmt.Sprintf(" Includes %s of artifacts also listed on their own.", fsx.Bytes(nestedBytes))
	}
	// Last written (not the project activity): a scratch folder nobody writes is unused.
	it.LastUsed = st.Newest
	if it.LastUsed.After(s.now) {
		it.LastUsed = s.now
	}
	wt := s.worktreeIn(d.path)
	if len(gits) > 0 || wt != "" || nestedGit != "" {
		it.Method = core.MethodReport
		it.Selectable = false
		switch {
		case len(gits) > 0:
			wt, _ = filepath.Rel(filepath.Dir(d.path), gits[0].path)
		case wt == "":
			wt = nestedGit
		}
		it.Warn = "contains a git checkout (" + wt + ") — not proposed"
		return it
	}
	it.Recheck = noCheckout([]string{d.path})
	it.Warn = s.inUseWarn(c)
	return it
}
