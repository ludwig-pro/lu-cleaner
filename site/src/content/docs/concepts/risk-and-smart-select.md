---
title: Risk levels and smart select
description: The four risk levels, the exact rules smart select uses to preselect items, how to pick everything yourself, and the five cleaning methods.
sidebar:
  order: 2
---

Every item lu-cleaner finds carries a **risk** (what you lose if it is deleted) and a **method** (how it is cleaned). Smart select combines the risk, the age and the scanner's own judgment to preselect what a careful developer would remove without a second thought.

## The four risk levels

| Risk | Meaning | Examples |
|---|---|---|
| <span class="risk safe">safe</span> | Pure cache. Regenerated automatically, holds no user data. At worst the next build or install is slower. | npm cache (`~/.npm/_cacache`), Metro cache, Xcode DerivedData, Gradle daemon logs, a project's Android `build/` folder, XCTest screen recordings, unavailable simulators |
| <span class="risk moderate">moderate</span> | Regenerable, but it costs time or bandwidth to get back. | `node_modules`, `ios/Pods`, a clean git worktree (its branch is kept), simulator runtimes (about 8 GB to re-download), unused node versions, Gradle wrapper distributions, NDKs |
| <span class="risk caution">caution</span> | May hold data or state you care about. | A worktree with uncommitted changes, a simulator with installed apps and their data, Claude Code or Codex sessions older than 30 days, Xcode archives, Android emulators (AVDs), a virtualenv without a requirements file, large git-ignored folders no rule knows, old installers in `~/Downloads` |
| <span class="risk never">never</span> | Protected or out of reach. Shown for information, never offered for deletion. | Time Machine local snapshots, swap files, staged macOS updates, chat databases of AI tools (Cursor global state, Conductor, Warp), a `build/` folder that turned out to hold source code |

A scanner can raise an item's risk based on what it observes. For example, a worktree is <span class="risk moderate">moderate</span> by default and becomes <span class="risk caution">caution</span> as soon as it has uncommitted changes, is locked, is open in an editor, has a process working inside it or has an active agent session. A node version becomes <span class="risk caution">caution</span> when it is your default version or the `node` on your `PATH`.

### What each level changes

| | Picker | Smart select | `clean --yes` |
|---|---|---|---|
| <span class="risk safe">safe</span> | selectable | yes, from 1 MiB | included |
| <span class="risk moderate">moderate</span> | selectable | yes, once unused for `stale_after` | included |
| <span class="risk caution">caution</span> | selectable, confirmed by typing `yes` | never | only with `--risk caution` |
| <span class="risk never">never</span> | not selectable | never | never |

Two details in the picker protect caution items further: pressing <kbd>space</kbd> on a category selects all of its items **except** caution ones (open the category to pick those individually), and the confirmation dialog asks you to type `yes` whenever the selection contains at least one caution item.

## Smart select

Smart select is one function, applied to every item, in this order:

1. **Never** when the item cannot be cleaned (report-only, not selectable, or risk <span class="risk never">never</span>), is still being measured, **carries a warning**, or is <span class="risk caution">caution</span>.
2. **Never** for project artifacts (`node_modules`, `Pods`, builds…) of a project that was active in the last 24 hours: you are working on it.
3. **Yes** when the scanner itself recommends the item (see below).
4. **Yes** for <span class="risk safe">safe</span> items of at least 1 MiB.
5. **Yes** for <span class="risk moderate">moderate</span> items whose last use is known and older than `stale_after` (14 days by default).
6. Otherwise, no.

The order matters. A warning blocks everything, including the scanner's own recommendation. That is deliberate: warnings are how scanners say "look at this first". Some examples:

- `Xcode is running — quit it before cleaning` keeps DerivedData out of smart select until you quit Xcode and rescan.
- `2 unpushed commits (branch feature/login is kept)` keeps a worktree out, even though removing it would not lose the commits.
- `pinned by 1 project file(s): …` keeps a node version that a project's `.nvmrc` asks for.
- `newest iOS symbols — kept for on-device debugging` keeps the most recent DeviceSupport folder.

### Scanner recommendations

Rules 4 and 5 are generic. Scanners know more: they can recommend an item the generic rules would miss (a moderate item younger than `stale_after`, a safe item under 1 MiB), and they keep an item out by attaching a warning or raising its risk. A few representative scanner recommendations, which come on top of the generic rules:

| Scanner | Recommended when |
|---|---|
| worktrees | Clean, pushed and idle for 7 days, or merged into the default branch and idle for 1 day. Stale worktree metadata (`git worktree prune`) is always recommended. |
| artifacts | A safe output (build folder, cache) of a project idle for 24 hours. A Yarn Berry project cache that `.yarnrc.yml` no longer uses. |
| apple | DerivedData of a workspace that no longer exists. Unavailable simulators. XCTest recordings of shut-down simulators. A runtime no simulator uses when a newer one of the same platform is installed. |
| android | x86 emulators and system images on Apple Silicon. System images no AVD uses. SDK packages (NDK, build-tools, platforms) no scanned project uses; the newest installed version is never recommended. Interrupted Gradle downloads. |
| js | Node versions nothing references (not default, not on `PATH`, not pinned, not running, no global package installed only there). npx packages unused for 14 days. Superseded Expo Go builds. Playwright browsers no installed Playwright needs. |
| ai | Superseded CLI builds (Claude Code, cursor-agent, Copilot CLI…). Claude Code data of folders that no longer exist. Cursor data of deleted workspaces. |
| system | `brew cleanup`. Downloaded app updates. Old VS Code builds' cache and superseded extension versions. JetBrains caches of IDE versions you no longer have. |

The full list, item kind by item kind, is in the [scanners reference](/lu-cleaner/reference/scanners/). Catalog entries can carry the flag too: `Flipper leftovers` is recommended whatever its size, since React Native 0.74+ no longer uses Flipper.

### Where smart select applies

| Command | Behavior |
|---|---|
| `lu-cleaner`, `lu-cleaner clean` | Opens the picker. When the scan finishes, recommended items are preselected, except those you already touched during the scan. |
| `lu-cleaner artifacts`, `worktrees`, `devices` | Nothing is preselected, unless you pass `--smart`. |
| `lu-cleaner scan` | Recommended items get a ★ in the `RECO` column; each category header shows how much is recommended. `scan --smart` lists only them. |
| `lu-cleaner clean --yes --smart` | Cleans exactly the recommended items (within the other filters). |

```bash
lu-cleaner scan --smart                        # what smart select would pick, nothing deleted
lu-cleaner clean --yes --smart --dry-run       # the same, as a cleaning plan with re-checks
lu-cleaner clean --yes --smart -c artifacts    # clean only the recommended project artifacts
```

`stale_after` is a setting, not a filter. `--older-than 30d` hides everything used in the last 30 days (and everything whose age is unknown), whatever its risk; `stale_after = "30d"` only changes when moderate items become recommended. See [Configuration](/lu-cleaner/reference/configuration/).

## Picking everything yourself

Smart select is a starting point, never a requirement.

- **Start empty.** `lu-cleaner clean --no-smart` opens the picker with nothing selected.
- **Clear a view.** In the picker, <kbd>n</kbd> unselects every item of the current view (the open category, or the filtered list). <kbd>A</kbd> selects them all, <kbd>i</kbd> inverts, <kbd>space</kbd> toggles one item, and <kbd>a</kbd> re-applies smart select to the current view.
- **Script it without smart select.** `clean --yes` accepts any narrowing filter instead of `--smart`:

```bash
lu-cleaner clean --yes -c artifacts --older-than 30d --dry-run   # every stale artifact, recommended or not
lu-cleaner clean --yes -k node_modules --min-size 200MB --dry-run
lu-cleaner clean --yes -c worktrees --risk caution --dry-run     # caution items need an explicit --risk
```

All keys are listed in [Keyboard shortcuts](/lu-cleaner/reference/keyboard-shortcuts/).

## Cleaning methods

The method is chosen by the scanner for each item. You can see it in the picker's details pane, in `scan --json` and in the confirmation dialog.

| Method | What happens | Frees space |
|---|---|---|
| `delete` | The path is removed permanently. Symlinks are removed, never followed. Read-only folders (Go module cache, some SDKs) and file flags are cleared on a second attempt. | Immediately |
| `trash` | The path is moved to `~/.Trash`, renamed if a file with the same name is already there. Only works on the same volume as your home. | Only once you empty the Trash |
| `command` | lu-cleaner runs the tool's own cleanup command: `xcrun simctl delete <udid>`, `xcrun simctl runtime delete …`, `pnpm store prune`, `docker builder prune -a -f`, `brew cleanup --prune=all`, `go clean -cache`, `git worktree prune`… | When the command completes |
| `worktree` | `git worktree remove` from the main repository, then removal of the tool's empty task folder. The branch is kept. Refused when there is uncommitted work, a lock, or commits reachable only from a detached HEAD, unless `--force`. | Immediately |
| `report` | Nothing. The item is information only. | No |

**Why commands?** Some data is owned by a tool that keeps its own bookkeeping. Removing a simulator with `xcrun simctl delete` keeps CoreSimulator's own records consistent, which deleting its folder behind its back does not guarantee. `pnpm store prune` removes only the packages no project references, which a plain `rm` cannot know. When a tool offers a correct cleanup, lu-cleaner uses it.

### Why `delete` is the default

The point of cleaning is to get space back. Moving 40 GB to the Trash frees nothing: the files stay on the same disk until the Trash is emptied, and a full disk stays full. It also mixes lu-cleaner's output with the files you trashed yourself, so the day you empty the Trash you delete both at once without reviewing either.

Permanent deletion is reasonable here because of what lu-cleaner proposes: caches and outputs that come back on their own, data you confirmed item by item, and a pipeline that re-checks every path right before removing it (see [Safety model](/lu-cleaner/concepts/safety/)).

If you still prefer a safety net, you can move items to the Trash instead:

- `--trash` on the command line, for one run;
- `use_trash = true` in the configuration, for every run;
- <kbd>t</kbd> in the picker, to toggle Trash mode for the next clean.

Trash mode only changes `delete` items: commands and worktree removals behave the same. Xcode archives use the Trash even without Trash mode, because an archive cannot be rebuilt identically.

:::caution
In Trash mode, the summary reminds you that space is only freed once the Trash is emptied. If `doctor` shows a large Trash after a clean, that is where your space went.
:::
