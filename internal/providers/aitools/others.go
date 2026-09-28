package aitools

import (
	"path/filepath"
	"strconv"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// ---------------------------------------------------------------- Conductor

// conductorArchivedContexts groups, per repository, the archived workspace
// contexts (~/conductor/archived-contexts/<repo>/<workspace>) whose content is
// older than archiveContextAge. They hold notes/plans but also heavy
// artifacts (APKs, recordings, .app bundles).
func (s *scanner) conductorArchivedContexts() {
	root := s.home("conductor/archived-contexts")
	if !s.usable(root) {
		return
	}
	for _, repo := range list(root, false) {
		if s.ctx.Err() != nil {
			return
		}
		if !repo.dir || !s.usable(repo.path) {
			continue
		}
		it := s.newItem("conductor-archived-contexts", core.CatAI, "", core.RiskCaution)
		it.ID = itemID(it.Kind, repo.path)
		for _, ws := range list(repo.path, false) {
			if !ws.dir {
				continue
			}
			st, _ := fsx.Size(s.ctx, ws.path, nil)
			newest := maxTime(st.Newest, ws.mtime)
			if s.now.Sub(newest) < archiveContextAge {
				continue
			}
			it.Paths = append(it.Paths, ws.path)
			it.LastUsed = maxTime(it.LastUsed, newest)
		}
		if len(it.Paths) == 0 {
			continue
		}
		it.Location = repo.path + "/…"
		it.Name = "Conductor archived contexts > 30d · " + repo.name + " (" + plural(len(it.Paths), "workspace", "workspaces") + ")"
		it.Meta = map[string]string{"repo": repo.name, "workspaces": strconv.Itoa(len(it.Paths))}
		it.Note = "Notes, todos, plans and attachments of Conductor workspaces archived 30+ days ago (often with large APKs / recordings); not regenerated."
		s.publish(it, pubOpts{})
	}
}

// ---------------------------------------------------------------- ChatGPT Atlas

// chatgptAtlas proposes the data of the ChatGPT Atlas browser when the app is
// no longer installed.
func (s *scanner) chatgptAtlas() {
	dir := s.appSupport("com.openai.atlas")
	if !isDir(dir) || s.findApp("ChatGPT Atlas", "Atlas") != "" {
		return
	}
	it := s.newItem("chatgpt-atlas-leftover", core.CatAI, "ChatGPT Atlas data (app not installed)", core.RiskCaution)
	it.ID = itemID(it.Kind, dir)
	it.Path = dir
	it.ProcessGuard = procChatGPT
	it.Note = "Browser profile (cookies, logins) and conversation cache left by the uninstalled ChatGPT Atlas app; conversations stay on the server."
	s.publish(it, pubOpts{placeholder: true, newest: true})
}

// ---------------------------------------------------------------- Antigravity

// antigravity handles the leftovers of Google Antigravity in ~/.gemini and
// ~/.antigravity. ~/.gemini itself belongs to gemini-cli and is never removed.
func (s *scanner) antigravity() {
	installed := s.findApp("Antigravity") != ""
	gem := s.home(".gemini")
	if !isDir(gem) && !isDir(s.home(".antigravity")) {
		return
	}
	if !installed {
		if p := filepath.Join(gem, "antigravity-browser-profile"); isDir(p) {
			// A full Chromium profile: saved passwords (Login Data), cookies,
			// a signed-in Google account. Like the ChatGPT Atlas leftover it
			// is user data: caution, never preselected.
			it := s.newItem("antigravity-browser-profile", core.CatAI, "Antigravity agent browser profile (app not installed)", core.RiskCaution)
			it.ID = itemID(it.Kind, p)
			it.Path = p
			it.ProcessGuard = procAntigrav
			it.NoRecommend = true
			it.Note = "Chromium profile of the Antigravity browser agent: saved passwords, cookies / logged-in sessions, history, extensions and caches. The app was not found; a reinstall starts an empty profile (logins are not restored)."
			s.publish(it, pubOpts{placeholder: true, newest: true})
		}
		if p := s.home(".antigravity/extensions"); isDir(p) {
			it := s.newItem("antigravity-extensions", core.CatAI, "Antigravity IDE extensions (app not installed)", core.RiskModerate)
			it.ID = itemID(it.Kind, p)
			it.Path = p
			it.Note = "Extensions of the uninstalled Antigravity IDE; reinstalled with the app."
			s.publish(it, pubOpts{placeholder: true, newest: true})
		}
	}
	// Browser recordings (JPEG frames) older than recordingAge.
	rec := filepath.Join(gem, "antigravity/browser_recordings")
	it := s.newItem("antigravity-browser-recordings", core.CatAI, "", core.RiskCaution)
	it.ID = itemID(it.Kind, rec)
	it.ProcessGuard = procAntigrav
	for _, e := range list(rec, false) {
		if s.now.Sub(e.mtime) < recordingAge {
			continue
		}
		it.Paths = append(it.Paths, e.path)
		it.LastUsed = maxTime(it.LastUsed, e.mtime)
	}
	if len(it.Paths) > 0 {
		it.Location = rec + "/…"
		it.Name = "Antigravity browser recordings > 90d (" + strconv.Itoa(len(it.Paths)) + ")"
		it.Note = "Screen-recording frames of Antigravity browser-agent runs; not regenerated."
		s.publish(it, pubOpts{placeholder: true, newest: true})
	}
	// Conversations: report only. (antigravity/scratch holds user projects: the
	// artifacts scanner handles their build outputs, a report item here would
	// shadow them.)
	for _, r := range []struct{ kind, name, rel, note string }{
		{"antigravity-conversations", "Antigravity conversations", "antigravity/conversations",
			"Antigravity chat transcripts; kept (report only)."},
		{"antigravity-brain", "Antigravity brain / implicit context", "antigravity/brain",
			"Antigravity agent memory; kept (report only)."},
	} {
		p := filepath.Join(gem, r.rel)
		if !isDir(p) {
			continue
		}
		it := s.newItem(r.kind, core.CatAI, r.name, core.RiskCaution)
		it.ID = itemID(r.kind, p)
		it.Path = p
		it.Method = core.MethodReport
		it.Selectable = false
		it.Note = r.note
		s.publish(it, pubOpts{newest: true})
	}
}
