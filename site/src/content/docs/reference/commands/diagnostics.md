---
title: "lu-cleaner diagnostics"
description: "Manage optional technical error reports (off until explicit consent)"
sidebar:
  order: 12
  label: "diagnostics"
---

:::note[Generated]
This page is generated from the CLI itself (`make docs`). Do not edit it by hand.
:::

Manage optional technical error reports (off until explicit consent).

## Usage

```bash
lu-cleaner diagnostics
```

## Subcommands

| Command | Description |
|---|---|
| `lu-cleaner diagnostics disable` | Revoke consent and remove the local report |
| `lu-cleaner diagnostics enable` | Read the notice and explicitly consent to technical reports |
| `lu-cleaner diagnostics export` | Print the last sanitized local report as JSON (no upload) |
| `lu-cleaner diagnostics status` | Show consent and the configured recipient |

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
| `--scan-mode` | string | `eco` | scan resource profile: eco\|fast (default: config scan_mode, or eco) |
| `--smart` |  |  | recommended items only: preselected in the picker (nothing is preselected otherwise), the only ones cleaned with --yes |
| `--trash` |  |  | move files and folders to ~/.Trash instead of deleting them (space is freed only once the Trash is emptied); worktrees and commands are skipped. --trash=false overrides use_trash |
| `-v`, `--verbose` |  |  | debug logging on stderr |
| `-y`, `--yes` |  |  | clean without the interactive picker |
