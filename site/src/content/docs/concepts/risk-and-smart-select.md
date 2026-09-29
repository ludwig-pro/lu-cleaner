---
title: Risk levels and smart select
description: The four risk levels, the exact rules smart select uses to recommend items (opt-in, with the a key or --smart), how to choose what to remove yourself, and the five cleaning methods.
sidebar:
  order: 2
---

Every item lu-cleaner finds carries a **risk** (what you lose if it is deleted) and a **method** (how it is cleaned). Smart select combines the risk, the age and the scanner's own judgment to recommend what a careful developer would remove without a second thought. It is something you ask for, never something that happens on its own: nothing is selected when the picker opens.

## The four risk levels

| Risk | Meaning | Examples |
|---|---|---|
| <span class="risk safe">safe</span> | Pure cache. Regenerated automatically, holds no user data. At worst the next build or install is slower. | npm cache (`~/.npm/_cacache`), Metro cache, Xcode DerivedData, Gradle daemon logs, a project's Android `build/` folder, XCTest screen recordings, an unavailable simulator whose runtime is gone for good |
| <span class="risk moderate">moderate</span> | Regenerable, but it costs time or bandwidth to get back. | `node_modules`, `ios/Pods`, a clean git worktree (its branch is kept), simulator runtimes (about 8 GB to re-download), unused node versions, Gradle wrapper distributions, NDKs |
| <span class="risk caution">caution</span> | May hold data or state you care about. | A worktree with uncommitted changes or ignored secret files (`.env`, keystores…), an orphaned worktree folder, a simulator with installed apps and their data, Claude Code or Codex sessions older than 30 days, chat database backups, Xcode archives, Android emulators (AVDs), the Yarn Berry cache when Plug'n'Play projects run from it, a virtualenv without a requirements file, large git-ignored folders no rule knows, old installers in `~/Downloads` |
| <span class="risk never">never</span> | Protected or out of reach. Shown for information, never offered for deletion. | Time Machine local snapshots, swap files, staged macOS updates, chat databases of AI tools (Cursor global state, Conductor, Warp), a `build/` folder that turned out to hold source code |

A scanner can raise an item's risk based on what it observes. For example, a worktree is <span class="risk moderate">moderate</span> by default and becomes <span class="risk caution">caution</span> as soon as it has uncommitted changes, is locked, holds ignored secret files that exist nowhere else (`.env`, `.npmrc`, keystores, signing keys…), contains another repository, is open in an editor, has a process working inside it or has an active agent session. A node version becomes <span class="risk caution">caution</span> when it is your default version or the `node` on your `PATH`.

### What each level changes

| | Picker | Smart select | `clean --yes` |
|---|---|---|---|
| <span class="risk safe">safe</span> | selectable | yes, from 1 MiB | included |
| <span class="risk moderate">moderate</span> | selectable | yes, once unused for `stale_after` | included |
| <span class="risk caution">caution</span> | selectable, confirmed by typing `yes` | never | only with `--risk caution` |
| <span class="risk never">never</span> | not selectable | never | never |

`clean --yes` treats items that carry a warning like caution items, whatever their risk: without `--risk caution` they are left out and listed under **Held back**. `--risk never` is refused, since <span class="risk never">never</span> items are report-only.

Two details in the picker protect caution items further: pressing <kbd>space</kbd> on a category selects all of its items **except** caution ones (open the category to pick those individually), and the confirmation dialog asks you to type `yes` whenever the selection contains at least one caution item, even one inside a selected item, or an item that needs `--force`.

## Smart select

Smart select is **opt-in**. When the picker opens, nothing is selected, and it stays that way until you choose: <kbd>space</kbd> picks an item, <kbd>a</kbd> runs smart select on the current view, and `--smart` runs it for you as soon as the scan ends. The reason is simple: deleting should always be the result of an explicit choice. If the recommended items were already selected, a single <kbd>d</kbd> and <kbd>enter</kbd> would delete everything smart select found, whether or not you had looked at it. Asking for smart select yourself means you know what was picked, and the footer and the confirmation dialog show the count and the total before anything is removed.

Smart select is one function, applied to every item, in this order:

1. **Never** when the item cannot be cleaned (report-only, not selectable, or risk <span class="risk never">never</span>), is still being measured, **carries a warning**, is <span class="risk caution">caution</span>, or **its scanner vetoes it** (see below).
2. **Never** for project artifacts (`node_modules`, `Pods`, builds…) of a project that was active in the last 24 hours: you are working on it.
3. **Yes** when the scanner itself recommends the item (see below).
4. **Yes** for <span class="risk safe">safe</span> items of at least 1 MiB.
5. **Yes** for <span class="risk moderate">moderate</span> items whose last use is known and older than `stale_after` (14 days by default).
6. Otherwise, no.

The order matters. A warning or a veto blocks everything, including the scanner's own recommendation. That is deliberate: warnings are how scanners say "look at this first". Some examples:

- `Xcode is running — quit it before cleaning` keeps DerivedData out of smart select until you quit Xcode and rescan.
- `2 unpushed commits (branch feature/login is kept)` keeps a worktree out, even though removing it would not lose the commits.
- `pinned by 1 project file(s): …` keeps a node version that a project's `.nvmrc` asks for.
- `newest iOS symbols — kept for on-device debugging` keeps the most recent DeviceSupport folder.

### Scanner vetoes

Some items are never recommended, whatever their risk, size and age, because deleting them is irreversible or their origin is uncertain. You can still select them yourself:

- emptying the Trash;
- orphaned data whose owner may come back: orphaned worktree folders (which also need `--force`), Claude Code data of a deleted folder written in the last 30 days or whose folder may be restored (it is then <span class="risk caution">caution</span>), Cursor data of deleted workspaces that is recent or holds chats;
- the Antigravity browser profile of an uninstalled app (saved logins, cookies);
- an unavailable simulator that may come back (runtime still on disk, another Xcode selected) or that holds apps with data;
- the Yarn Berry global cache when Plug'n'Play projects run from it, a pnpm store whose global virtual store projects link into, `pnpm store prune` when a registered project is unreachable;
- the `keep_latest` newest node versions (per version manager) and simulator runtimes (per platform), and every node version when the process list cannot be read;
- old Android Studio settings while a newly installed version has not imported them yet;
- a build folder outside git with only weak evidence of being generated.

Chat database backups (Cursor state database backups, Codex repair backups of the memories and goals databases) are <span class="risk caution">caution</span>, so never recommended either. In the picker, an item that needs `--force` is never picked by smart select, even when you started lu-cleaner with `--force`.

### Scanner recommendations

Rules 4 and 5 are generic. Scanners know more: they can recommend an item the generic rules would miss (a moderate item younger than `stale_after`, a safe item under 1 MiB), and they keep an item out by attaching a warning or raising its risk. A few representative scanner recommendations, which come on top of the generic rules:

| Scanner | Recommended when |
|---|---|
| worktrees | Clean, pushed and idle for 7 days, or merged into the default branch and idle for 1 day, with no warning. Stale worktree metadata (`git worktree prune`) is always recommended. |
| artifacts | A safe output (build folder, cache) of a project idle for 24 hours. A Yarn Berry project cache that `.yarnrc.yml` no longer uses. |
| apple | DerivedData of a workspace that no longer exists. Unavailable simulators whose runtime is gone for good and that hold no app. XCTest recordings of shut-down simulators. A runtime no simulator uses when a newer one of the same platform is installed. |
| android | x86 emulators and system images on Apple Silicon. System images no AVD uses. SDK packages (NDK, build-tools, platforms) no scanned project uses; the newest installed version is never recommended. Interrupted Gradle downloads. |
| js | Node versions nothing references (not default, not on `PATH`, not pinned, not running, no global package installed only there), beyond the `keep_latest` newest. npx packages unused for 14 days. Superseded Expo Go builds. Playwright browsers no installed Playwright needs. Watchman watches on deleted folders (live watches of Metro or Jest are never touched). |
| ai | Superseded CLI builds (Claude Code, cursor-agent, Copilot CLI…). Claude Code data of folders that no longer exist, untouched for 30 days. Cursor data of deleted workspaces, untouched for 30 days and without chats. |
| system | `brew cleanup`. Downloaded app updates. Old VS Code builds' cache and superseded extension versions. JetBrains caches of IDE versions you no longer have. |

The full list, item kind by item kind, is in the [scanners reference](/lu-cleaner/reference/scanners/). Catalog entries can carry the flag too: `Flipper leftovers` is recommended whatever its size, since React Native 0.74+ no longer uses Flipper.

### Where smart select applies

| Command | Behavior |
|---|---|
| `lu-cleaner`, `lu-cleaner clean`, `lu-cleaner artifacts`, `worktrees`, `devices` | Opens the picker with nothing selected. Press <kbd>a</kbd> to smart select the current view. |
| The same commands with `--smart` | Opens the picker; when the scan finishes, the recommended items are selected, except those you already touched during the scan. |
| `lu-cleaner scan` | Recommended items get a ★ in the `RECO` column; each category header shows how much is recommended. `scan --smart` lists only them. |
| `lu-cleaner clean --yes --smart` | Cleans exactly the recommended items (within the other filters). |

```bash
lu-cleaner scan --smart                        # what smart select would pick, nothing deleted
lu-cleaner clean --yes --smart --dry-run       # the same, as a cleaning plan with re-checks
lu-cleaner clean --yes --smart -c artifacts    # clean only the recommended project artifacts
```

`stale_after` is a setting, not a filter. `--older-than 30d` hides everything used in the last 30 days (and everything whose age is unknown), whatever its risk; `stale_after = "30d"` only changes when moderate items become recommended. See [Configuration](/lu-cleaner/reference/configuration/).

## Choosing what to remove

Smart select is a starting point you opt into, never a requirement.

- **Start empty, always.** `lu-cleaner` and `lu-cleaner clean` open the picker with nothing selected. Pick with <kbd>space</kbd> (on a category, every item except caution ones), or press <kbd>a</kbd> to select the recommended items and review them.
- **Start with the recommended items.** `lu-cleaner clean --smart` opens the picker with them already selected. That is the explicit version of pressing <kbd>a</kbd> right after the scan.
- **Adjust a view.** In the picker, <kbd>n</kbd> unselects every item of the current view (the open category, or the filtered list). <kbd>A</kbd> selects them all, <kbd>i</kbd> inverts, <kbd>space</kbd> toggles one item, and <kbd>a</kbd> applies smart select to the current view: recommended items on, every other item off.
- **Rehearse.** `--dry-run` (`-n`) shows what would be cleaned and deletes nothing.
- **Script it without smart select.** `clean --yes` accepts any narrowing filter instead of `--smart`:

```bash
lu-cleaner clean --yes -c artifacts --older-than 30d --dry-run   # every stale artifact, recommended or not
lu-cleaner clean --yes -k node_modules --min-size 200MB --dry-run
lu-cleaner clean --yes -c worktrees --risk caution --dry-run     # caution and warned items need an explicit --risk
```

All keys are listed in [Keyboard shortcuts](/lu-cleaner/reference/keyboard-shortcuts/).

## Cleaning methods

The method is chosen by the scanner for each item. You can see it in the picker's details pane, in `scan --json` and in the confirmation dialog.

| Method | What happens | Frees space |
|---|---|---|
| `delete` | The path is removed permanently. Symlinks are removed, never followed. Read-only folders (Go module cache, some SDKs) and file flags are cleared on a second attempt. | Immediately |
| `trash` | The path is moved to `~/.Trash`, renamed (`name 2`, `name 3`…) if an entry with the same name is already there; an existing entry is never replaced. Only works on the same volume as your home. | Only once you empty the Trash |
| `command` | lu-cleaner runs the tool's own cleanup command: `xcrun simctl delete <udid>`, `xcrun simctl runtime delete …`, `pnpm store prune`, `docker builder prune -a -f`, `brew cleanup --prune=all`, `go clean -cache`, `git worktree prune`… | When the command completes |
| `worktree` | `git worktree remove` from the main repository, then removal of the tool's empty task folder. The branch is kept. Refused when the worktree is locked, has uncommitted or untracked changes, has commits on no branch (detached HEAD) or contains another repository, unless `--force`. | Immediately |
| `report` | Nothing. The item is information only. | No |

**Why commands?** Some data is owned by a tool that keeps its own bookkeeping. Removing a simulator with `xcrun simctl delete` keeps CoreSimulator's own records consistent, which deleting its folder behind its back does not guarantee. `pnpm store prune` removes only the packages no project references, which a plain `rm` cannot know. When a tool offers a correct cleanup, lu-cleaner uses it.

### Why `delete` is the default

The point of cleaning is to get space back. Moving 40 GB to the Trash frees nothing: the files stay on the same disk until the Trash is emptied, and a full disk stays full. It also mixes lu-cleaner's output with the files you trashed yourself, so the day you empty the Trash you delete both at once without reviewing either.

Permanent deletion is reasonable here because of what lu-cleaner proposes: caches and outputs that come back on their own, data you confirmed item by item, and a pipeline that re-checks every path right before removing it (see [Safety model](/lu-cleaner/concepts/safety/)).

If you still prefer a safety net, you can move items to the Trash instead:

- `--trash` on the command line, for one run;
- `use_trash = true` in the configuration, for every run;
- <kbd>t</kbd> in the picker, to toggle Trash mode for the next clean.

Trash mode never deletes anything permanently. It only moves files and folders: worktree and command items are skipped (`not possible in Trash mode (it would delete permanently)`), and so are items already in `~/.Trash` (emptying the Trash included). `--trash=false` turns it off for one run when `use_trash` is set. Xcode archives use the Trash even without Trash mode, because an archive cannot be rebuilt identically.

:::caution
Moves to the Trash are never counted as freed: summaries and the history total them apart and remind you that space is only freed once the Trash is emptied. If `doctor` shows a large Trash after a clean, that is where your space went.
:::
