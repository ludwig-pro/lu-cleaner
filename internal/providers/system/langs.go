package system

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// ------------------------------------------------------------------ Go

// golang proposes `go clean -cache` (build cache, safe) and
// `go clean -modcache` (module cache, moderate: re-downloaded). The module
// cache is read-only on disk, so the go command is the right tool. Without a
// go binary the default locations are deleted directly.
func (s *scan) golang() {
	gocache, modcache := "", ""
	hasGo := s.has("go")
	if hasGo {
		out, err := s.run(10*time.Second, "go", "env", "GOCACHE", "GOMODCACHE")
		if err != nil {
			return
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 1 {
			gocache = strings.TrimSpace(lines[0])
		}
		if len(lines) >= 2 {
			modcache = strings.TrimSpace(lines[1])
		}
	} else {
		gocache = s.home("Library/Caches/go-build")
		modcache = s.home("go/pkg/mod")
	}
	type goc struct {
		kind, name string
		dir        string
		risk       core.Risk
		cmd        []string
		note       string
	}
	for _, c := range []goc{
		{"go-build-cache", "Go build cache", gocache, core.RiskSafe, []string{"go", "clean", "-cache"},
			"Compiled packages and test results cached by `go build` / `go test`; rebuilt on the next compile."},
		{"go-module-cache", "Go module cache", modcache, core.RiskModerate, []string{"go", "clean", "-modcache"},
			"Downloaded Go modules (read-only files); downloaded again by the next build that needs them (bandwidth)."},
	} {
		if c.dir == "" || c.dir == "off" || !filepath.IsAbs(c.dir) {
			continue
		}
		c.dir = filepath.Clean(c.dir)
		pl := s.locate(c.dir)
		if !pl.Exists {
			continue
		}
		it := s.newItem(c.kind, core.CatLangs, c.name, c.risk)
		it.ID = itemID(c.kind, c.dir)
		it.Note = c.note
		switch {
		case pl.External:
			it.Location = c.dir
			it.Warn = warnExternal
			s.report(it, pubOpts{measure: []string{pl.Real}})
			continue
		case hasGo:
			it.Location = c.dir
			it.Method = core.MethodCommand
			it.Command = c.cmd
			if !fsx.Within(pl.Real, s.homeReal) {
				// e.g. GOMODCACHE on a shared volume outside home: still ours via go clean
				it.Meta = map[string]string{"real": pl.Real}
			}
			s.publish(it, pubOpts{measure: []string{pl.Real}, placeholder: true, newest: true})
		default:
			if pl.Link {
				continue
			}
			it.Path = c.dir
			s.publish(it, pubOpts{placeholder: true, newest: true})
		}
	}
}

// ------------------------------------------------------------------ rustup

// rustup reports installed Rust toolchains other than the default one and
// the directory overrides (projects may still pin them).
func (s *scan) rustup() {
	home := s.p.getenv("RUSTUP_HOME")
	if home == "" {
		home = s.home(".rustup")
	}
	tcDir := filepath.Join(home, "toolchains")
	if !isDir(tcDir) {
		return
	}
	var settings struct {
		DefaultToolchain string            `toml:"default_toolchain"`
		Overrides        map[string]string `toml:"overrides"`
	}
	_, _ = toml.DecodeFile(filepath.Join(home, "settings.toml"), &settings)
	keep := map[string]string{}
	if d := settings.DefaultToolchain; d != "" {
		keep[d] = "default"
	}
	for dir, tc := range settings.Overrides {
		keep[tc] = "override for " + s.env.Pretty(dir)
	}
	for _, e := range list(tcDir, false) {
		if !e.dir {
			continue
		}
		if _, ok := keep[e.name]; ok || matchesToolchain(e.name, keep) {
			continue
		}
		it := s.newItem("rustup-toolchain", core.CatLangs, "Rust toolchain · "+e.name, core.RiskModerate)
		it.Path = e.path
		it.Meta = map[string]string{"default": settings.DefaultToolchain, "command": "rustup toolchain uninstall " + e.name}
		it.Note = "Rust toolchain that is not the rustup default; remove it with `rustup toolchain uninstall " + e.name + "` unless a project pins it (rust-toolchain.toml)."
		s.report(it, pubOpts{newest: true, placeholder: true})
	}
}

// matchesToolchain handles short names in settings ("stable" vs
// "stable-aarch64-apple-darwin").
func matchesToolchain(name string, keep map[string]string) bool {
	for k := range keep {
		if strings.HasPrefix(name, k+"-") {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ rbenv

// rbenvScanMaxDirs bounds the project walk looking for .ruby-version files.
const rbenvScanMaxDirs = 4000

// rbenv reports installed Ruby versions that are neither the global one nor
// pinned by a project found in the scan roots. CocoaPods needs a working
// Ruby, so nothing is removed automatically.
func (s *scan) rbenv() {
	root := s.p.getenv("RBENV_ROOT")
	if root == "" {
		root = s.home(".rbenv")
	}
	vdir := filepath.Join(root, "versions")
	if !isDir(vdir) {
		return
	}
	var installed []entry
	for _, e := range list(vdir, false) {
		if e.dir {
			installed = append(installed, e)
		}
	}
	if len(installed) < 2 {
		return
	}
	global := strings.TrimSpace(readSmall(filepath.Join(root, "version")))
	used := s.rubyVersionsInProjects()
	for _, e := range installed {
		if e.name == global || used[e.name] != "" {
			continue
		}
		it := s.newItem("rbenv-ruby", core.CatLangs, "Ruby "+e.name+" (rbenv, unused)", core.RiskModerate)
		it.Path = e.path
		it.Meta = map[string]string{"global": global, "command": "rbenv uninstall -f " + e.name}
		it.Note = "Ruby version installed with rbenv that is not the global one and not pinned by a .ruby-version in your project roots; remove it with `rbenv uninstall -f " + e.name + "` (reinstalling compiles for minutes)."
		s.report(it, pubOpts{newest: true, placeholder: true})
	}
}

// rubyVersionsInProjects walks the project and worktree roots (depth <= 3)
// for .ruby-version / .tool-versions files. It returns version -> file.
func (s *scan) rubyVersionsInProjects() map[string]string {
	used := map[string]string{}
	visited := 0
	skip := map[string]bool{"node_modules": true, "Pods": true, "build": true, "DerivedData": true, "vendor": true, "dist": true}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if visited >= rbenvScanMaxDirs || s.ctx.Err() != nil {
			return
		}
		visited++
		for _, f := range []string{".ruby-version", ".tool-versions"} {
			data := readSmall(filepath.Join(dir, f))
			if data == "" {
				continue
			}
			for _, l := range strings.Split(data, "\n") {
				l = strings.TrimSpace(l)
				if f == ".tool-versions" {
					fields := strings.Fields(l)
					if len(fields) >= 2 && fields[0] == "ruby" {
						used[strings.TrimPrefix(fields[1], "ruby-")] = filepath.Join(dir, f)
					}
				} else if l != "" && !strings.HasPrefix(l, "#") {
					used[strings.TrimPrefix(l, "ruby-")] = filepath.Join(dir, f)
					break
				}
			}
		}
		if depth >= 3 {
			return
		}
		for _, e := range list(dir, false) {
			if e.dir && !skip[e.name] {
				walk(e.path, depth+1)
			}
		}
	}
	roots := append(append([]string(nil), s.env.Roots...), s.env.WorktreeRoots...)
	sort.Strings(roots)
	for _, r := range roots {
		walk(r, 0)
	}
	return used
}

// readSmall reads at most 64 KB of a text file ("" when missing).
func readSmall(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, 64<<10)
	n, _ := f.Read(b)
	return string(b[:n])
}
