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
- **Never deletes source code.** A directory holding a `.git` entry of any kind, or laid out as a bare repository, is refused, your project roots are never removed, and generic names like `build/` or `dist/` are only proposed when git ignores them or their content proves they are build output.
- **Never follows symlinks.** Deleting a symlink removes the link, not its target.
- **Never touches credentials, keys or tool configuration** (see [protected paths](#3-protected-paths)).
- **Never enters other apps' containers without Full Disk Access.** `~/Library/Containers/<app>` and `~/Library/Group Containers/<group>` are not read, measured or cleaned: on macOS 14 and later the first access would block on a system permission prompt. What lives there is reported as needing Full Disk Access.
- **Never deletes permanently in Trash mode.** Worktree removals and commands, which cannot go to the Trash, are skipped instead.
- **Never deletes without your explicit selection.** The picker never preselects anything: you pick with `space`, or ask for the recommended items with `a` (or start with them using `--smart`), and the confirmation shows the total. `clean --yes` needs a narrowing filter.
- **Never modifies anything while scanning.** Scanners only list directories, read small files and run read-only commands (`git status` with optional locks disabled, `xcrun simctl list`, `lsof`, `sqlite3` in read-only immutable mode, `watchman --no-spawn watch-list`).
- **Never prints raw control characters.** Names, paths and messages read from disk go through a sanitizer (escape sequences and bidi overrides are shown as visible escapes such as `\x1b`), so a crafted folder name cannot rewrite your terminal. JSON output escapes them as `\uXXXX`.

## Checks on every item, in order

For each selected item, the executor runs these checks. The first one that fails skips the item with its reason.

1. **Cleanable.** Report-only items, items that are not selectable and items of risk <span class="risk never">never</span> are skipped (`not cleanable`).
2. **Trash mode.** With `--trash`, `use_trash` or <kbd>t</kbd> in the picker, worktree and command items are skipped (`not possible in Trash mode (it would delete permanently)`), and so are items already in `~/.Trash` (`already in the Trash`).
3. **Needs `--force`.** Some items are refused unless you pass `--force`: `needs --force: <reason>`. Today these are orphaned worktree folders, whose uncommitted work git can no longer see. Trash mode may still move them, since a move to the Trash can be undone.
4. **Running apps.** Items declare the apps that must be closed: Xcode and `xcodebuild` for DerivedData, Simulator for CoreSimulator caches, the emulator engine for Android AVDs, Codex and ChatGPT for the Codex logs database, Android Studio for its caches. If one runs, the item is skipped (`Xcode is running — quit it first (or use --force)`). If the process list cannot be read, the item is skipped too, because "not running" cannot be proven.
5. **Item re-check.** Some items carry their own last-minute test. A simulator, its recordings, logs and caches are only removed if `simctl` still reports the simulator as shut down; if it was booted since the scan, or `simctl` cannot be queried, the item is skipped. An unavailable simulator is only deleted if it is still unavailable, on the same runtime. Claude Code data of a deleted folder is only removed if the folder is still missing and no session wrote there in the last hour. An orphaned worktree folder is only removed if its git metadata is still verifiably gone and it holds no other repository.
6. **Per-path checks.** For every path of the item (command items have none):
   1. A path that no longer exists is ignored. If all are gone, the item is reported as `already gone`.
   2. **Inode identity.** The scan recorded the identity (inode) of each path. If it changed, the path was replaced since the scan (by a symlink, a new clone, a restored backup) and the item is skipped: `changed since the scan (different inode) — rescan`.
   3. **Open databases.** SQLite files (`.sqlite`, `.sqlite3`, `.db`, `-wal`, `-shm`, `-journal`, `.vscdb`) are checked with `lsof`. Deleting a database an app has open corrupts it, so an open file, or an `lsof` failure, skips the item.
   4. **The safety guard** validates the path. See [below](#the-safety-guard).
   5. **Marker files.** Project artifacts are only proposed when a marker sits next to them: `package.json` next to `node_modules`, `Podfile` or `Podfile.lock` next to `Pods`, `build.gradle` next to an Android `build/`. The marker must still be there, otherwise the folder may have become something else.
   6. **Current directory.** The directory your shell runs lu-cleaner from, or any folder containing it, is never removed.
   7. **Removable.** A path whose parent folder is read-only is skipped before anything is touched, so that it is never left half deleted. A mount point, a folder containing a mounted volume or disk image (`contains the mount point … — eject it first`), and another user's file in a sticky folder are skipped too.
   8. **Processes inside.** A process whose working directory is inside the folder (a dev server, a shell, an agent), or that runs an executable or a loaded library from it (a node binary from a node version folder, a native `.node` addon from `node_modules`), makes the item skip: `in use: process 4242 is working inside …`.
7. **Worktree rules**, for git worktrees. See [below](#worktree-rules).

With `--dry-run` (`-n`), all of these checks run for real, the worktree rules included, and nothing is executed after them, so the plan you see includes the skips you would get. Only git's own refusals at removal time cannot be previewed.

## The safety guard

The guard is the last line of defence. Its rules are hard-coded and independent from the catalog and the scanners. Scanners consult it too, so protected paths never appear in the list in the first place. Like APFS, paths are compared case-insensitively and regardless of Unicode normalization: `~/Dev` and `~/dev`, or a name typed with a composed `é` and the same name stored decomposed by Finder, are the same path.

### 1. Well-formed path

The path must be absolute and clean: no `..`, no `.`, no duplicate slashes. Relative or ambiguous paths are refused before anything else.

### 2. Allowed areas and denied folders

Deletions happen only **strictly inside** your home folder or your per-user temporary area (the parent of `$TMPDIR`, such as `/var/folders/xx/yyyy/`, which holds `T/` and `C/`). That area is opened only when `$TMPDIR` resolves to the per-user folder macOS gives you and belongs to you. Any other `$TMPDIR` only allows what is strictly inside it, and only if it is private to you (owned by you, not writable by others): an unset `$TMPDIR` or a shared folder such as `/tmp` or `/var/tmp` never becomes an allowed area. Everything else is refused: `outside the allowed areas (home, per-user temp)`.

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

You can extend the list with the `protect` key of the configuration file, and every `exclude` path is protected too. Those entries are literal paths, never patterns (a folder named `[wip] app` is matched as is); `~` and `$VARS` are expanded, and an entry reached through a symlink also protects its real location. See [Configuration](/lu-cleaner/reference/configuration/).

### 4. Scan roots

Your project roots and worktree roots may be cleaned **inside**, but a root itself, or any folder that contains a root, is never removed. With `~/dev` as a root, `~/dev/my-app/node_modules` can go; `~/dev` cannot. When you pass roots on the command line, your configured roots stay protected too.

### 5. Symlinked parents are resolved

If a parent folder of the path is a symlink, the **real** location must pass the same rules. Suppose `~/Library/Android/sdk` is a symlink to an external SSD: a system image reached through it resolves to `/Volumes/…`, which is outside the allowed areas, so it is refused. The path itself is never followed: removing a symlink removes the link.

### 6. Git repositories are source code

A directory holding a `.git` entry of **any** kind is a repository and is refused: a `.git` directory (a clone), a `.git` file (a linked worktree, a submodule, a separate git dir) or a `.git` symlink. So is a bare repository (a `HEAD` file with `objects/` and `refs/` folders). An unreadable `.git` counts as present: only a clean "does not exist" proves there is none. The refusal reads `is a git repository (…)`.

Only a few items are allowed to be repositories: the CocoaPods spec repo clone, items already in your Trash, verified orphaned worktree folders, and linked worktrees, which git itself removes after lu-cleaner's own checks (see below).

## Worktree rules

Removing a git worktree is the most sensitive operation lu-cleaner performs, because the folder may hold work that exists nowhere else.

- Only **linked worktrees** are removed this way: a checkout whose `.git` file points to git's record of it, which points back to that same folder. A copied or moved checkout is refused (`git records this worktree at …`). The command is `git worktree remove`, run from the main repository. **The branch is always kept**: commits on a branch, pushed or not, stay in the main repository.
- Unless you pass `--force`, the removal is skipped when:
  - the worktree is **locked** (`git worktree lock`);
  - it has **uncommitted changes**, tracked or untracked. The count uses explicit git options that ignore your configuration, so `status.showUntrackedFiles=no` cannot hide untracked files, and changed submodules count;
  - its HEAD is **detached** with commits that no branch, tag or remote contains: they would become unreachable. Create a branch first (`git branch <name>`);
  - it **contains another worktree or repository**, registered or not, even in an ignored folder: git would delete it along;
  - its main repository no longer exists, or git cannot answer (`git status` fails), because unknown is not clean.
- `--dry-run` runs the same checks, `git status` included, and reports the same refusals.
- Git itself refuses to remove dirty, locked or submodule worktrees without `--force`. That is a second line of defence after lu-cleaner's own checks.
- **Unpushed commits on a named branch do not block removal**: the branch survives, and so do its commits. The scanner still shows a warning (`2 unpushed commits (branch feature/login is kept)`), which keeps the worktree out of smart select.
- **Ignored files are lost.** `node_modules`, `Pods` and native builds never block `git worktree remove`, which is usually what you want. Ignored files that look like secrets or personal settings (`.env*`, `.npmrc`, keystores, signing keys, Firebase configs, `*.local.*` overrides, Claude local settings…) and exist nowhere else make the worktree <span class="risk caution">caution</span>, with the warning `ignored secret/local files would be lost: .env.local`. A file identical to the same path in the main working tree is not counted.
- A worktree git no longer tracks is an **orphan** only when that is verified: its git metadata (or its main repository) does not exist, under a folder that can be read. Git cannot help, so the folder is removed as a plain directory, after the guard check and a re-check that it is still an orphan and holds no other repository. Orphans are <span class="risk caution">caution</span>, never recommended, and **need `--force`**, since git can no longer see their uncommitted work. Trash mode may move them without `--force`.
- A worktree whose main repository or git metadata **cannot be read** (permissions, macOS privacy protection, unmounted volume) is not an orphan: it is report-only, never deleted, and its metadata is never pruned. A worktree whose main repository was moved or renamed is report-only too, with the `git worktree repair` command to run.
- Worktrees outside the allowed areas (for example in `/tmp`) are report-only. Their warning gives the exact `git -C <repo> worktree remove <path>` command to run yourself.
- Worktrees are never moved to the Trash: in Trash mode they are skipped.

At scan time, the worktrees scanner also marks as <span class="risk caution">caution</span> any worktree that is open in Cursor or VS Code, has a process working inside, or has an active Codex thread, Claude desktop session or Conductor workspace. The worktree you are currently in cannot be selected at all.

## What `--force` bypasses

`--force` exists for the case where you have checked an item yourself and a guard is in the way (Xcode is open for another project, you really want to discard a dirty worktree). It relaxes a small, fixed set of checks:

| Check | Bypassed by `--force` |
|---|---|
| Running apps | Yes |
| Processes working inside, or executing from, a folder | Yes |
| Worktree locked, uncommitted changes, commits on no branch, nested worktrees or repositories (git is called with `--force --force`) | Yes |
| Items that need `--force` (orphaned worktree folders) | Yes |
| Item re-checks (simulator still shut down, orphan folder still missing) | No |
| Inode identity since the scan | No |
| Open SQLite databases | No |
| Marker files | No |
| Current directory, read-only parent, mounted volumes inside | No |
| The safety guard: allowed areas, denied folders, protected paths, scan roots, git repositories, symlinked parents | No |
| Report-only items and risk <span class="risk never">never</span> | No |
| Trash mode skipping worktrees and commands | No |
| `clean --yes` excluding <span class="risk caution">caution</span> items and items with a warning unless `--risk caution` | No |

:::danger
`--force` applies to every item of the run. Combined with `--yes`, it applies to the whole plan without asking. Use it on a narrow selection (one worktree, one kind), after a `--dry-run`.
:::

## Non-interactive cleaning

`clean --yes` (and `artifacts --yes`, `worktrees --yes`, `devices --yes`) never prompts, so it has stricter defaults than the picker:

- **A narrowing filter is mandatory**: `--smart`, `--category`/`-c` or `--kind`/`-k` (`artifacts`, `worktrees` and `devices` are already narrowed to their own categories). A bare `lu-cleaner clean --yes` is refused with exit code 2, so a script with an empty variable cannot turn into "clean everything". A `--kind` naming a scanner that spans several categories (`-k catalog`, `-k system`) is not narrow enough on its own, and `artifacts --target` rejects a kind it does not know instead of scanning for nothing.
- **Caution items are excluded** unless you pass `--risk caution` explicitly. So are items the scan flagged with a warning (in use by a process, ignored secret files that would be lost, a runtime used by booted simulators…): they are listed under **Held back** instead.
- Items still being measured are never included.

```bash
lu-cleaner clean --yes --smart --dry-run   # always start here
lu-cleaner clean --yes --smart
```

See [Automation](/lu-cleaner/guides/automation/) for scripts and scheduled cleanups.

## The analyzer

`lu-cleaner analyze` lets you delete arbitrary folders from an ncdu-like view. Those deletions go through the same executor and guard: every entry is treated as <span class="risk caution">caution</span> and needs a typed `yes`, and a verified linked worktree is removed with `git worktree remove` like in the picker. Git repositories, submodules, git data, checkouts git cannot remove safely and folders that hold a repository are never deleted from the analyzer, and without Full Disk Access other apps' containers are shown locked and never opened.
