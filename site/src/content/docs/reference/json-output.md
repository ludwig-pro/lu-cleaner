---
title: JSON output
description: Schemas of scan --json, clean --yes --json, doctor --json and history --json, with anonymized examples, exit codes and stability notes for scripts.
sidebar:
  order: 5
---

Every command that reports something accepts `--json`. Use it in scripts, CI jobs and AI agents instead of parsing the tables, which are meant for humans and change with the terminal width.

```bash
lu-cleaner scan --json | jq '.totals'
lu-cleaner clean --yes --smart --dry-run --json | jq '.results | length'
```

## General rules

- The JSON document goes to **stdout**, indented. Progress lines, provider warnings and `--verbose` debug output go to **stderr**, so `2>/dev/null` is safe.
- **Sizes** are integers in bytes. Human-readable sizes only appear inside `meta` and `note` strings, in decimal units (`1.34 GB` = 1,340,000,000 bytes).
- **Dates** are RFC 3339 strings. `generated_at` is in UTC; `last_used` keeps the local offset.
- **Durations** are milliseconds when the field name ends in `_ms`; the `took` fields of `clean` are nanoseconds.
- **Risk** is one of `safe`, `moderate`, `caution`, `never`. **Method** is one of `delete`, `trash`, `command`, `worktree`, `report`.
- Optional fields are omitted when empty, unless stated otherwise.

## `scan --json`

The scan report. The same document is produced by `lu-cleaner --json` without a command, and by `artifacts`, `worktrees` and `devices` with `--json` (restricted to their categories).

```bash
lu-cleaner scan --json                    # every item
lu-cleaner scan --json --top 5            # at most 5 items per category
lu-cleaner scan --json --smart            # recommended items only
lu-cleaner scan --json -c worktrees,artifacts --older-than 30d
```

Without `--top`, the JSON lists **every** item that passes the filters (the text report shows 15 per category). As in the text report, items smaller than `min_size` (1 MB by default) are hidden unless you pass `--min-size 0`, and report-only items are included.

### Top-level fields

| Field | Type | Description |
|---|---|---|
| `version` | string | lu-cleaner version that produced the document |
| `generated_at` | string | Scan time, UTC, to the second |
| `disk` | object | Home volume: `total`, `free` (available to you), `used`, in bytes |
| `items` | array | Items, grouped by category in display order, sorted by `--sort` (size by default) within each category |
| `totals.size` | integer | What cleaning every matching cleanable item would free (really-freed sizes, nested paths counted once) |
| `totals.recommended` | integer | The same for recommended items |
| `totals.items` | integer | Number of cleanable items |
| `totals.by_category` | object | Freeable bytes per category ID. Values overlap when items of one category lie inside items of another (artifacts inside worktrees) |
| `totals.shared_by_category` | object | For each category with such an overlap, the part of its bytes inside items of another category. `size` counts those bytes once. Always present, `{}` when none |
| `errors` | object | Scanner ID → error message, for scanners that failed. Always present, `{}` when none |
| `timing_ms` | object | Scanner ID → scan duration in milliseconds |
| `took_ms` | integer | Total scan duration in milliseconds |

Totals are computed over all matching items, even when `--top` shortens `items`.

### Item fields

| Field | Type | Description |
|---|---|---|
| `id` | string | Stable identifier, usually `<scanner>:<kind>:<path>` |
| `provider` | string | Scanner ID (`worktrees`, `artifacts`, `apple`, `android`, `ai`, `js`, `system`, `catalog`) |
| `category`, `category_title` | string | Category ID and display title |
| `kind` | string | Item kind, as accepted by `--kind`. See [What gets scanned](/lu-cleaner/reference/scanners/) |
| `name` | string | Human label |
| `path` | string | The path removed, for single-path items |
| `paths` | array | The paths removed, for group items (then `path` is absent) |
| `location` | string | Where a group or command item acts, for display |
| `size` | integer | Allocated bytes, hardlinks counted once |
| `reclaim` | integer | Bytes really freed, only present when less than `size` (data shared with files outside the item through hardlinks or APFS clones) |
| `freed` | integer | Best estimate of bytes freed: `reclaim` when present, else `size`. Always present |
| `files` | integer | Number of files |
| `last_used` | string | Best estimate of the last activity |
| `age_days` | number or null | Days since `last_used`, one decimal; `null` when unknown |
| `risk` | string | `safe`, `moderate`, `caution` or `never` |
| `method` | string | `delete`, `trash`, `command`, `worktree` or `report` |
| `command` | array | Command run by `command` items, as argv |
| `post_commands` | array | Commands run after the main action (array of argv) |
| `process_guard` | array | Processes that must not run when cleaning |
| `require_sibling` | array | Marker files that must still exist next to the path |
| `project` | string | Project or main repository the item belongs to |
| `meta` | object | Scanner-specific details, all values are strings |
| `note` | string | What the item is and how it comes back |
| `warn` | string | Warning; an item with a warning is never recommended, and `clean --yes` holds it back unless `--risk caution` |
| `require_force` | boolean | `true` when cleaning refuses the item without `--force`, in a dry run too (orphaned worktree folders). Omitted otherwise |
| `recommended` | boolean | Recommended by smart select: what the `a` key and `--smart` pick. Always present |
| `cleanable` | boolean | `false` for report-only, non-selectable and `never` items. Always present |

### Example

Abridged and anonymized: three items out of a full scan.

```json
{
  "version": "v0.1.0",
  "generated_at": "2026-09-28T09:12:44Z",
  "disk": { "total": 500000000000, "free": 43000000000, "used": 457000000000 },
  "items": [
    {
      "id": "worktrees:worktree:/Users/me/.codex/worktrees/a1b2/my-app",
      "provider": "worktrees",
      "category": "worktrees",
      "kind": "codex-worktree",
      "name": "my-app · feature/login",
      "path": "/Users/me/.codex/worktrees/a1b2/my-app",
      "size": 2310000000,
      "files": 187000,
      "risk": "moderate",
      "method": "worktree",
      "project": "/Users/me/dev/my-app",
      "meta": {
        "artifacts": "1.95 GB",
        "branch": "feature/login",
        "checkout": "360 MB",
        "dirty": "0",
        "ios/Pods": "610 MB",
        "locked": "false",
        "main": "/Users/me/dev/my-app",
        "merged": "true",
        "node_modules": "1.34 GB",
        "status": "merged",
        "tool": "codex",
        "unpushed": "0"
      },
      "note": "Codex worktree: git worktree remove deletes the checkout and keeps branch feature/login in my-app; Codex threads using it lose their folder (archiving them in Codex snapshots first).",
      "last_used": "2026-09-19T17:40:02+02:00",
      "category_title": "Worktrees",
      "age_days": 8.6,
      "recommended": true,
      "cleanable": true,
      "freed": 2310000000
    },
    {
      "id": "artifacts:node_modules:/Users/me/dev/shop-app/node_modules",
      "provider": "artifacts",
      "category": "artifacts",
      "kind": "node_modules",
      "name": "shop-app › node_modules",
      "path": "/Users/me/dev/shop-app/node_modules",
      "size": 1210000000,
      "reclaim": 84000000,
      "files": 98000,
      "risk": "moderate",
      "method": "delete",
      "require_sibling": ["package.json"],
      "project": "/Users/me/dev/shop-app",
      "meta": {
        "git": "ignored",
        "package_manager": "pnpm",
        "project_type": "expo",
        "reclaim": "84.0 MB"
      },
      "note": "Installed JS dependencies; reinstall with `pnpm i` when you need the project again (minutes, network). Hardlinked with the pnpm store: only 84.0 MB is really freed.",
      "last_used": "2026-07-02T10:03:11+02:00",
      "category_title": "Project artifacts",
      "age_days": 88.1,
      "recommended": true,
      "cleanable": true,
      "freed": 84000000
    },
    {
      "id": "apple:ios-simulator:6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B",
      "provider": "apple",
      "category": "simulators",
      "kind": "ios-simulator",
      "name": "iPhone 16 Pro · iOS 18.6",
      "location": "/Users/me/Library/Developer/CoreSimulator/Devices/6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B",
      "size": 3400000000,
      "files": 41000,
      "risk": "caution",
      "method": "command",
      "command": ["xcrun", "simctl", "delete", "6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B"],
      "meta": {
        "apps": "com.example.myapp",
        "device_type": "iPhone 16 Pro",
        "runtime": "iOS 18.6",
        "state": "Shutdown",
        "udid": "6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B"
      },
      "note": "Simulator with 1 installed app(s) and their data (com.example.myapp): deleting it loses app data, logins and settings — reinstall the dev build afterwards. …",
      "last_used": "2026-08-30T11:22:05+02:00",
      "category_title": "iOS Simulators",
      "age_days": 29,
      "recommended": false,
      "cleanable": true,
      "freed": 3400000000
    }
  ],
  "totals": {
    "size": 61200000000,
    "recommended": 23400000000,
    "items": 214,
    "by_category": { "artifacts": 18300000000, "simulators": 21500000000, "worktrees": 21400000000 },
    "shared_by_category": { "artifacts": 1280000000 }
  },
  "errors": {},
  "timing_ms": { "apple": 5120, "artifacts": 9870, "worktrees": 11240 },
  "took_ms": 11251
}
```

Useful queries:

```bash
# recommended items, biggest first
lu-cleaner scan --json --smart | jq -r '.items | sort_by(-.freed)[] | "\(.freed)\t\(.kind)\t\(.name)"'

# worktrees whose status is clean or merged
lu-cleaner scan --json -c worktrees | jq '.items[] | select(.meta.status == "clean" or .meta.status == "merged") | .path'

# how much each category could free
lu-cleaner scan --json | jq '.totals.by_category'
```

## `clean --yes --json`

The result of a non-interactive clean or dry run. `--json` requires `--yes` (the interactive picker has no JSON output). `artifacts`, `worktrees` and `devices` produce the same document with `--yes --json`. Items held back because of a warning are not in the document: a `warning: N items with a warning left out (…)` line goes to stderr instead.

```bash
lu-cleaner clean --yes --smart --dry-run --json
lu-cleaner clean --yes -c artifacts --older-than 30d --json
```

### Summary fields

| Field | Type | Description |
|---|---|---|
| `results` | array | One entry per processed item, in completion order. `[]` when nothing matched |
| `estimated_freed` | integer | Space really freed: sum of `freed` over the `done` results, plus what failed deletions freed before failing. Moves to the Trash are never counted. Always `0` in a dry run |
| `trashed` | integer | Bytes moved to the Trash by `done` results: still used until the Trash is emptied. Always present |
| `disk_before`, `disk_after` | object | Home volume `total`, `free`, `used` before and after the run |
| `measured_freed` | integer | `disk_after.free − disk_before.free`: what the filesystem reports. Can be lower than estimated (snapshots, Trash, open files) and is noise in a dry run |
| `trash` | boolean | Trash mode: files and folders were moved to the Trash, worktree and command items were skipped |
| `dry_run` | boolean | Nothing was executed |
| `took` | integer | Total duration, in nanoseconds |

### Result fields

| Field | Type | Description |
|---|---|---|
| `item` | object | The item, with the same fields as in `scan --json` minus the computed ones (`category_title`, `age_days`, `cleanable`, `freed`). `recommended` only appears when the scanner forced it |
| `status` | string | `done`, `dry-run`, `skipped` or `failed` |
| `method` | string | Method actually used, or that would be in a dry run: `trash` for a `delete` item cleaned in Trash mode |
| `freed` | integer | Estimated bytes really freed for `done` and `dry-run` results, what a `failed` deletion freed before failing, `0` otherwise. A move to the Trash frees nothing: its size is in `trashed` |
| `trashed` | integer | Bytes moved to the Trash (or that would be, in a dry run). Omitted when `0` |
| `message` | string | What happened (`would delete …`, `would run: xcrun simctl delete …`, `would move to Trash: …`, `moved to …`, `git worktree removed (branch kept)`, `orphaned worktree directory removed`) or why the item was skipped (`needs --force: …`, `not possible in Trash mode (it would delete permanently)`…) |
| `error` | string | Failure reason, for `failed` results |
| `took` | integer | Duration, in nanoseconds |

### Example

`lu-cleaner clean --yes -c js,xcode --risk caution --dry-run --json` with Xcode open, abridged to two results. Without `--risk caution`, the DerivedData item, which carries a warning, would be held back instead of skipped:

```json
{
  "results": [
    {
      "item": {
        "id": "catalog:js-npm-cache",
        "provider": "catalog",
        "category": "js",
        "kind": "js-npm-cache",
        "name": "npm cache (_cacache)",
        "path": "/Users/me/.npm/_cacache",
        "size": 1140000000,
        "files": 23000,
        "last_used": "2026-09-28T10:41:12+02:00",
        "risk": "safe",
        "method": "delete",
        "note": "npm's content cache; the next `npm install` / `npx` re-downloads what it needs. Existing node_modules are unaffected."
      },
      "status": "dry-run",
      "method": "delete",
      "freed": 1140000000,
      "message": "would delete /Users/me/.npm/_cacache",
      "took": 8167084
    },
    {
      "item": {
        "id": "apple:xcode-derived-data:/Users/me/Library/Developer/Xcode/DerivedData/MyApp-abcdefghijklmnopqrstuvwxyzab",
        "provider": "apple",
        "category": "xcode",
        "kind": "xcode-derived-data",
        "name": "MyApp",
        "path": "/Users/me/Library/Developer/Xcode/DerivedData/MyApp-abcdefghijklmnopqrstuvwxyzab",
        "size": 4800000000,
        "risk": "safe",
        "method": "delete",
        "process_guard": ["Xcode", "xcodebuild"],
        "warn": "Xcode is running — quit it before cleaning"
      },
      "status": "skipped",
      "method": "delete",
      "freed": 0,
      "message": "Xcode is running — quit it first (or use --force)",
      "took": 2140
    }
  ],
  "estimated_freed": 0,
  "trashed": 0,
  "disk_before": { "total": 500000000000, "free": 43000000000, "used": 457000000000 },
  "disk_after": { "total": 500000000000, "free": 42999920000, "used": 457000080000 },
  "measured_freed": -80000,
  "trash": false,
  "dry_run": true,
  "took": 383662875
}
```

In a dry run, add up the per-result `freed` values to get what the run would free (and `trashed` for what it would move to the Trash):

```bash
lu-cleaner clean --yes --smart --dry-run --json \
  | jq '[.results[] | select(.status == "dry-run") | .freed] | add'
```

## `doctor --json`

Disk health and the reasons space may not come back.

```bash
lu-cleaner doctor --json            # includes a scan summary by category
lu-cleaner doctor --json --no-scan  # disk, snapshots, Trash and running apps only
```

| Field | Type | Description |
|---|---|---|
| `version`, `generated_at` | string | As in `scan --json` |
| `disk` | object | Home volume `total`, `free`, `used` |
| `used_pct` | number | Used percentage, one decimal |
| `snapshots` | array | Names of APFS local (Time Machine) snapshots of the root volume |
| `trash` | object | `path`, `size`, `files`, `readable` (`false` without Full Disk Access), optional `note` |
| `running` | array | Apps that block some cleanups: `app`, `processes`, `impact` |
| `scanned` | boolean | `false` with `--no-scan` |
| `categories` | array | Per category: `id`, `title`, `size`, `recommended`, `items`, and `shared` (bytes inside items of another category) when not `0`. Empty with `--no-scan` |
| `total`, `recommended` | integer | Freeable bytes, all and recommended |
| `errors` | object | Scanner errors, when any |
| `use_trash` | boolean | `use_trash` setting of the configuration |
| `tips` | array | Explanations of why freed space may not show up |

```json
{
  "version": "v0.1.0",
  "generated_at": "2026-09-28T09:20:03Z",
  "disk": { "total": 500000000000, "free": 43000000000, "used": 457000000000 },
  "used_pct": 91.4,
  "snapshots": [],
  "trash": { "path": "/Users/me/.Trash", "size": 0, "files": 0, "readable": false, "note": "cannot read the Trash (grant Full Disk Access to your terminal)" },
  "running": [
    { "app": "Simulator", "processes": ["Simulator", "launchd_sim"], "impact": "booted simulators and runtimes cannot be deleted" }
  ],
  "scanned": false,
  "categories": [],
  "total": 0,
  "recommended": 0,
  "use_trash": false,
  "tips": ["APFS local snapshots pin the blocks of deleted files until they expire (about 24h) or are thinned.", "…"]
}
```

## `history --json`

What past cleans did, read from `~/.local/state/lu-cleaner/history.jsonl`.

```bash
lu-cleaner history --json             # the last 30 entries
lu-cleaner history --json --limit 0   # all entries
```

| Field | Type | Description |
|---|---|---|
| `path` | string | The history file |
| `total` | integer | Number of entries in the file |
| `freed` | integer | Sum of `freed` over all entries of the file (not only the ones shown): the space really freed. Moves to the Trash are not counted |
| `trashed` | integer | Sum of `size` over the `done` moves to the Trash: still used until the Trash is emptied |
| `entries` | array | Newest first, at most `--limit` (30 by default, `0` = all) |

Each entry has these fields:

| Field | Description |
|---|---|
| `time`, `kind`, `category`, `name` | When, and which item |
| `path` | The path removed, for single-path items |
| `location`, `paths`, `count` | For a group item: its display location, its first 50 paths and the total number of paths |
| `command` | The command run, for command items |
| `method` | Method actually used: `trash` when Trash mode turned a deletion into a move |
| `status` | `done`, `skipped` or `failed`; dry runs are never recorded |
| `size` | The item's measured size |
| `freed` | Bytes really freed: less than `size` when data is shared through hardlinks or APFS clones, what a failed deletion freed before failing, `0` for moves to the Trash, skipped items and other failures. Always present |
| `error`, `message` | Failure reason, and what happened or why the item was skipped |

```json
{
  "entries": [
    {
      "time": "2026-09-27T18:46:23.836594+02:00",
      "kind": "ios-simulator",
      "category": "simulators",
      "name": "iPad Air 11-inch (M2) · iOS 18.6",
      "command": "xcrun simctl delete A1B2C3D4-0000-4000-8000-000000000001",
      "method": "command",
      "status": "done",
      "size": 18341888,
      "freed": 18341888
    },
    {
      "time": "2026-09-27T18:46:20.112004+02:00",
      "kind": "node_modules",
      "category": "artifacts",
      "name": "shop-app › node_modules",
      "path": "/Users/me/dev/shop-app/node_modules",
      "method": "delete",
      "status": "done",
      "size": 1210000000,
      "freed": 84000000
    }
  ],
  "freed": 37100000000,
  "path": "/Users/me/.local/state/lu-cleaner/history.jsonl",
  "total": 88,
  "trashed": 0
}
```

The file itself is JSON Lines with the same entry format, oldest first, so you can also read it directly: `jq -s 'map(select(.status == "done")) | length' ~/.local/state/lu-cleaner/history.jsonl`. Lines written by older versions have no `freed` field: `history` then assumes the full size for a done permanent removal and `0` otherwise.

## Other commands

| Command | Output |
|---|---|
| `catalog --json` | Array of catalog entries: `id`, `category`, `name`, `paths`, `risk`, `method`, `mode` (`group` or `each`) and, when set, `exclude`, `command`, `requires`, `process_guard`, `note`, `older_than`, `keep_latest`, `allow_git_repo`, `recommended`, `min_bytes`, `files` |
| `config show --json` | Effective configuration: `path`, `exists`, `config` (the file's values), resolved `roots` and `roots_source`, `worktree_roots`, `exclude`, `protect`, `missing` (the `exclude` and `protect` entries that do not exist), `stale_after`, `min_size` (bytes), `disabled_categories`, `state_dir`, `history_file`, `clean` (`trash`, `force`) |
| `analyze [path] --json` | `root`, `total` and `entries` (every direct child: `name`, `path`, `dir`, `size`, `files`, `unreadable`) |
| `version --json` | `version`, `go`, `os`, `arch` |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. Items skipped for safety reasons do not change the exit code; check `results[].status`. |
| `1` | Runtime failure: an invalid configuration file, an I/O error, or at least one item `failed` during a clean. |
| `2` | Usage error: unknown command or flag, invalid value (`--min-size`, `--older-than`, `--risk` including `never`, `--category` including a tool name such as `cursor`, `--sort`, `--top`, `--limit`, an unknown `artifacts --target` kind), `clean --yes` without `--smart`, `-c` or `-k` (or with only a `-k` naming a scanner that spans several categories), `clean --json` without `--yes`, `clean` without a terminal and without `--yes`, a `--root` that is not a directory or is `/`, a category disabled in the configuration. |
| `130` | Interrupted by <kbd>Ctrl</kbd>+<kbd>C</kbd>. |
| `143` | Interrupted by `SIGTERM`. |

A scanner that fails does not change the exit code either: the scan continues, the error is printed on stderr and listed in `errors`.

## Stability

- The documents have no schema version of their own. `version` tells you which lu-cleaner produced them; pin it in CI if you depend on details.
- Expect **additive** changes in any release: new fields, new item kinds, new `meta` keys, new categories. Write consumers that ignore what they do not know.
- `kind`, `category`, `risk`, `method` and `status` values are identifiers meant for scripts. `name`, `note`, `warn`, `message` and every `meta` value are human text and may change wording.
- `meta` keys depend on the scanner and on what it could observe; test for presence instead of assuming a key exists.
- Text output (tables, colors, the `★` column) is for humans only. It is plain and emoji-free when stdout is not a terminal, but its layout is not a contract. Neither text nor JSON ever carries a raw control character from a file name: text shows a visible escape (`\x1b`), JSON escapes it as `\u001b`.

See [Automation](/lu-cleaner/guides/automation/) for complete scripts.
