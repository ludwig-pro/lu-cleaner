---
title: Keyboard shortcuts
description: Every key of the interactive picker (dashboard, clean, worktrees, artifacts, devices) and of the disk analyzer (analyze), including the confirmation dialogs.
sidebar:
  order: 4
---

lu-cleaner has two interactive screens:

- **The picker** opens with `lu-cleaner`, `lu-cleaner clean`, `lu-cleaner devices`, `lu-cleaner worktrees` and `lu-cleaner artifacts`. The last two skip the category overview and open directly on the item list.
- **The analyzer** opens with `lu-cleaner analyze [path]`: an ncdu-like explorer of any directory tree.

Press `?` in either one to show the built-in help; any key closes it. Arrow keys and their Vim equivalents both work everywhere.

## Picker

### Navigation

| Key | Action |
| --- | --- |
| `↑` / `k` | Move up. |
| `↓` / `j` | Move down. |
| `pgup` / `ctrl+b` | Page up. |
| `pgdown` / `ctrl+f` / `ctrl+d` | Page down. |
| `home` / `g` | First row. |
| `end` / `G` | Last row. |
| `→` / `l` / `enter` | Category overview: open the category. Item list: show or hide the details panel. |
| `←` / `h` / `esc` / `backspace` | Back to the category overview. |
| `tab` / `shift+tab` | Next / previous category, from an item list. |

In `worktrees` and `artifacts`, which have no category overview, `←`, `tab` and `shift+tab` do nothing.

### Selection

| Key | Action |
| --- | --- |
| `space` | Item list: select or unselect the item under the cursor. Category overview: select every cleanable item of the category **except** <span class="risk caution">caution</span> ones; if something in the category is already selected, unselect the whole category instead. |
| `a` | Smart select the view: recommended items on, every other item off. |
| `A` | Select every cleanable item of the view, <span class="risk caution">caution</span> items included. |
| `n` | Unselect every item of the view. |
| `i` | Invert the selection of the view. |

"The view" is the current item list (after any text filter) when a category is open, and every visible item when you are on the category overview. Report-only items can never be selected: `space` on one shows why in the status line.

Smart select also runs once, automatically, when the scan finishes: always with `lu-cleaner`, with `lu-cleaner clean` unless you pass `--no-smart`, and with `worktrees`, `artifacts` and `devices` only when you pass `--smart`. See [Risk levels and smart select](/lu-cleaner/concepts/risk-and-smart-select/).

### View

| Key | Action |
| --- | --- |
| `s` | Item list: cycle the sort between size (largest first), age (oldest first) and name. Category overview: toggle between the default order and size. |
| `/` | Filter by text (see [Text filter](#text-filter)). |
| `H` / `.` | Show or hide empty entries that cannot be selected (size 0, informational only). They are hidden by default. |

### Actions

| Key | Action |
| --- | --- |
| `d` / `x` | Clean the selection. Opens the [confirmation dialog](#confirmation-dialog). |
| `t` | Toggle Trash mode: selected items are moved to `~/.Trash` instead of being deleted. Space is only freed once you empty the Trash. |
| `o` | Reveal the item under the cursor in Finder (item list only). |
| `?` | Help overlay. It also lists the scanners that failed, if any. |
| `q` / `ctrl+c` | Quit. |

### Text filter

`/` opens a filter line at the bottom of the screen. It matches, case-insensitively, anywhere in the name, location, kind, project or scanner id of an item.

| Key | Action |
| --- | --- |
| Any character | Add to the filter. The list updates as you type. |
| `backspace` | Delete the last character. |
| `alt+backspace` / `ctrl+w` | Delete the last word. |
| `ctrl+u` | Clear the filter line. |
| `↑` / `↓` | Move in the list while typing. |
| `enter` | Keep the filter and go back to the list. The breadcrumb shows `filter "…"`. |
| `esc` | Clear the filter and go back to the list. |

A kept filter stays active while you browse. Press `esc` on the category overview (or in the `worktrees` and `artifacts` lists) to clear it.

### Confirmation dialog

The keys depend on whether the selection contains <span class="risk caution">caution</span> items.

| Selection | Confirm | Cancel |
| --- | --- | --- |
| No caution item | `y` or `enter` | `n`, `esc` or `q` |
| At least one caution item | type `yes`, then `enter` | `esc` |

While typing `yes`, `backspace`, `alt+backspace`, `ctrl+w` and `ctrl+u` edit the input. `ctrl+c` quits lu-cleaner without cleaning anything.

### While cleaning and after

| Screen | Key | Action |
| --- | --- | --- |
| Progress | `ctrl+c` | Cancel: items in progress finish, the remaining ones are skipped as `cancelled`. Other keys are ignored. |
| Summary | any key | Back to the lists. Cleaned items disappear; skipped and failed items stay selected. |
| Summary | `ctrl+c` | Quit. |

## Analyzer

`lu-cleaner analyze` starts in your home folder; pass a path to start elsewhere. Entries are sorted by size and measured in the background.

### Navigation

| Key | Action |
| --- | --- |
| `↑` / `k`, `↓` / `j` | Move up / down. |
| `pgup` / `ctrl+b`, `pgdown` / `ctrl+f` / `ctrl+d` | Page up / down. |
| `home` / `g`, `end` / `G` | First / last entry. |
| `→` / `l` / `enter` | Open the directory under the cursor. Symlinks are never followed. |
| `←` / `h` / `backspace` | Go to the parent directory, up to `/`. |

### Marking and deleting

| Key | Action |
| --- | --- |
| `space` | Mark or unmark the entry under the cursor, then move down. |
| `esc` | Clear every mark. |
| `d` / `x` / `delete` | Delete the marked entries, or the entry under the cursor when nothing is marked. |

`delete` is the forward-delete key (`fn` + `⌫` on a Mac keyboard). The `⌫` key alone sends `backspace`, which goes to the parent directory.

Deleting always asks you to type `yes` and press `enter` (`esc` cancels), because anything can be selected in the analyzer. Every path still goes through the [safety guard](/lu-cleaner/concepts/safety/), and a linked git worktree is removed with `git worktree remove` instead of being deleted. `--dry-run` and `--trash` apply here too.

While a deletion runs, `ctrl+c` cancels the remaining entries and quits once the ones in progress are done. On the result screen, any key returns to the list.

### View and actions

| Key | Action |
| --- | --- |
| `s` | Cycle the sort between size (largest first), name and age (oldest first). |
| `.` | Hide or show hidden files (dotfiles). They are shown by default. |
| `r` | Rescan the current directory. |
| `o` | Reveal the entry under the cursor in Finder. |
| `?` | Help overlay. |
| `q` / `ctrl+c` | Quit. |

## See also

- [Your first cleanup](/lu-cleaner/getting-started/first-cleanup/): the picker, screen by screen.
- [`lu-cleaner analyze`](/lu-cleaner/reference/commands/analyze/) and [`lu-cleaner clean`](/lu-cleaner/reference/commands/clean/): command-line flags.
