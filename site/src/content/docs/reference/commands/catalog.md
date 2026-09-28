---
title: "lu-cleaner catalog"
description: "List the well-known paths lu-cleaner knows about (audit)"
sidebar:
  order: 4
  label: "catalog"
---

:::note[Generated]
This page is generated from the CLI itself (`make docs`). Do not edit it by hand.
:::

List the catalog of well-known caches and tool data: id, category, risk,
cleaning method and path globs ("~" = home, $TMPDIR = per-user temp dir).
Filter with --category and --kind (substring of the id).

## Usage

```bash
lu-cleaner catalog
```

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

