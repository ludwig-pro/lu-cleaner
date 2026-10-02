# Architecture

```
cmd/lu-cleaner/          main (version via -ldflags)
internal/
  core/                  data model: Item, Risk, Method, Category, Provider, Env, Filter, Recommend, TopLevel
  fsx/                   fast concurrent directory sizing (allocated blocks, hardlink and APFS clone
                         aware), app-data protection (other apps' containers), human units
  sysx/                  disk free (statfs), running processes, APFS local snapshots
  safety/                Guard: the last line of defence before any deletion
  config/                ~/.config/lu-cleaner/config.toml
  engine/                runs providers concurrently, streams item upserts
  scanctl/               per-invocation I/O and command budgets, profiles and process priorities
  clean/                 executor: delete / trash / command / git worktree removal, history
  providers/
    registry.go          providers.All()
    catalog/             data-driven well-known paths (data_*.go, one file per domain)
    worktrees/           git worktrees (Codex, Cursor, Conductor, Claude Code, manual)
    artifacts/           project artifacts (node_modules, Pods, android/build, .expo…) — npkill/kondo-like
    apple/               Xcode DerivedData per project, Archives, DeviceSupport, simulators & runtimes
    android/             AVDs, SDK system images / NDK / build-tools, Gradle versions
    jsdev/               node versions (nvm/fnm), fnm multishells, Metro/Haste temp caches, pnpm store…
    aitools/             Claude Code / Codex / Cursor… sessions & logs by age, stale DB backups
    system/              Trash, Docker/colima, Homebrew, local snapshots (report), Downloads (report)
  tui/                   Bubble Tea: picker (clean) + analyzer (ncdu-like)
  cli/                   cobra commands
```

## Data flow

```
providers ──emit(*Item) upsert──▶ engine.Run (chan Event) ──▶ TUI picker / CLI table / JSON
                                                         │
                                          user selection ▼
                                    clean.Run(items) ── Guard.Check each target ──▶ rm / Trash / command / git worktree remove
                                                         └─▶ history.jsonl + statfs before/after
```

## Contracts

* Scan limits resolve without process effects: `--scan-mode` > `config.scan_mode` > `eco`.
  `LU_WALKERS` overrides only the shared I/O budget when it is a positive integer; invalid
  values keep the profile default and are logged only in verbose mode. The CLI creates one
  `scanctl.Controller` per invocation and attaches it to the context for the engine,
  providers, size cache and TUI. `Env.ScanLimits` carries the resolved limits to direct callers.
* `eco` admits 2 simultaneous I/O operations, 1 command and 1 prefetch per sizing pool, with
  batches of 256 entries and a cancellable 5 ms cooldown after each I/O operation. `fast`
  admits 8 I/O operations, 4 commands and 4 prefetch jobs, with the same batch size and no
  cooldown. An I/O permit covers a bounded read or metadata operation; it is released before
  recursion, waiting for cached sizes or executing a command. Command execution timeouts start
  after admission; cancellation and a parent deadline also stop queued work.
* Process policy activates after validating scan limits and before scan setup's root
  inventories, only on scan paths (including `analyze`, the picker and
  `doctor --no-scan`, which measures the Trash). `eco` sets `GOMAXPROCS` to the smaller of its
  current value and 2 and requests macOS background priority; `fast` keeps inherited runtime
  and OS priorities. A priority failure is a warning: resource quotas still apply. The CLI
  restores the previous runtime and priority at the end of `App.Run`, after TUI sizing workers,
  directory listings, repository checks and provider streams have stopped. Rescans cancel and
  join the previous sizing pool, cancel replaced listings and ignore obsolete results while
  keeping the invocation controller. Configuration, help, version, history and catalog commands
  do not activate process policy. Standalone internal scans without a controller use `fast`
  without process effects.
* Directory listings are complete and sorted despite bounded reads. Cancellation is checked
  between batches and metadata operations. Sizing accounting remains allocated-block based,
  with the same hardlink and APFS clone rules. Scan/clean/analyze JSON contracts stay unchanged;
  only `config show --json` gains effective scan limits. Verbose stderr includes limits and
  resource counters at invocation completion. `files`/`dirs` count entries walked for sizing;
  `cache_hits`/`cache_misses` describe the invocation's memoized size requests. `LU_TRACE=1`
  reports the persistent cache separately. Configuration output includes the resolved
  `GOMAXPROCS` value and requested background/inherited priority without applying either.

* A provider never deletes anything. It only emits `*core.Item`.
* Emitting the same `Item.ID` again replaces the previous value (use it to show an item with
  `Sizing=true` first, then with its measured size). Never mutate an item after emitting it
  (emit `it.Clone()` if you keep working on it).
* `Item.Size` is the allocated size (`st_blocks*512`), hardlinks counted once; `Item.Reclaim`
  is set (through `Item.SetReclaim`) when deleting frees less than `Size` because the data is
  shared with files outside the item: hardlinks (pnpm store…) AND APFS clones (bun and pnpm
  installs, `cp -c`, Finder duplicates), which only count their private bytes. `Freed()` is what
  totals use. It is an estimate: data shared between two separately measured trees (the bun cache
  and a `node_modules` installed from it) is freed only when both are deleted.
* `Item.LastUsed` drives ages, `--older-than`, and which moderate items smart select recommends.
* Risk: `safe` (pure cache) → `moderate` (regenerable, costs time/bandwidth) → `caution`
  (user data/state) → `never` (report only).
* `Item.NoRecommend` is a provider veto: `core.Recommend` (smart select) never recommends the
  item (emptying the Trash, orphan data whose origin is uncertain…). Items with a `Warn` are
  never recommended either, and `clean --yes` treats them like `caution` items: they need an
  explicit `--risk caution` (they are listed as "held back" otherwise).
* Nothing is preselected, ever: the picker (`lu-cleaner`, `lu-cleaner clean`) opens with an empty
  selection, so deleting is always the result of an explicit choice (space, or the `a` key that
  selects what `core.Recommend` approves in the current view). `--smart` is the explicit opt-in
  that starts the picker with the recommended items selected; with `--yes` it restricts the plan
  to them. The confirmation dialog shows the total before anything is removed.
* Methods: `delete` (default, frees space now), `trash` (does NOT free space until the Trash is
  emptied), `command` (tool's own cleanup: `xcrun simctl delete <udid>`, `xcrun simctl runtime
  delete`, `pnpm store prune`…), `worktree` (`git worktree remove` + `prune`), `report`.
  Unavailable simulators are one item per device (`simctl delete <udid>`, re-checked with
  `simctl list` right before running), never `simctl delete unavailable`, which would also remove
  devices that are only unusable right now (another Xcode selected, runtime image not mounted)
  and come back once the setup is fixed.
* Nested selections (`core.TopLevel`, used by the executor, the CLI plan and the TUI): an item
  whose targets all lie inside (or equal) a target of another selected item is dropped; on
  identical paths the first item of the list wins. A command item takes part through `Covers`,
  the directory it removes entirely (the device folder for `simctl delete <udid>`): items inside
  it are dropped, and the command is dropped when `Covers` equals or lies inside a selected path
  item — a path item is never dropped because of a `Covers` equal to its own path, so the result
  does not depend on the selection order. Exception: a worktree item nested in another selected
  worktree item is kept, so that git removes the inner one first with its own checks (the
  executor processes worktrees deepest first and keeps the outer one when an inner one is not
  removed). `core.Total` still counts nested worktrees once.
* `Item.RequireForce`: the executor refuses the item unless `--force`, in dry-run too (the dry
  run reports the same refusal, "needs --force: <reason>"). Set on orphaned worktree folders
  (MethodDelete, `AllowGitRepo`) whose uncommitted work git can no longer see. Trash mode may
  still move them (recoverable).
* Worktree removal keeps the branch: commits on a branch (pushed or not) stay in the main
  repository. Without `--force` it refuses a worktree that is locked, has uncommitted or untracked
  changes (measured with explicit git flags that ignore the user's config), has commits on no
  branch (detached HEAD), or contains another registered worktree or repository. `--dry-run` runs
  the same checks (the status check included) and reports the same refusals.
* Trash mode (`--trash`, `use_trash`, `t` in the TUI; `--trash=false` overrides `use_trash`) only
  moves filesystem paths to the Trash. Worktree and command items are skipped ("not possible in
  Trash mode (it would delete permanently)"), items already in `~/.Trash` too ("already in the
  Trash"). Summaries and history report Trash moves (`Result.Trashed`, `Summary.Trashed`) apart
  from freed space.
* Orphans: a worktree or main repository is "missing" only on ENOENT with a readable nearest
  existing ancestor. Any other error (EACCES, EPERM/TCC, unmounted volume) makes it
  "unreadable": report only, never deleted (and never pruned by `git worktree prune`).
* Every filesystem target is re-validated by `safety.Guard` right before removal: absolute clean
  path, inside `$HOME` or the per-user temp dir, not a protected path (credentials, AI tool
  configs, keystores, ssh…), not containing one, not a git repository — any `.git` entry (dir,
  file or symlink) or a bare repository (`HEAD` + `objects` + `refs`) — unless `AllowGitRepo`
  (set by the executor for `worktree` items, which git removes itself, and by items that opt in:
  `~/.cocoapods/repos`, verified orphan worktree dirs), symlinked parents resolved, not the
  current directory. Items may also require a marker file next to them (`RequireSibling`, e.g.
  `package.json` next to `node_modules`) and may refuse to run while a process is alive
  (`ProcessGuard`, e.g. Xcode, Simulator).
* Other apps' data (`fsx.AppDataProtected`): on macOS 14+, the first access to another app's
  container (`~/Library/Containers/<app>`, `~/Library/Group Containers/<group>`) from a process
  without Full Disk Access opens a system permission prompt and blocks the `open()`/`stat()` call
  until someone answers. Without Full Disk Access nothing below a container root is ever read,
  sized or cleaned: sizing returns `fsx.ErrNeedsFullDiskAccess`, catalog entries there become
  report-only items with a warning (`fsx.GlobPrefixProtected`), the containers provider skips
  them, and the analyzer does not enter them. The container roots themselves may be listed.
* Path comparisons (protect, exclude, roots, busy detection) are case-insensitive and
  Unicode-normalization-insensitive (`safety.Key`: NFC + lower case), like APFS. Config
  `exclude` / `protect` entries have `~` and `$VARS` expanded (an undefined variable is an error)
  and also match through symlinks (the resolved form of each entry is added). Roots are stored
  with their on-disk spelling (`~/code` typed for `~/Code`), so paths derived from them match the
  paths the kernel reports for processes (cwd, executables) in the in-use checks.
* Roots given on the command line (`lu-cleaner artifacts <root>`, `--root`) set
  `Env.ExplicitRoots`: the artifacts scanner then scans only `Env.Roots` (no worktree roots, no
  built-in extra folders), and the CLI drops any project artifact outside them. `/` is refused as
  a root. They narrow the artifacts scan only: the other providers get an `Env` with the
  configured (or auto-detected) roots and `ExplicitRoots` false, since they read the roots to
  learn what the projects use (Android SDK / NDK / Gradle versions, Ruby and node versions, main
  repositories of worktrees) — with `--root ~/one-project` alone, every version used by the other
  projects would look unused. Configured roots stay protected as a whole. Config exclusions are
  also enforced by the CLI on every provider's output, matching item paths emitted through a
  symlinked folder in their resolved form too.
* Human output never prints a raw control character: names, paths, warnings and messages go
  through a sanitizer (C0/C1 controls, DEL and bidi overrides become visible escapes such as
  `\x1b`); JSON output escapes them as `\uXXXX`.
* Signals: the first Ctrl-C / SIGTERM cancels the run (pending items are skipped, exit 130 / 143);
  a second one kills the process at once, even in the middle of a long deletion.
* Category totals (`scan`, `doctor`) overlap when items of one category lie inside items of
  another (artifacts inside worktrees): the shared part is shown ("inside Worktrees", JSON
  `shared_by_category`), and the global total counts it once.
