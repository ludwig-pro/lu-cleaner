# Architecture

```
cmd/lu-cleaner/          main (version via -ldflags)
internal/
  core/                  data model: Item, Risk, Method, Category, Provider, Env, Filter, Recommend, TopLevel
  fsx/                   fast concurrent directory sizing (allocated blocks, hardlink aware), human units
  sysx/                  disk free (statfs), running processes, APFS local snapshots
  safety/                Guard: the last line of defence before any deletion
  config/                ~/.config/lu-cleaner/config.toml
  engine/                runs providers concurrently, streams item upserts
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

* A provider never deletes anything. It only emits `*core.Item`.
* Emitting the same `Item.ID` again replaces the previous value (use it to show an item with
  `Sizing=true` first, then with its measured size). Never mutate an item after emitting it
  (emit `it.Clone()` if you keep working on it).
* `Item.Size` is the allocated size (`st_blocks*512`), hardlinks counted once; `Item.Reclaim`
  is set when deleting frees less than `Size` (pnpm hardlinks…).
* `Item.LastUsed` drives ages, `--older-than`, and smart selection of moderate items.
* Risk: `safe` (pure cache) → `moderate` (regenerable, costs time/bandwidth) → `caution`
  (user data/state) → `never` (report only).
* Methods: `delete` (default, frees space now), `trash` (does NOT free space until the Trash is
  emptied), `command` (tool's own cleanup: `xcrun simctl delete unavailable`…), `worktree`
  (`git worktree remove` + `prune`, refuses dirty/unpushed/locked unless `--force`), `report`.
* Every filesystem target is re-validated by `safety.Guard` right before removal: absolute clean
  path, inside `$HOME` or the per-user temp dir, not a protected path (credentials, AI tool
  configs, keystores, ssh…), not containing one, not a git repository (unless `AllowGitRepo`),
  symlinked parents resolved, not the current directory. Items may also require a marker file
  next to them (`RequireSibling`, e.g. `package.json` next to `node_modules`) and may refuse to
  run while a process is alive (`ProcessGuard`, e.g. Xcode, Simulator).
