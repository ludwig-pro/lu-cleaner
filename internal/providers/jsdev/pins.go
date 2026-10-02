package jsdev

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
)

const (
	// pinMaxDepth bounds where Node pins (.nvmrc...) and Expo SDKs are read:
	// root/a/b/c. Yarn Plug'n'Play projects are searched deeper (env.MaxDepth,
	// like the artifacts scanner) since missing one makes the global Yarn
	// cache look unused.
	pinMaxDepth = 3
	// pinMaxDirs bounds the directories read down to pinMaxDepth, and
	// deepMaxDirs those read below it (Plug'n'Play search only): the deep
	// search can never starve the pins. Reaching either bound makes the
	// Plug'n'Play detection incomplete.
	pinMaxDirs  = 40_000
	deepMaxDirs = 150_000
	// projectWorkers bounds concurrent directory reads.
	projectWorkers = 8
	// defaultProjectDepth is used when env.MaxDepth is unset.
	defaultProjectDepth = 8
)

// pinRef is a Node version requested by a project file.
type pinRef struct {
	spec   spec
	source string // file that requests it
}

// projectInfo is what a walk of the project roots tells us.
type projectInfo struct {
	pins []pinRef
	// pnp lists projects using Yarn Plug'n'Play (.pnp.cjs): they read the
	// global Yarn cache at runtime.
	pnp []string
	// pnpIncomplete says why the Plug'n'Play search may have missed projects
	// (capped, unreadable or excluded folders, narrowed roots...); "" when
	// every project root was searched to the full depth. Only a complete
	// search that found nothing lets the global Yarn cache be rated safe.
	pnpIncomplete string
	// expoSDK maps an Expo SDK major to the projects depending on it.
	expoSDK map[int][]string
}

// pinDirSkip are directory names never descended into.
var pinDirSkip = map[string]bool{
	"node_modules": true, "Pods": true, "build": true, "dist": true, "DerivedData": true,
	"target": true, "vendor": true, "Library": true, "Applications": true, "Movies": true,
	"Music": true, "Pictures": true,
}

// projectWalk is the state of one loadProjects call (shared by workers).
type projectWalk struct {
	ctx      context.Context
	env      *core.Env
	maxDepth int
	sem      chan struct{}
	wg       sync.WaitGroup

	mu         sync.Mutex
	info       *projectInfo
	seen       map[string]bool
	budget     [2]int // directories left: [0] down to pinMaxDepth, [1] below
	incomplete []string
}

// loadProjects walks env.Roots and env.WorktreeRoots (plus the extra
// project folders of the artifacts scanner, without explicit roots) looking
// for .nvmrc, .node-version, .tool-versions, package.json (≤ pinMaxDepth
// levels) and Yarn Plug'n'Play projects (.pnp.cjs, ≤ env.MaxDepth levels
// below a root or a linked worktree checkout, like the artifacts scanner).
// A root equal to the home folder is not walked, which makes the
// Plug'n'Play search incomplete. maxDirs > 0 overrides both directory
// budgets (tests).
func loadProjects(ctx context.Context, env *core.Env, maxDirs int) *projectInfo {
	budget := [2]int{pinMaxDirs, deepMaxDirs}
	if maxDirs > 0 {
		budget = [2]int{maxDirs, maxDirs}
	}
	w := &projectWalk{
		ctx: ctx, env: env,
		maxDepth: max(pinMaxDepth, env.MaxDepth),
		sem:      make(chan struct{}, projectWorkers),
		info:     &projectInfo{expoSDK: map[int][]string{}},
		seen:     map[string]bool{},
		budget:   budget,
	}
	if env.MaxDepth <= 0 {
		w.maxDepth = max(pinMaxDepth, defaultProjectDepth)
	}
	roots := append(append([]string{}, env.Roots...), env.WorktreeRoots...)
	nProject := len(roots)
	if !env.ExplicitRoots {
		// The home folders the artifacts scanner also walks (it may find
		// projects there, so may we).
		for _, rel := range projectExtraRoots {
			if p := filepath.Join(env.Home, rel); fsx.IsDir(p) {
				roots = append(roots, p)
			}
		}
	}
	walked := 0 // project and worktree roots walked
	homeKey, homeOK := dirKey(env.Home)
	rootKeys := map[[2]uint64]bool{}
	for i, r := range roots {
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		k, ok := dirKey(r)
		if r == filepath.Clean(env.Home) || (ok && homeOK && k == homeKey) {
			// The whole home is never descended into (pins there would be
			// noise): Plug'n'Play projects anywhere in it may be missed.
			w.markIncomplete("the home folder itself (a root) is not searched")
			continue
		}
		// Same folder under another name (~/code and ~/Code, a symlink):
		// walked once.
		if ok {
			if rootKeys[k] {
				continue
			}
			rootKeys[k] = true
		}
		if i < nProject {
			walked++
		}
		w.spawn(r, 0, 0)
	}
	w.wg.Wait()
	switch {
	case walked == 0:
		w.incomplete = append(w.incomplete, "no project folder to search")
	case env.ExplicitRoots:
		w.incomplete = append(w.incomplete, "only the folders given on the command line were searched")
	}
	info := w.info
	if len(w.incomplete) > 0 {
		sort.Strings(w.incomplete)
		info.pnpIncomplete = joinLimit(w.incomplete, 3)
	}
	// Deterministic output whatever the walk order.
	sort.Slice(info.pins, func(i, j int) bool { return info.pins[i].source < info.pins[j].source })
	sort.Strings(info.pnp)
	for _, l := range info.expoSDK {
		sort.Strings(l)
	}

	// Pins right in the home directory (without descending into it).
	for _, name := range []string{".nvmrc", ".node-version"} {
		p := filepath.Join(env.Home, name)
		if data, err := readSmall(p); err == nil {
			if line := firstLine(data); line != "" {
				info.pins = append(info.pins, pinRef{spec: parseSpec(line), source: p})
			}
		}
	}
	return info
}

// projectExtraRoots are home-relative folders holding projects that the
// artifacts scanner walks besides the roots (kept in sync with its
// extraRoots); skipped with explicit roots.
var projectExtraRoots = []string{
	"conductor/archived-contexts",
	"Documents/Codex",
	".gemini/antigravity/scratch",
}

// dirKey identifies a directory (device, inode), following symlinks.
func dirKey(p string) ([2]uint64, bool) {
	var st unix.Stat_t
	if unix.Stat(p, &st) != nil {
		return [2]uint64{}, false
	}
	return [2]uint64{uint64(st.Dev), st.Ino}, true
}

// spawn walks dir in a new goroutine when a worker slot is free, inline
// otherwise (bounded concurrency without deadlocks). depth is the distance
// from the root (pins, budgets); rel the distance from the root or from the
// nearest linked worktree checkout (bounded by maxDepth).
func (w *projectWalk) spawn(dir string, depth, rel int) {
	w.wg.Add(1)
	select {
	case w.sem <- struct{}{}:
		go func() {
			defer func() { <-w.sem; w.wg.Done() }()
			w.walk(dir, depth, rel)
		}()
	default:
		defer w.wg.Done()
		w.walk(dir, depth, rel)
	}
}

// markIncomplete records why the Plug'n'Play search may have missed projects.
func (w *projectWalk) markIncomplete(why string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, x := range w.incomplete {
		if x == why {
			return
		}
	}
	w.incomplete = append(w.incomplete, why)
}

func (w *projectWalk) walk(dir string, depth, rel int) {
	if w.ctx.Err() != nil {
		return
	}
	if w.env.Excluded(dir) {
		w.markIncomplete("excluded folder " + w.env.Pretty(dir))
		return
	}
	w.mu.Lock()
	if w.seen[dir] {
		w.mu.Unlock()
		return
	}
	b := 0
	if depth > pinMaxDepth {
		b = 1
	}
	if w.budget[b] <= 0 {
		w.mu.Unlock()
		w.markIncomplete("search capped (too many folders)")
		return
	}
	w.seen[dir] = true
	w.budget[b]--
	w.mu.Unlock()

	ents, err := fsx.ReadDir(w.ctx, dir)
	if err != nil {
		// A folder that vanished meanwhile hides nothing; any other error
		// (EACCES, EPERM/TCC, I/O) may hide a project.
		if !errors.Is(err, fs.ErrNotExist) {
			w.markIncomplete("unreadable folder " + w.env.Pretty(dir))
		}
		return
	}
	pinsHere := depth <= pinMaxDepth
	if depth > 0 {
		for _, e := range ents {
			if e.Name() == ".git" && !e.IsDir() {
				// A linked worktree (or submodule) checkout gets the full
				// depth again, like in the artifacts scanner.
				rel = 0
				break
			}
		}
	}
	var subdirs []string
	var pins []pinRef
	pnp := false
	expo := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			if rel >= w.maxDepth || pinDirSkip[name] {
				continue
			}
			switch {
			case name == ".worktrees":
				// In-repo manual worktrees (<repo>/.worktrees/*), walked like
				// the artifacts scanner does.
				subdirs = append(subdirs, filepath.Join(dir, name))
			case name == ".claude":
				// In-repo Claude Code worktrees (<repo>/.claude/worktrees/*).
				if sub := filepath.Join(dir, name, "worktrees"); fsx.IsDir(sub) {
					subdirs = append(subdirs, sub)
				}
			case strings.HasPrefix(name, "."):
			default:
				subdirs = append(subdirs, filepath.Join(dir, name))
			}
			continue
		}
		switch name {
		case ".pnp.cjs", ".pnp.js":
			pnp = true
		case ".nvmrc", ".node-version":
			if !pinsHere {
				continue
			}
			p := filepath.Join(dir, name)
			if data, err := readSmall(p); err == nil {
				if line := firstLine(data); line != "" {
					pins = append(pins, pinRef{spec: parseSpec(line), source: p})
				}
			}
		case ".tool-versions":
			if !pinsHere {
				continue
			}
			p := filepath.Join(dir, name)
			if data, err := readSmall(p); err == nil {
				for _, s := range toolVersionsNode(data) {
					pins = append(pins, pinRef{spec: s, source: p})
				}
			}
		case "package.json":
			if !pinsHere {
				continue
			}
			if data, err := readSmall(filepath.Join(dir, name)); err == nil {
				expo = expoSDKOf(data)
			}
		}
	}
	if pnp || len(pins) > 0 || expo > 0 {
		w.mu.Lock()
		w.info.pins = append(w.info.pins, pins...)
		if pnp {
			w.info.pnp = append(w.info.pnp, dir)
		}
		if expo > 0 {
			w.info.expoSDK[expo] = append(w.info.expoSDK[expo], dir)
		}
		w.mu.Unlock()
	}
	for _, sd := range subdirs {
		w.spawn(sd, depth+1, rel+1)
	}
}

// readSmall reads a small config file (refuses anything above 64 KiB).
func readSmall(p string) (string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return "", os.ErrInvalid
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// firstLine returns the first non-empty, non-comment line.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}

// toolVersionsNode extracts node versions from a .tool-versions file
// ("nodejs 20.11.1" for asdf, "node 20" for mise; several versions allowed).
func toolVersionsNode(data string) []spec {
	var out []spec
	for _, l := range strings.Split(data, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		f := strings.Fields(l)
		if len(f) < 2 || (f[0] != "nodejs" && f[0] != "node") {
			continue
		}
		for _, v := range f[1:] {
			if sp := parseSpec(v); sp.kind != specNone {
				out = append(out, sp)
			}
		}
	}
	return out
}

var leadingInt = regexp.MustCompile(`^[\^~>=<v ]*([0-9]+)`)

// expoSDKOf returns the major of the "expo" dependency of a package.json (0 if none).
func expoSDKOf(data string) int {
	var pj struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal([]byte(data), &pj) != nil {
		return 0
	}
	v, ok := pj.Dependencies["expo"]
	if !ok {
		v = pj.DevDependencies["expo"]
	}
	m := leadingInt.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
