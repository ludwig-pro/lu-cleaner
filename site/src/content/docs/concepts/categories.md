---
title: Categories
description: The eleven categories lu-cleaner groups items into, what each one contains and how much space it typically holds.
sidebar:
  order: 4
---

Items are grouped into eleven categories. They organize the picker's first screen and the `scan` report, and they are what `--category` (`-c`) and the `disabled_categories` setting select.

```bash
lu-cleaner scan --summary              # one line per category
lu-cleaner scan -c simulators,xcode    # only these two
lu-cleaner clean -c worktrees          # picker limited to worktrees
```

## Overview

| ID | Title | Contains | Also accepted by `-c` |
|---|---|---|---|
| `worktrees` | Worktrees | Git worktrees created by AI agents and by hand | `wt`, `worktree` |
| `artifacts` | Project artifacts | `node_modules`, `ios/Pods`, iOS and Android builds, `.expo`, `dist`… inside your projects | `artifact` |
| `simulators` | iOS Simulators | Simulator devices, runtimes, recordings, logs and caches | `sim`, `simulator` |
| `xcode` | Xcode | DerivedData, archives, DeviceSupport, CocoaPods, SwiftPM, extra Xcode installs | |
| `android` | Android | Emulators (AVDs), SDK packages, Gradle, Android Studio, JDKs | |
| `ai` | AI tools | Claude Code, Claude desktop, Codex, Cursor agent, ChatGPT, Conductor… data and caches | |
| `js` | JS toolchain | npm, Yarn, pnpm, bun caches, node versions, Metro, Expo, test browsers | |
| `ide` | IDEs | VS Code, Cursor (editor part), Zed, JetBrains, Sublime caches and state | |
| `containers` | Containers | Docker, colima, Lima, OrbStack, Apple's `container` | `container` |
| `langs` | Other toolchains | Homebrew, Go, Rust, Python, Ruby, .NET, JVM caches | `lang` |
| `system` | System | Trash, browser and app caches, logs, Downloads, macOS snapshots and swap | |

Category IDs are case-insensitive. Several can be combined with commas or by repeating the flag: `-c js,ai` or `-c js -c ai`.

Tool names are not categories: `-c cursor`, `-c docker` or `-c node_modules` is refused with a hint naming the category the tool belongs to, because that category covers other tools too and a `clean --yes -c cursor` would clean every AI tool. To narrow to one tool or one kind of item, use `--kind`/`-k` with an item kind or a scanner ID (`lu-cleaner scan --json` shows the `kind` of every item).

## Typical sizes

The ranges below are illustrative, for a Mac used for React Native, iOS and Android development with AI coding agents. Your numbers will differ; `lu-cleaner scan --summary` or `lu-cleaner doctor` gives yours.

| Category | Typical total | What makes it grow |
|---|---|---|
| Worktrees | 5 – 50 GB | Each agent task checks out the repository again, with its own dependencies and native builds (1 – 3 GB per React Native worktree). |
| Project artifacts | 10 – 60 GB | One `node_modules` + `Pods` + Android build per project, per package in a monorepo. |
| iOS Simulators | 10 – 70 GB | Runtimes (about 8 GB each), devices with apps, UI-test screen recordings that nothing ever deletes. |
| Xcode | 5 – 40 GB | One DerivedData folder per workspace path, so one per worktree; 2 – 6 GB of DeviceSupport per iOS version. |
| Android | 10 – 40 GB | System images (1.5 – 7 GB each), emulators, Gradle caches per Gradle version, NDKs. |
| AI tools | 2 – 40 GB | Session transcripts with screenshots, old CLI builds, local models, VM images of desktop apps. |
| JS toolchain | 5 – 30 GB | Several package-manager caches side by side, one node install per version, Metro and Jest caches. |
| IDEs | 1 – 10 GB | Caches of old editor builds, superseded extension versions, language-server indexes. |
| Containers | 0 – 100 GB | Docker images and build cache inside a VM disk that never shrinks by itself. |
| Other toolchains | 1 – 20 GB | Homebrew downloads and old versions, Go and Cargo caches. |
| System | 2 – 30 GB | Browser caches and on-device AI models, app logs, old installers in Downloads, the Trash. |

## Worktrees

Every linked git worktree found in your worktree roots (`~/.codex/worktrees`, `~/.cursor/worktrees`, `~/conductor/workspaces`, `~/.claude/worktrees`…), in your project roots, and through `git worktree list` of each repository. Each item shows the tool that created it, the branch and a status: `clean`, `dirty`, `unpushed`, `merged`, `locked`, `orphan` or `unknown`. Stale worktree metadata (folders deleted without git knowing) appears as a `git worktree prune` item.

A worktree is removed as a whole with `git worktree remove`, which keeps its branch. An `orphan` (its git metadata verifiably gone) is deleted as a plain folder and needs `--force`; a worktree whose main repository cannot be read, or was moved, is report-only. See [AI agent worktrees](/lu-cleaner/guides/ai-worktrees/).

## Project artifacts

Build and dependency outputs found by walking your project roots, npkill-style, one item per folder: `node_modules`, `ios/Pods`, `ios/build`, `android/app/build`, `android/.gradle`, `.cxx`, `.expo`, `.next`, `.turbo`, `dist`, `coverage`, Rust `target`, Python `.venv`, Yarn Berry `.yarn/cache` and dozens more. A folder only matches when a marker file sits next to it (`package.json` next to `node_modules`), generic names like `build` must be ignored by git or contain build output, and a folder holding a `.git` entry is never proposed. With `--root` or `lu-cleaner artifacts <dir>`, only those folders are scanned.

Large git-ignored folders that no rule recognizes (200 MB or more, not counting known artifacts inside them) are listed as <span class="risk caution">caution</span> so you can decide. See [React Native projects](/lu-cleaner/guides/react-native-projects/).

:::note
Artifacts inside a worktree are listed twice: as part of the worktree's size and as artifacts of their own, so you can keep the worktree and drop only its `node_modules`. If you select both, only the worktree is processed and totals never count the same bytes twice.
:::

## iOS Simulators

Simulator devices (one item per device, deleted with `xcrun simctl delete <UDID>`), unavailable devices (one item per device too, <span class="risk caution">caution</span> when they may come back with another Xcode or hold apps with data), orphan device folders `simctl` no longer knows, runtimes (`xcrun simctl runtime delete`), alternate device sets (SwiftUI previews, parallel-testing clones), XCTest screen recordings left inside simulators, unified logs and system caches of shut-down simulators, CoreSimulator caches. See [iOS simulators and Xcode](/lu-cleaner/guides/ios-simulators-xcode/).

## Xcode

DerivedData, one item per folder (Xcode creates one per workspace path, so one per worktree), flagged when its workspace no longer exists; archives, DeviceSupport symbols per iOS version, extra Xcode installs (report only), the Metal toolchain, and the fixed caches of Xcode, CocoaPods, Swift Package Manager, Carthage and fastlane.

## Android

Emulators (AVDs) and their Quick Boot snapshots, SDK system images, NDKs, build-tools, platforms, CMake and sources, SDK Manager leftovers, the Gradle user home (wrapper distributions, per-version caches, dependency cache, daemon logs, toolchain JDKs), Android Studio caches, logs and old settings, installed JDKs, Kotlin/Native and Maven caches. An SDK or AVD folder on an external drive is reported, never touched. See [Android](/lu-cleaner/guides/android/).

## AI tools

Data left by AI coding tools: Claude Code sessions older than 30 days and data of project folders that no longer exist, old Claude Code, cursor-agent and Copilot CLI builds, Codex sessions and logs databases, Cursor agent transcripts and workspace data, Conductor archived contexts, local models (Ollama, LM Studio, Hugging Face, Whisper), desktop apps' caches and VM images. Configuration, credentials, memories and chat databases are protected, and backups of chat databases are <span class="risk caution">caution</span>. See [AI tools data](/lu-cleaner/guides/ai-tools-data/).

## JS toolchain

Package-manager caches (npm, Yarn classic and Berry, pnpm, bun, corepack), node versions installed by nvm, fnm, mise, asdf or volta (keeping the ones in use), npx installs, npm global leftovers, Metro, Haste, Jest and Vitest caches, Expo Go simulator builds, Playwright, Puppeteer and Cypress browsers, fnm's stale shell links and watchman watches on deleted folders (live Metro and Jest watches are left alone). The Yarn Berry global cache is <span class="risk caution">caution</span> when Plug'n'Play projects run from it, and so is a pnpm store whose global virtual store your projects link into. See [JS toolchain](/lu-cleaner/guides/js-toolchain/).

## IDEs

Caches and state of editors: VS Code (and Insiders, VSCodium) cached data of old builds, superseded extension versions, workspace storage of deleted folders, language-server indexes and Local History; the editor part of Cursor and Windsurf; Zed; JetBrains IDEs (caches and settings of old versions, logs); Sublime Text and Sublime Merge.

## Containers

Docker build cache, unused images and stopped containers (cleaned with Docker's own `prune` commands), unused volumes (report only), Docker Desktop and OrbStack disk images (listed only with Full Disk Access, since they live in app containers), colima and Lima VMs, Apple's `container` data, and download caches of VM tools. VM disks are reported with the command that reclaims their space: deleting a VM loses everything inside it.

## Other toolchains

Homebrew download cache and old versions (`brew cleanup`), Go build and module caches (`go clean`), Cargo registry and rustup downloads, unused Rust toolchains and rbenv Rubies (report only), pip, uv, Poetry, pipenv and conda caches, RubyGems, NuGet, Ivy/sbt, Zig and Bazel caches.

## System

The Trash, caches and offline web caches of desktop apps (Slack, Discord, Notion, Figma, Postman…), browser caches and their on-device AI models, app logs and crash reports, app updater downloads, old installers, archives and app builds in `~/Downloads` (<span class="risk caution">caution</span>, never preselected), local iPhone backups (report only), and the macOS facts that explain a full disk: Time Machine local snapshots, swap files and staged macOS updates, all report only. See [Disk space not freed](/lu-cleaner/guides/disk-space-not-freed/).

## Turning categories off

To skip a category entirely, list it in the configuration file:

```toml
disabled_categories = ["containers", "system"]
```

Items of disabled categories are never listed nor cleaned, and scanners that only produce disabled categories are not started at all, which also makes scans faster. See [Configuration](/lu-cleaner/reference/configuration/).
