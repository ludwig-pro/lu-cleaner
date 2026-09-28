---
title: Safety model
description: Every check lu-cleaner runs before removing anything, in order, why each one exists, what --force does and does not bypass, and what lu-cleaner never does.
sidebar:
  order: 3
---

lu-cleaner deletes gigabytes at a time on the machine you work on. Its safety model rests on four principles:

- **Scanners propose, one pipeline decides.** Scanners never delete anything. Every removal goes through the same executor and the same safety guard, whatever produced the item. A wrong catalog entry or a scanner bug can at worst *propose* something, and that proposal still has to pass the guard's hard-coded rules.
- **Check at the last moment.** Your machine changes between the scan and the clean: you open Xcode, an agent starts working in a worktree, a folder is replaced by a fresh clone. Every check below runs again right before each item is processed.
- **Fail closed.** When lu-cleaner cannot verify something (the process list cannot be read, `lsof` fails, `simctl` does not answer, `git status` errors), the item is skipped.
- **All or nothing per item.** Every path of an item is validated before anything is removed, so a failed check skips the whole item, and the summary says why.

## What lu-cleaner never does

- **Never uses `sudo` on your Mac** or asks for administrator rights. Root-owned leftovers (stale simulator dyld caches, system JDKs) are reported with the command to run yourself. The only `sudo` lu-cleaner ever runs is `fstrim` *inside* a colima Linux VM, right after a Docker prune, so that the VM disk can shrink.
- **Never removes files outside your home folder and your per-user temporary folder.** Space held elsewhere (simulator runtimes in system storage, Homebrew's Cellar, Docker's VM disk) is only reclaimed through the owning tool's own command, which is shown to you before you confirm.
- **Never deletes source code.** A directory that is a git repository is refused, your project roots are never removed, and generic names like `build/` or `dist/` are only proposed when git ignores them or their content proves they are build output.
- **Never follows symlinks.** Deleting a symlink removes the link, not its target.
- **Never touches credentials, keys or tool configuration** (see [protected paths](#3-protected-paths)).
- **Never deletes without your explicit selection**, in the picker or through `clean --yes` with a narrowing filter.
- **Never modifies anything while scanning.** Scanners only list directories, read small files and run read-only commands (`git status` with optional locks disabled, `xcrun simctl list`, `lsof`, `sqlite3` in read-only immutable mode).

## Checks on every item, in order

For each selected item, the executor runs these checks. The first one that fails skips the item with its reason.

1. **Cleanable.** Report-only items, items that are not selectable and items of risk <span class="risk never">never</span> are skipped (`not cleanable`).
2. **Running apps.** Items declare the apps that must be closed: Xcode and `xcodebuild` for DerivedData, Simulator for CoreSimulator caches, the emulator engine for Android AVDs, Codex and ChatGPT for the Codex logs database, Android Studio for its caches. If one runs, the item is skipped (`Xcode is running — quit it first (or use --force)`). If the process list cannot be read, the item is skipped too, because "not running" cannot be proven.
3. **Item re-check.** Some items carry their own last-minute test. A simulator, its recordings, logs and caches are only removed if `simctl` still reports the simulator as shut down; if it was booted since the scan, or `simctl` cannot be queried, the item is skipped. Claude Code data of a deleted folder is only removed if the folder is still missing and no session wrote there in the last hour.
4. **Per-path checks.** For every path of the item (command items have none):
   1. A path that no longer exists is ignored. If all are gone, the item is reported as `already gone`.
   2. **Inode identity.** The scan recorded the identity (inode) of each path. If it changed, the path was replaced since the scan (by a symlink, a new clone, a restored backup) and the item is skipped: `changed since the scan (different inode) — rescan`.
   3. **Open databases.** SQLite files (`.sqlite`, `.sqlite3`, `.db`, `-wal`, `-shm`, `-journal`, `.vscdb`) are checked with `lsof`. Deleting a database an app has open corrupts it, so an open file, or an `lsof` failure, skips the item.
   4. **The safety guard** validates the path. See [below](#the-safety-guard).
   5. **Marker files.** Project artifacts are only proposed when a marker sits next to them: `package.json` next to `node_modules`, `Podfile` or `Podfile.lock` next to `Pods`, `build.gradle` next to an Android `build/`. The marker must still be there, otherwise the folder may have become something else.
   6. **Current directory.** The directory your shell runs lu-cleaner from, or any folder containing it, is never removed.
   7. **Processes inside.** A process whose working directory is inside the folder (a dev server, a shell, an agent), or that runs an executable or a loaded library from it (a node binary from a node version folder, a native `.node` addon from `node_modules`), makes the item skip: `in use: process 4242 is working inside …`.
5. **Worktree rules**, for git worktrees. See [below](#worktree-rules).

With `--dry-run` (`-n`), checks 1 to 4 run for real and nothing is executed after them, so the plan you see includes the skips you would get. The git checks of the worktree rules are the exception: they run only when a worktree is really removed. The scan already flags dirty, locked and detached worktrees as <span class="risk caution">caution</span>, with a warning.

## The safety guard

The guard is the last line of defence. Its rules are hard-coded and independent from the catalog and the scanners. Scanners consult it too, so protected paths never appear in the list in the first place. Paths are compared case-insensitively, like APFS.

### 1. Well-formed path

The path must be absolute and clean: no `..`, no `.`, no duplicate slashes. Relative or ambiguous paths are refused before anything else.

### 2. Allowed areas and denied folders

Deletions happen only **strictly inside** your home folder or your per-user temporary area (the parent of `$TMPDIR`, such as `/var/folders/xx/yyyy/`, which holds `T/` and `C/`). Everything else is refused: `outside the allowed areas (home, per-user temp)`.

Some folders can have their content cleaned but can **never be removed themselves**:

- system folders: `/`, `/System`, `/Library`, `/Applications`, `/Users`, `/usr`, `/bin`, `/etc`, `/var`, `/private`, `/opt`, `/opt/homebrew`, `/Volumes`, `/tmp`…;
- your home and its standard folders: `~`, `~/Library`, `~/Documents`, `~/Desktop`, `~/Downloads`, `~/Pictures`, `~/Movies`, `~/Music`, `~/.Trash`…;
- the big containers of caches and tools: `~/Library/Caches`, `~/Library/Application Support`, `~/Library/Developer`, `~/Library/Developer/CoreSimulator/Devices`, `~/Library/Android/sdk`, `~/.config`, `~/.cache`, `~/.local`, `~/.npm`, `~/.gradle`, `~/.android`, `~/.claude`, `~/.codex`, `~/.cursor`, `~/go`…;
- `$TMPDIR` itself and its sibling folders.

`~/Library/Caches/CocoaPods` can be cleaned; `~/Library/Caches` cannot.

### 3. Protected paths

A protected path is never removed, **nor anything inside it, nor anything that contains it**. The built-in list covers, among others:

- keys and credentials: `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.azure`, `~/.kube`, `~/.netrc`, `~/.npmrc`, `~/.yarnrc.yml`, `~/.gitconfig`, `~/.git-credentials`, `~/.config/gh`, `~/.password-store`, `~/.docker/config.json`, Keychains;
- shell profiles: `~/.zshrc`, `~/.zprofile`, `~/.bashrc`…;
- personal data: Preferences, iCloud Drive and cloud storage folders, Mail, Messages, Calendars, Safari, cookies, iOS device backups (`MobileSync`);
- development identity: provisioning profiles, Xcode snippets, key bindings and themes, Android `debug.keystore` and adb keys, `~/.gradle/gradle.properties` and init scripts, Expo's `~/.expo/state.json`;
- AI tool configuration and memory: `~/.claude.json`, Claude Code settings, `CLAUDE.md`, skills, agents, commands, hooks, rules, history and sessions, each project's `memory/`; Codex `auth.json`, `config.toml`, memories, skills, rules, `AGENTS.md` and state databases; Cursor `mcp.json`, rules and skills; VS Code and Cursor user settings, key bindings, snippets and state databases; Gemini CLI credentials and settings; Claude desktop configuration.

Some entries are **patterns**. `~/.claude/projects/*/memory` protects the auto-memory of every Claude Code project, and `~/.codex/state_*.sqlite*` every Codex state database. A folder that *contains* a match is refused as well: removing a whole `~/.claude/projects/<project>` folder that holds a `memory/` folder is refused. This is why the Claude Code scanner removes the transcripts of a deleted project one by one and keeps `memory/`.

"Contains" matters just as much as "inside": removing `~/Library/Developer/Xcode/UserData` would remove your Xcode code snippets and key bindings, so it is refused even if a scanner proposed it.

You can extend the list with the `protect` key of the configuration file, and every `exclude` path is protected too. See [Configuration](/lu-cleaner/reference/configuration/).

### 4. Scan roots

Your project roots and worktree roots may be cleaned **inside**, but a root itself, or any folder that contains a root, is never removed. With `~/dev` as a root, `~/dev/my-app/node_modules` can go; `~/dev` cannot.

### 5. Symlinked parents are resolved

If a parent folder of the path is a symlink, the **real** location must pass the same rules. Suppose `~/Library/Android/sdk` is a symlink to an external SSD: a system image reached through it resolves to `/Volumes/…`, which is outside the allowed areas, so it is refused. The path itself is never followed: removing a symlink removes the link.

### 6. Git repositories are source code

A directory containing a `.git` **directory** is a repository and is refused: `is a git repository (source code)`. Only a few items are explicitly allowed to be repositories, such as the CocoaPods spec repo clone and items already in your Trash. Linked worktrees, which have a `.git` **file**, go through `git worktree remove` instead (see below).

## Worktree rules

Removing a git worktree is the most sensitive operation lu-cleaner performs, because the folder may hold work that exists nowhere else.

- Only **linked worktrees** (a checkout with a `.git` file) are removed this way. The command is `git worktree remove`, run from the main repository. **The branch is always kept.**
- Unless you pass `--force`, the removal is skipped when:
  - the worktree is **locked** (`git worktree lock`);
  - it has **uncommitted changes**, tracked or untracked;
  - its HEAD is **detached** with commits that no branch, tag or remote contains: they would become unreachable. Create a branch first (`git branch <name>`);
  - git cannot answer (`git status` fails), because unknown is not clean.
- Git itself refuses to remove dirty, locked or submodule worktrees without `--force`. That is a second line of defence after lu-cleaner's own checks.
- **Unpushed commits on a named branch do not block removal**: the branch survives, and so do its commits. The scanner still shows a warning (`2 unpushed commits (branch feature/login is kept)`), which keeps the worktree out of smart select.
- **Ignored files are lost.** `node_modules`, `Pods` and native builds never block `git worktree remove`, which is usually what you want. `.env` files are ignored too: the scanner warns when a worktree holds some (`ignored .env files would be lost: .env.local`).
- A worktree git no longer tracks (its metadata or its main repository is gone) is an **orphan**. Git cannot help, so the folder is removed as a plain directory, after the guard check. Orphans are always <span class="risk caution">caution</span>.
- Worktrees outside the allowed areas (for example in `/tmp`) are report-only. Their warning gives the exact `git -C <repo> worktree remove <path>` command to run yourself.

At scan time, the worktrees scanner also marks as <span class="risk caution">caution</span> any worktree that is open in Cursor or VS Code, has a process working inside, or has an active Codex thread, Claude desktop session or Conductor workspace. The worktree you are currently in cannot be selected at all.

## What `--force` bypasses

`--force` exists for the case where you have checked an item yourself and a guard is in the way (Xcode is open for another project, you really want to discard a dirty worktree). It relaxes a small, fixed set of checks:

| Check | Bypassed by `--force` |
|---|---|
| Running apps | Yes |
| Processes working inside, or executing from, a folder | Yes |
| Worktree locked, uncommitted changes, detached commits (git is called with `--force --force`) | Yes |
| Item re-checks (simulator still shut down, orphan folder still missing) | No |
| Inode identity since the scan | No |
| Open SQLite databases | No |
| Marker files | No |
| Current directory | No |
| The safety guard: allowed areas, denied folders, protected paths, scan roots, git repositories, symlinked parents | No |
| Report-only items and risk <span class="risk never">never</span> | No |
| `clean --yes` excluding <span class="risk caution">caution</span> items unless `--risk caution` | No |

:::danger
`--force` applies to every item of the run. Combined with `--yes`, it applies to the whole plan without asking. Use it on a narrow selection (one worktree, one kind), after a `--dry-run`.
:::

## Non-interactive cleaning

`clean --yes` (and `artifacts --yes`, `worktrees --yes`, `devices --yes`) never prompts, so it has stricter defaults than the picker:

- **A narrowing filter is mandatory**: `--smart`, `--category`/`-c` or `--kind`/`-k` (`artifacts`, `worktrees` and `devices` are already narrowed to their own categories). A bare `lu-cleaner clean --yes` is refused with exit code 2, so a script with an empty variable cannot turn into "clean everything".
- **Caution items are excluded** unless you pass `--risk caution` explicitly.
- Items still being measured are never included.

```bash
lu-cleaner clean --yes --smart --dry-run   # always start here
lu-cleaner clean --yes --smart
```

See [Automation](/lu-cleaner/guides/automation/) for scripts and scheduled cleanups.

## The analyzer

`lu-cleaner analyze` lets you delete arbitrary folders from an ncdu-like view. Those deletions go through the same executor and guard: every entry is treated as <span class="risk caution">caution</span> and needs a typed `yes`, and a linked worktree is removed with `git worktree remove` like in the picker.
