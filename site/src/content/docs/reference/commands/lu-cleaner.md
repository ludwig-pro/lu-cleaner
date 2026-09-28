---
title: "lu-cleaner"
description: "Free disk space on a macOS dev machine (React Native, iOS, Android, AI worktrees)"
sidebar:
  order: 1
  label: "lu-cleaner"
---

:::note[Generated]
This page is generated from the CLI itself (`make docs`). Do not edit it by hand.
:::

lu-cleaner frees disk space on a macOS developer machine: git worktrees left
by AI agents (Codex, Cursor, Conductor, Claude Code), node_modules, Pods,
iOS/Android builds, simulators, emulators, package-manager caches, Xcode data
and AI tools data.

Without a command it opens the interactive dashboard (or prints the scan
report when the output is not a terminal). Nothing is ever deleted without an
explicit selection (picker) or --yes, and every path is re-checked by a safety
guard right before removal.

## Usage

```bash
lu-cleaner [flags]
```

## Examples

```bash
lu-cleaner                          # interactive dashboard
lu-cleaner scan                     # what takes space, grouped by category
lu-cleaner clean --yes --smart -n   # dry-run of the recommended cleanup
lu-cleaner artifacts ~/dev          # npkill-like: node_modules, Pods, builds…
lu-cleaner worktrees                # stale AI worktrees
lu-cleaner doctor                   # why is my disk still full?
```

## Options

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

