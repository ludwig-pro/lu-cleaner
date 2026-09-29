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

Without --yes, opens the interactive picker, pre-filtered by --category,
--kind, --min-size, --older-than and --risk. Nothing is preselected: you
choose what to remove (space; "a" selects the recommended items). Pass
--smart to start with the recommended items preselected.

With --yes, runs non-interactively: scan, filter, print the plan, clean.
A narrowing filter is mandatory (--smart, --category or --kind). Items of
risk "caution" (user data, dirty worktrees, sessions…) and items the scan
flagged with a warning (in use by a running process, ignored .env files that
would be lost, runtime used by booted simulators…) are only included with an
explicit --risk caution. Use --dry-run first.

With --trash (or use_trash), files and folders go to the Trash; worktrees and
commands (simctl, docker, brew…) cannot be undone, so they are skipped.

## Usage

```bash
lu-cleaner clean
```

## Examples

```bash
lu-cleaner clean                                   # interactive picker, nothing preselected
lu-cleaner clean --smart                           # picker with the recommended items preselected
lu-cleaner clean --yes --smart --dry-run           # what smart select would do
lu-cleaner clean --yes --smart                     # clean the recommended items
lu-cleaner clean -y -c artifacts --older-than 30d  # stale node_modules, Pods, builds
lu-cleaner clean -y -k node_modules --min-size 200MB
```

## Global options

| Flag | Type | Default | Description |
|---|---|---|---|
| `-c`, `--category` | stringSlice |  | only these categories (repeatable, comma-separated): worktrees, artifacts, simulators, xcode, android, ai, js, ide, containers, langs, system |
| `-n`, `--dry-run` |  |  | show what would be cleaned, delete nothing |
| `--force` |  |  | ignore running-app and in-use guards, and the worktree checks (locked, uncommitted changes, commits on no branch, nested worktrees) |
| `--json` |  |  | machine-readable JSON output |
| `-k`, `--kind` | stringSlice |  | only these item kinds or provider ids (repeatable, comma-separated) |
| `--min-size` | string |  | hide items smaller than this, e.g. 100MB (default: config min_size; 0 for clean --yes) |
| `--no-color` |  |  | disable colors (also honours NO_COLOR) |
| `--older-than` | string |  | only items unused for longer than this, e.g. 30d, 2w, 6m |
| `--risk` | string |  | highest risk allowed: safe\|moderate\|caution (default: moderate for clean --yes, everything otherwise); with --yes, caution also admits items with a warning |
| `--root` | stringArray |  | project root to scan for artifacts (repeatable): replaces the config roots for the artifacts scan only, the other scanners keep them to see what your projects use |
| `--smart` |  |  | recommended items only: preselected in the picker (nothing is preselected otherwise), the only ones cleaned with --yes |
| `--trash` |  |  | move files and folders to ~/.Trash instead of deleting them (space is freed only once the Trash is emptied); worktrees and commands are skipped. --trash=false overrides use_trash |
| `-v`, `--verbose` |  |  | debug logging on stderr |
| `-y`, `--yes` |  |  | clean without the interactive picker |

