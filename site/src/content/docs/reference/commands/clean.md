---
title: "lu-cleaner clean"
description: "Select and clean (interactive picker, or --yes for scripts)"
sidebar:
  order: 5
  label: "clean"
---

:::note[Generated]
This page is generated from the CLI itself (`make docs`). Do not edit it by hand.
:::

Select what to clean, then clean it.

Without --yes, opens the interactive picker (recommended items preselected
unless --no-smart), pre-filtered by --category, --kind, --min-size,
--older-than and --risk.

With --yes, runs non-interactively: scan, filter, print the plan, clean.
A narrowing filter is mandatory (--smart, --category or --kind) and items of
risk "caution" (user data, dirty worktrees, sessions…) are only included with
an explicit --risk caution. Use --dry-run first.

## Usage

```bash
lu-cleaner clean [flags]
```

## Examples

```bash
lu-cleaner clean                                   # interactive picker
lu-cleaner clean --yes --smart --dry-run           # what smart select would do
lu-cleaner clean --yes --smart                     # clean the recommended items
lu-cleaner clean -y -c artifacts --older-than 30d  # stale node_modules, Pods, builds
lu-cleaner clean -y -k node_modules --min-size 200MB
```

## Options

| Flag | Type | Default | Description |
|---|---|---|---|
| `--no-smart` |  |  | do not preselect (picker) / keep only (--yes) recommended items |

## Global options

| Flag | Type | Default | Description |
|---|---|---|---|
| `-c`, `--category` | stringSlice |  | only these categories (repeatable, comma-separated): worktrees, artifacts, simulators, xcode, android, ai, js, ide, containers, langs, system |
| `-n`, `--dry-run` |  |  | show what would be cleaned, delete nothing |
| `--force` |  |  | ignore running-app guards and dirty/unpushed worktree checks |
| `--json` |  |  | machine-readable JSON output |
| `-k`, `--kind` | stringSlice |  | only these item kinds or provider ids (repeatable, comma-separated) |
| `--min-size` | string |  | hide items smaller than this, e.g. 100MB (default: config min_size; 0 for clean --yes) |
| `--no-color` |  |  | disable colors (also honours NO_COLOR) |
| `--older-than` | string |  | only items unused for longer than this, e.g. 30d, 2w, 6m |
| `--risk` | string |  | highest risk allowed: safe\|moderate\|caution (default: moderate for clean --yes, everything otherwise) |
| `--root` | stringArray |  | project root to scan for artifacts (repeatable, overrides config roots) |
| `--smart` |  |  | only recommended items (safe caches, stale regenerable data) |
| `--trash` |  |  | move to ~/.Trash instead of deleting (space is freed only once the Trash is emptied) |
| `-v`, `--verbose` |  |  | debug logging on stderr |
| `-y`, `--yes` |  |  | clean without the interactive picker |

