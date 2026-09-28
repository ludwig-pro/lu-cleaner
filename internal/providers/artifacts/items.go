package artifacts

import (
	"bytes"
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
		if bytes.Contains(rc, []byte("enableGlobalCache: true")) {
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

// finish fills size, dangers and recommendation of a measured item.
func (s *scan) finish(c *cand, it *core.Item, size, reclaim, files, apparent int64) {
	it.Sizing = false
	it.Size, it.Files = size, files
	if reclaim < size {
		it.Reclaim = max(reclaim, 1) // 0 would mean "same as Size"
		it.Meta["reclaim"] = fsx.Bytes(reclaim)
		store := "files outside it"
		switch it.Meta["package_manager"] {
		case "pnpm":
			store = "the pnpm store"
		case "bun":
			store = "the bun cache"
		case "yarn-berry":
			store = "the Yarn global cache"
		case "npm", "yarn":
			store = "another install"
		}
		it.Note += fmt.Sprintf(" Hardlinked with %s: only %s is really freed.", store, fsx.Bytes(reclaim))
	}
	if apparent > 0 && it.Meta["icloud"] == "true" && apparent > 4*size && apparent-size > 100<<20 {
		it.Meta["cloud_bytes"] = fsx.Bytes(apparent - size)
	}
	var warns []string
	if w := s.inUseWarn(c); w != "" {
		warns = append(warns, w)
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
func (s *scan) ignoredItem(d *ignDir, st fsx.Stats, nested []*cand, gits []*gitRoot) *core.Item {
	g := d.git
	c := &cand{path: d.path, root: d.root, git: g, tool: g.tool, rule: &rule{Kind: "ignored-dir"}}
	if c.tool == "" {
		c.tool = d.root.tool
	}
	project := g.path
	rel := strings.TrimPrefix(d.path, project+"/")
	it := &core.Item{
		ID:         itemID("ignored-dir", d.path),
		Provider:   "artifacts",
		Category:   core.CatArtifacts,
		Kind:       "ignored-dir",
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
	if len(gits) > 0 {
		it.Method = core.MethodReport
		it.Selectable = false
		rel, _ := filepath.Rel(d.path, gits[0].path)
		it.Warn = "contains a git checkout (" + rel + ") — not proposed"
		return it
	}
	it.Warn = s.inUseWarn(c)
	return it
}
