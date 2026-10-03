---
title: "lu-cleaner worktrees"
description: "Git worktrees left by Codex, Cursor, Conductor, Claude Code…"
sidebar:
  order: 21
  label: "worktrees"
---

:::note[Generated]
This page is generated from the CLI itself (`make docs`). Do not edit it by hand.
:::

List the git worktrees created by AI agents and by hand, with their tool,
branch and status (clean, dirty, unpushed, orphan, merged), and pick what to
remove. Removal uses 'git worktree remove' and keeps the branch, so commits on
a branch (pushed or not) stay in the main repository. Without --force it
refuses a worktree that is locked, has uncommitted or untracked changes, has
commits on no branch (detached HEAD), or contains another worktree or
repository; --dry-run runs the same checks.

With --yes (no picker), worktrees of risk "caution" and worktrees with a
warning (ignored .env files that would be lost…) also need --risk caution.
Worktrees are never moved to the Trash: with --trash they are skipped.

## Usage

```bash
lu-cleaner worktrees [flags]
```

**Aliases:** `wt`

## Options

| Flag | Type | Default | Description |
|---|---|---|---|
| `-l`, `--list` |  |  | print the table instead of opening the picker |

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
