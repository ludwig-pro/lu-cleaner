---
title: "lu-cleaner config"
description: "Show, create or edit the configuration file"
sidebar:
  order: 6
  label: "config"
---

:::note[Generated]
This page is generated from the CLI itself (`make docs`). Do not edit it by hand.
:::

The configuration lives in ~/.config/lu-cleaner/config.toml ($XDG_CONFIG_HOME,
or $LU_CLEANER_CONFIG). Every key is optional: run 'lu-cleaner config init'
for a commented sample.

## Usage

```bash
lu-cleaner config
```

## Subcommands

| Command | Description |
|---|---|
| `lu-cleaner config edit` | Open the config in $VISUAL / $EDITOR (default: open -t) |
| `lu-cleaner config init` | Write a commented sample config (if none exists) |
| `lu-cleaner config path` | Print the config file path |
| `lu-cleaner config show` | Print the effective configuration, with resolved roots |

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

