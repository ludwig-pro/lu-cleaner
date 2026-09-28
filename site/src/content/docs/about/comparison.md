---
title: Comparison with other tools
description: How lu-cleaner compares with CleanMyMac, mole, npkill, kondo, ncdu/dua/gdu and DevCleaner for Xcode, what each tool is best at, and how they complement each other.
sidebar:
  order: 1
---

lu-cleaner is deliberately narrow. It targets **macOS machines of developers who build React Native, iOS or Android apps and work with AI coding agents**. Other tools cover more ground, or run on more platforms. This page compares them honestly so you can pick the right one, or combine several.

:::note
The information about other tools comes from their public documentation as of September 2026. Tools change quickly. If something here is out of date, please [open an issue](https://github.com/ludwig-pro/lu-cleaner/issues).
:::

## At a glance

| Tool | What it is | Platforms | Interface | JSON output | License · price |
|---|---|---|---|---|---|
| **lu-cleaner** | Developer disk cleaner | macOS | CLI + terminal UI | Yes (`scan`, `clean`, `doctor`, `history`, `analyze`) | MIT · free |
| **CleanMyMac** (app) | Whole-Mac maintenance | macOS | GUI | No | Proprietary · subscription |
| **CleanMyMac CLI** | Developer cleanup in the terminal (public beta) | macOS | Terminal menus | Not documented | Proprietary · free during the beta |
| **mole** | Whole-Mac cleanup, uninstaller, analyzer, status | macOS | CLI + terminal UI | For `analyze`, `status`, `history` | GPL-3.0 · free (separate paid app) |
| **npkill** | `node_modules` finder | Any (Node.js) | Terminal UI | Yes (`--json`, `--json-stream`) | MIT · free |
| **kondo** | Build-artifact cleaner for 20+ ecosystems | macOS, Linux, Windows | CLI prompts, optional GUI | Not documented | MIT · free |
| **ncdu, dua, gdu** | Disk usage analyzers | Any | Terminal UI | Export (ncdu, gdu) | MIT · free |
| **DevCleaner for Xcode** | Xcode data cleaner | macOS | GUI (Mac App Store) | No | GPL-3.0 · free, optional tips |

## Developer coverage

| Tool | AI agent worktrees | React Native / iOS / Android artifacts | Simulators and emulators | AI tools data | Checks before deleting |
|---|---|---|---|---|---|
| **lu-cleaner** | Finds them, shows status (dirty, unpushed, merged, orphaned), removes with `git worktree remove` | `node_modules`, `ios/Pods`, `ios/build`, `android/app/build`, `.gradle`, `.cxx`, `.expo`… with marker files and git checks | iOS devices, runtimes, orphan device folders, XCTest recordings, logs; Android AVDs, system images, NDKs | Claude Code, Codex, Cursor, Conductor… with orphan detection | Safety guard, unchanged inode, running apps, open databases, processes inside, typed `yes` for risky items |
| **CleanMyMac** (app) | Not documented | Xcode junk | Not documented | Not documented | Review before cleaning |
| **CleanMyMac CLI** | Not documented | `node_modules`, `Pods`, `.next`, `target`, `.venv`… plus Xcode, Gradle, CocoaPods caches | Old simulators | Yes (Claude mentioned) | Review, ignore list, recent artifacts not preselected |
| **mole** | Cleans artifacts *inside* agent worktrees, never the worktrees themselves | `node_modules`, `build`, `dist`, `target`, `.build` | Not documented | Not documented | Dry run, whitelist, recent artifacts not preselected |
| **npkill** | No | `node_modules` (other names with `--targets`) | No | No | Flags folders that apps need |
| **kondo** | No | Node.js, Gradle, Swift, React Native and many others | No | No | Prompt per project, `--older` filter |
| **ncdu, dua, gdu** | No | Generic: you decide what a folder is | No | No | Confirmation only |
| **DevCleaner for Xcode** | No | Xcode DerivedData, Archives | Device support, simulator and device logs (not devices or runtimes) | No | Review in the app |

"Not documented" means the tool's documentation does not mention it, not that the feature is missing.

## What lu-cleaner does not do

- **No app uninstaller, no malware scan, no RAM or system "optimization".** Use CleanMyMac or mole for those.
- **No system-level cleaning.** lu-cleaner only deletes inside your home folder and your per-user temp folder, and never uses `sudo`. Caches in `/Library` or other users' folders are out of scope.
- **macOS only.** No Linux or Windows version.
- **No cleaning of external drives.** Items on another volume are reported, never removed.
- **No GUI.** It is a terminal tool, with an interactive terminal UI.

## When to use which

| You want to… | Use |
|---|---|
| Keep a whole Mac tidy: app leftovers, system junk, uninstall apps | CleanMyMac or mole |
| Quickly sweep `node_modules` on any OS | npkill |
| Clean build folders of Rust, Unity, .NET, Python… projects | kondo |
| Find out what a folder is made of, on any machine, including servers and external drives | ncdu, dua or gdu |
| A GUI focused on Xcode data | DevCleaner for Xcode |
| Get back the space eaten by agent worktrees, React Native projects, simulators, emulators, package-manager stores and AI tools data, without losing work | lu-cleaner |

## Using them together

lu-cleaner keeps no state besides its history file, so it does not conflict with other cleaners. A common combination:

1. A general cleaner (CleanMyMac or mole) for system junk and app leftovers.
2. lu-cleaner for developer data: `lu-cleaner` for the dashboard, or `lu-cleaner clean --yes --smart` on a schedule (see [Automation](/lu-cleaner/guides/automation/)).
3. A disk analyzer, or `lu-cleaner analyze`, when something still looks too big.

Two things set lu-cleaner apart even where tools overlap:

- **It knows state, not just folder names.** A worktree with uncommitted changes, a simulator that is booted, a Codex logs database that is open, Claude Code data whose folder still exists: lu-cleaner checks each of these before proposing or deleting anything. See the [safety model](/lu-cleaner/concepts/safety/).
- **It explains what comes back and at what cost.** Every item has a [risk level](/lu-cleaner/concepts/risk-and-smart-select/) and a note saying how it is regenerated, from a cache that refills on its own to a model you would have to download again.

## Related

- [How it works](/lu-cleaner/concepts/how-it-works/)
- [What gets scanned](/lu-cleaner/reference/scanners/)
- [FAQ](/lu-cleaner/about/faq/)
