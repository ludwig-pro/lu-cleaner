// Package core holds the data model shared by every scanner (provider),
// the cleaning engine, the TUI and the CLI.
package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Risk says how much you lose when an item is deleted.
type Risk int

const (
	// RiskSafe: pure cache, regenerated automatically, no user data.
	RiskSafe Risk = iota
	// RiskModerate: regenerable but costs time or bandwidth (node_modules, Pods, runtimes...).
	RiskModerate
	// RiskCaution: holds user data or state someone may want (sessions, dirty worktrees, simulators with data...).
	RiskCaution
	// RiskNever: protected. Never offered for deletion, only reported.
	RiskNever
)

func (r Risk) String() string {
	switch r {
	case RiskSafe:
		return "safe"
	case RiskModerate:
		return "moderate"
	case RiskCaution:
		return "caution"
	case RiskNever:
		return "never"
	}
	return fmt.Sprintf("risk(%d)", int(r))
}

// ParseRisk parses a user-supplied maximum risk (--risk): "safe",
// "moderate" or "caution" (or s, m/mod, c). "never" is rejected: never
// items are report-only, and a maximum of "never" would silently admit every
// caution item (it reads like "take no risk"). Serialized values (JSON,
// history) go through UnmarshalText, which accepts "never".
func ParseRisk(s string) (Risk, error) {
	r, err := parseRisk(s)
	if err != nil {
		return r, err
	}
	if r == RiskNever {
		return RiskSafe, fmt.Errorf("risk %q is not a maximum risk: never items are report-only (want safe|moderate|caution; caution is the highest)", s)
	}
	return r, nil
}

// parseRisk parses any risk name, "never" included.
func parseRisk(s string) (Risk, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "safe", "s":
		return RiskSafe, nil
	case "moderate", "m", "mod":
		return RiskModerate, nil
	case "caution", "c":
		return RiskCaution, nil
	case "never", "n":
		return RiskNever, nil
	}
	return RiskSafe, fmt.Errorf("unknown risk %q (want safe|moderate|caution)", s)
}

func (r Risk) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *Risk) UnmarshalText(b []byte) error {
	v, err := parseRisk(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// Method is how an item gets cleaned.
type Method int

const (
	// MethodDelete removes the path permanently (rm -rf). Frees space immediately.
	MethodDelete Method = iota
	// MethodTrash moves the path to ~/.Trash. Does NOT free space until the Trash is emptied.
	MethodTrash
	// MethodCommand runs Item.Command (e.g. `xcrun simctl delete unavailable`).
	MethodCommand
	// MethodWorktree removes a git worktree through `git worktree remove` (+ prune).
	MethodWorktree
	// MethodReport is informational only; nothing is ever deleted.
	MethodReport
)

func (m Method) String() string {
	switch m {
	case MethodDelete:
		return "delete"
	case MethodTrash:
		return "trash"
	case MethodCommand:
		return "command"
	case MethodWorktree:
		return "worktree"
	case MethodReport:
		return "report"
	}
	return fmt.Sprintf("method(%d)", int(m))
}

func (m Method) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

func (m *Method) UnmarshalText(b []byte) error {
	for _, c := range []Method{MethodDelete, MethodTrash, MethodCommand, MethodWorktree, MethodReport} {
		if c.String() == string(b) {
			*m = c
			return nil
		}
	}
	return fmt.Errorf("unknown method %q", b)
}

// Cleanable reports whether the method actually removes something.
func (m Method) Cleanable() bool { return m != MethodReport }

// Category groups items in the UI and on the command line (--category).
type Category string

const (
	CatAI         Category = "ai"         // Claude Code, Codex, Cursor, ChatGPT, Conductor... data & caches
	CatWorktrees  Category = "worktrees"  // git worktrees created by humans and AI agents
	CatArtifacts  Category = "artifacts"  // project build outputs: node_modules, Pods, android/build...
	CatXcode      Category = "xcode"      // DerivedData, Archives, DeviceSupport, CocoaPods, SwiftPM
	CatSimulators Category = "simulators" // iOS simulator devices, runtimes, caches
	CatAndroid    Category = "android"    // Gradle, AVDs, SDK system images, NDKs, Android Studio
	CatJS         Category = "js"         // npm/yarn/pnpm/bun caches, node versions, Metro/Expo/RN caches
	CatIDE        Category = "ide"        // VS Code, Cursor (editor part), Zed, JetBrains caches
	CatContainers Category = "containers" // Docker, colima, OrbStack
	CatLangs      Category = "langs"      // Go, Rust, Python, Ruby, Homebrew...
	CatSystem     Category = "system"     // ~/Library caches & logs, Trash, browsers, downloads, snapshots
)

// CategoryInfo describes a category for display purposes.
type CategoryInfo struct {
	ID    Category
	Title string
	Icon  string
	Desc  string
}

// Categories is the ordered list of known categories (display order).
var Categories = []CategoryInfo{
	{CatWorktrees, "Worktrees", "🌳", "git worktrees from Codex, Cursor, Conductor, Claude Code…"},
	{CatArtifacts, "Project artifacts", "📦", "node_modules, Pods, iOS/Android builds, .expo, dist…"},
	{CatSimulators, "iOS Simulators", "📱", "simulator devices, runtimes and caches"},
	{CatXcode, "Xcode", "🔨", "DerivedData, Archives, DeviceSupport, CocoaPods, SwiftPM"},
	{CatAndroid, "Android", "🤖", "Gradle, emulators (AVD), SDK images, NDK, Android Studio"},
	{CatAI, "AI tools", "🧠", "Claude, Codex, Cursor, ChatGPT, Conductor data & caches"},
	{CatJS, "JS toolchain", "🟨", "npm, yarn, pnpm, bun, node versions, Metro, Expo"},
	{CatIDE, "IDEs", "🧩", "VS Code, Cursor, Zed, JetBrains caches"},
	{CatContainers, "Containers", "🐳", "Docker, colima, OrbStack"},
	{CatLangs, "Other toolchains", "🧰", "Homebrew, Go, Rust, Python, Ruby"},
	{CatSystem, "System", "💻", "Library caches & logs, Trash, browsers, downloads"},
}

// LookupCategory returns the CategoryInfo for id (Title falls back to id).
func LookupCategory(id Category) CategoryInfo {
	for _, c := range Categories {
		if c.ID == id {
			return c
		}
	}
	return CategoryInfo{ID: id, Title: string(id), Icon: "•"}
}

// categorySynonyms are unambiguous alternative spellings of category ids.
var categorySynonyms = map[string]Category{
	"wt":        CatWorktrees,
	"worktree":  CatWorktrees,
	"artifact":  CatArtifacts,
	"sim":       CatSimulators,
	"simulator": CatSimulators,
	"container": CatContainers,
	"lang":      CatLangs,
}

// toolNames are tool and item names that are NOT categories although users
// type them as such. They used to be aliases of the whole category, so
// `clean --yes -c cursor` silently cleaned every AI tool (Claude, Codex...),
// and `-c node_modules` every project artifact. They are now rejected with a
// hint naming the category they belong to.
var toolNames = map[string]Category{
	// artifacts
	"node_modules": CatArtifacts, "projects": CatArtifacts,
	// simulators
	"ios": CatSimulators,
	// android
	"gradle": CatAndroid, "emulators": CatAndroid, "emulator": CatAndroid, "avd": CatAndroid,
	// ai
	"claude": CatAI, "codex": CatAI, "cursor": CatAI, "chatgpt": CatAI, "conductor": CatAI,
	// js
	"node": CatJS, "npm": CatJS, "yarn": CatJS, "pnpm": CatJS, "bun": CatJS,
	// containers
	"docker": CatContainers, "colima": CatContainers, "orbstack": CatContainers,
	// langs, system
	"brew": CatLangs, "homebrew": CatLangs, "trash": CatSystem,
}

// ParseCategory accepts a category id (case-insensitive) or an unambiguous
// synonym (wt, worktree, artifact, sim, simulator, container, lang). Tool
// names (cursor, docker, node_modules...) are rejected: a category spans
// many tools, and treating a tool name as its category would widen a
// `clean --yes` far beyond what the user asked for.
func ParseCategory(s string) (Category, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, c := range Categories {
		if string(c.ID) == s {
			return c.ID, nil
		}
	}
	if c, ok := categorySynonyms[s]; ok {
		return c, nil
	}
	if c, ok := toolNames[s]; ok {
		return "", fmt.Errorf("%q is not a category: it belongs to %q, which covers other tools too (%s); use %q to mean all of them, or --kind (-k) <kind or provider id> to narrow (kinds are listed by `lu-cleaner scan -c %s --json`)",
			s, c, LookupCategory(c).Desc, c, c)
	}
	return "", fmt.Errorf("unknown category %q (want %s)", s, categoryIDs())
}

// ParseCategoryOrTool is ParseCategory that also accepts the tool names it
// rejects (cursor, docker, node_modules...): it then returns the category
// the tool belongs to, with tool=true. Only for settings where widening to
// the whole category is the safe direction, such as the config's
// disabled_categories (disabling more than asked, as older versions did),
// never to select what to clean.
func ParseCategoryOrTool(s string) (c Category, tool bool, err error) {
	c, err = ParseCategory(s)
	if err == nil {
		return c, false, nil
	}
	if c, ok := toolNames[strings.ToLower(strings.TrimSpace(s))]; ok {
		return c, true, nil
	}
	return "", false, err
}

func categoryIDs() string {
	ids := make([]string, len(Categories))
	for i, c := range Categories {
		ids[i] = string(c.ID)
	}
	return strings.Join(ids, "|")
}

// Item is one cleanable (or reportable) thing found on disk.
//
// Providers emit *Item values through the emit callback. Emitting the same ID
// again replaces the previous value (upsert): this lets a provider show an
// item immediately with Sizing=true and send the measured version later.
// An emitted Item must never be mutated afterwards by the provider.
type Item struct {
	ID       string   `json:"id"`       // stable unique id, usually provider:kind:path
	Provider string   `json:"provider"` // provider id that produced it
	Category Category `json:"category"`
	Kind     string   `json:"kind"` // e.g. "node_modules", "xcode-derived-data", "codex-worktree"
	Name     string   `json:"name"` // short human label
	Path     string   `json:"path,omitempty"`
	// Paths makes a group item (e.g. "Metro caches (23 dirs)", "Claude transcripts > 30 days"):
	// cleaning removes every listed path. Path must then be empty and Location is shown instead.
	Paths    []string `json:"paths,omitempty"`
	Location string   `json:"location,omitempty"` // display location for group / command items

	Size    int64 `json:"size"`              // allocated bytes on disk (hardlinks counted once)
	Reclaim int64 `json:"reclaim,omitempty"` // bytes really freed if deleted (hardlinks shared elsewhere excluded); 0 = same as Size
	Files   int64 `json:"files,omitempty"`
	Sizing  bool  `json:"-"` // size still being computed

	LastUsed time.Time `json:"last_used,omitzero"` // best estimate of last activity (drives "age")

	Risk    Risk     `json:"risk"`
	Method  Method   `json:"method"`
	Command []string `json:"command,omitempty"` // argv for MethodCommand
	// Commands is an optional list of extra commands run after Command/removal (e.g. `git worktree prune`).
	PostCommands [][]string `json:"post_commands,omitempty"`

	// Safety re-checks performed right before cleaning.
	ProcessGuard   []string `json:"process_guard,omitempty"`   // refuse while one of these processes runs (pgrep -x names)
	RequireSibling []string `json:"require_sibling,omitempty"` // at least one of these must exist next to Path
	Selectable     bool     `json:"-"`                         // false => shown but cannot be selected
	AlwaysShow     bool     `json:"-"`                         // shown even below the min-size filter (count-based items: symlinks, prune…)
	AllowGitRepo   bool     `json:"-"`                         // Path may itself be a git repo (e.g. ~/.cocoapods/repos/master)
	// Covers declares, for command items, the directory the command removes
	// entirely (e.g. a simulator device dir for `simctl delete <udid>`). Items
	// inside it become redundant when both are selected (see TopLevel).
	Covers string `json:"-"`
	// Recheck, when set, is called right before cleaning (and in dry-run) to
	// re-validate state that may have changed since the scan (e.g. "the
	// simulator is still shut down"). A non-nil error skips the item.
	Recheck func(ctx context.Context) error `json:"-"`
	// Inodes is the identity snapshot of Targets() taken by the engine at scan
	// time (0 = missing). The executor refuses a target whose inode changed
	// since (TOCTOU protection: a directory replaced by a symlink, a new clone...).
	Inodes []uint64 `json:"-"`

	Project string            `json:"project,omitempty"` // project root / main repo this belongs to
	Meta    map[string]string `json:"meta,omitempty"`    // provider specific details (branch, dirty, api level...)
	Note    string            `json:"note,omitempty"`    // what it is / how it comes back
	Warn    string            `json:"warn,omitempty"`    // highlighted warning (dirty worktree, app running...)

	// Recommended marks items preselected by "smart select" (safe, stale, big).
	Recommended bool `json:"recommended,omitempty"`
	// RequireForce makes the executor refuse the item unless --force (also in
	// dry-run), e.g. orphaned worktree folders whose uncommitted work git can
	// no longer see.
	RequireForce bool `json:"require_force,omitempty"`
	// NoRecommend is a provider veto: never preselect this item, whatever its
	// risk and age (e.g. emptying the Trash, data whose owner is uncertain).
	NoRecommend bool `json:"-"`
}

// SetReclaim records how many bytes deleting the item really frees when that
// is less than Size (hardlinks shared elsewhere). A tree fully hardlinked
// elsewhere is stored as 1 byte, since 0 means "same as Size".
func (it *Item) SetReclaim(reclaim int64) {
	if reclaim >= it.Size {
		it.Reclaim = 0
		return
	}
	it.Reclaim = max(reclaim, 1)
}

// Freed returns the best estimate of bytes freed by cleaning the item.
func (it *Item) Freed() int64 {
	if it.Reclaim > 0 && it.Reclaim < it.Size {
		return it.Reclaim
	}
	return it.Size
}

// Targets returns the filesystem paths removed when the item is cleaned.
func (it *Item) Targets() []string {
	if len(it.Paths) > 0 {
		return it.Paths
	}
	if it.Path != "" {
		return []string{it.Path}
	}
	return nil
}

// Where returns the path to display (Path, else Location).
func (it *Item) Where() string {
	if it.Path != "" {
		return it.Path
	}
	return it.Location
}

// Age returns time since LastUsed (0 if unknown).
func (it *Item) Age(now time.Time) time.Duration {
	if it.LastUsed.IsZero() {
		return 0
	}
	return now.Sub(it.LastUsed)
}

// CanClean reports whether the item may be selected for cleaning.
func (it *Item) CanClean() bool {
	return it.Selectable && it.Risk != RiskNever && it.Method.Cleanable()
}

// Clone returns a shallow copy with its own Meta map.
func (it *Item) Clone() *Item {
	c := *it
	c.Paths = append([]string(nil), it.Paths...)
	c.Inodes = append([]uint64(nil), it.Inodes...)
	if it.Meta != nil {
		c.Meta = make(map[string]string, len(it.Meta))
		for k, v := range it.Meta {
			c.Meta[k] = v
		}
	}
	return &c
}

// Emit is the callback providers use to publish items (upsert by ID).
type Emit func(*Item)

// Provider discovers items of one domain (worktrees, simulators, catalog paths...).
type Provider interface {
	ID() string
	Title() string
	// Categories lists the categories this provider may emit (used to skip disabled providers).
	Categories() []Category
	// Scan must honour ctx cancellation and may call emit concurrently.
	Scan(ctx context.Context, env *Env, emit Emit) error
}
