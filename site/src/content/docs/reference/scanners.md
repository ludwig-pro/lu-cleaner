---
title: What gets scanned
description: Every item kind produced by lu-cleaner's eight scanners, with what it is, its risk, how it is cleaned and when smart select recommends it.
sidebar:
  order: 2
---

lu-cleaner runs eight scanners. Seven of them contain logic (git state, versions in use, simulator state…); the eighth, `catalog`, evaluates a list of fixed paths. This page lists every item **kind** they produce. Use a kind, or a scanner ID, with `--kind`/`-k`:

```bash
lu-cleaner scan -k node_modules,ios-pods      # two artifact kinds
lu-cleaner scan -k apple                      # everything the apple scanner found
lu-cleaner clean -y -k xcode-derived-data -n  # dry-run of all DerivedData folders
```

## How to read the tables

- **Risk**: <span class="risk safe">safe</span>, <span class="risk moderate">moderate</span>, <span class="risk caution">caution</span> or <span class="risk never">never</span>. Several kinds change risk depending on what the scanner observes; the condition is given.
- **Method**: `delete`, `trash`, `command` (the command is shown), `worktree` or `report`. See [cleaning methods](/lu-cleaner/concepts/risk-and-smart-select/#cleaning-methods).
- **Recommended** is the effective smart-select behavior:
  - **Yes**: whenever the item carries no warning (for safe items, from 1 MiB);
  - **Stale**: once unused for `stale_after` (14 days by default);
  - **No**: never preselected;
  - a condition: the scanner's own rule.

Two rules apply everywhere and are not repeated: an item with a **warning** is never recommended (an app running, a process inside, a newest version kept…), and project artifacts of a project active in the last 24 hours are never recommended. Details in [Risk levels and smart select](/lu-cleaner/concepts/risk-and-smart-select/#smart-select).

Anything whose real location is on another volume becomes report-only with the warning `on external volume — no internal gain`, whatever its kind.

## worktrees

Finds git **linked worktrees**, the checkouts AI agents create by the dozen, and removes whole worktrees with `git worktree remove`.

**Discovery**, combined and deduplicated by inode:

1. directories holding a `.git` file below the worktree roots (`~/.codex/worktrees`, `~/.cursor/worktrees`, `~/conductor/workspaces`, `~/.claude/worktrees`, `~/.claude-worktrees`, `~/.worktrees`, `~/.superset/worktrees`, `~/Library/Application Support/Claude/worktrees`, plus `worktree_roots`), 4 levels deep;
2. main repositories and stray worktrees below your project roots, up to `max_depth`;
3. `git worktree list --porcelain` of every main repository found, which catches worktrees anywhere on disk;
4. the conventional folders `<repo>/.claude/worktrees`, `<repo>/.worktrees`, `<repo>/worktrees` and `<repo>-worktrees`.

**The creating tool** is recognized from the location, Codex's `codex-thread.json`, and branch naming (`claude/…` for Claude desktop, `worktree-…` for Claude Code). The item kind is `<tool>-worktree`.

**Last activity** comes from git (HEAD commit date, index, reflog, modified files) and from the tools themselves (Codex threads, Claude desktop sessions), never from the folder's own modification time.

| Kind | What it is | Risk | Method | Recommended |
|---|---|---|---|---|
| `codex-worktree`, `cursor-worktree`, `conductor-worktree`, `claude-worktree`, `claude-desktop-worktree`, `superset-worktree`, `claude-squad-worktree`, `vibe-kanban-worktree`, `multica-worktree`, `manual-worktree` | One linked worktree, with its size broken down into checkout and artifacts (`node_modules`, `ios/Pods`, native builds…) | moderate; caution when dirty, locked, orphaned, moved, detached with unpushed commits, unreadable by git, open in Cursor or VS Code, used by a process, or tied to an active Codex thread, Claude desktop session or Conductor workspace | `worktree`; `delete` for orphans; `report` when outside your home (e.g. `/tmp`), on an external volume or when its main repository is on an unmounted volume | Clean, pushed and idle 7 days, or merged into the default branch and idle 1 day |
| `worktree-prune` | Git bookkeeping of worktrees whose folder is already gone | safe | `command`: `git -C <repo> worktree prune` (or deletion of only the safe entries when others belong to an unmounted or moved checkout) | Yes |

**Status** (column `STATUS` of `lu-cleaner worktrees --list`, `meta.status` in JSON):

| Status | Meaning |
|---|---|
| `clean` | No uncommitted changes, nothing unpushed |
| `merged` | Clean, and all its commits are in the default branch |
| `unpushed` | Commits that exist on no remote (ahead of the upstream, or never pushed), or git could not tell. The branch and its commits are kept on removal |
| `dirty` | Uncommitted changes, tracked or untracked |
| `locked` | `git worktree lock` was used |
| `orphan` | Git no longer tracks the folder: a plain copy of files |
| `unknown` | Git could not be queried |

Warnings also flag ignored `.env` files that would be lost, and a worktree git records at another location (`moved: … run git -C <repo> worktree repair`). The worktree containing your current directory cannot be selected. See [AI agent worktrees](/lu-cleaner/guides/ai-worktrees/).

## artifacts

The npkill-like scanner, React Native aware. It walks your project roots, the worktree roots and a few folders where tools keep project copies (`~/conductor/archived-contexts`, `~/Documents/Codex`, `~/.gemini/antigravity/scratch`, which get only dependency and build rules), up to `max_depth` levels. It never descends into symlinks or other volumes, and never proposes the checkout itself.

**Matching rules:**

- A folder only matches when a **marker** exists next to it (`package.json` next to `node_modules`). Markers are re-checked right before deletion.
- **Generic names** (`build`, `dist`, `out`, `target`, `coverage`, `vendor/bundle`, `.yarn/*`…) may be source code. Inside a git work tree they must be ignored by git, or be untracked with unambiguous build content (Xcode build products, `CMakeCache.txt`, `CACHEDIR.TAG`…). Outside git, they need that content.
- Inside a git work tree, a folder that holds **tracked files** is never proposed.
- A candidate that fails verification is shown as report-only with `not proposed: <reason>`.

**Last used** is the activity of the *project* (sources, lockfiles, configs, git index), because builds and installs touch the artifact itself constantly.

**Recommended**: safe artifacts once their project has been idle 24 hours; moderate ones once it has been idle for `stale_after`. Never while a process runs inside the project (`in use: process … runs inside the project (dev server, agent, shell?)`), and never when the folder contains built `.apk`, `.aab` or `.ipa` files.

### JavaScript and React Native

| Kind | Folder | Required next to it | Risk |
|---|---|---|---|
| `node_modules` | `node_modules` | `package.json` | moderate |
| `ios-pods` | `Pods` | `Podfile` or `Podfile.lock` | moderate |
| `ios-build` (`xcode-build` outside `ios/` and `macos/`) | `build` | `*.xcodeproj`, `*.xcworkspace` or `Podfile`, plus Xcode build content | safe; moderate when `Podfile.lock` references React Native codegen in `build/generated/ios` |
| `android-build` (`gradle-build` outside `android/`) | `build` | `build.gradle(.kts)`, plus Gradle output content | safe |
| `android-gradle` (`gradle-cache`) | `.gradle` | Gradle project files | safe |
| `android-kotlin` (`gradle-kotlin`) | `.kotlin` | Gradle project files | safe |
| `android-cxx` | `.cxx`, `.externalNativeBuild` | `CMakeLists.txt` or `build.gradle(.kts)` | safe |
| `expo` | `.expo` | `package.json`, `app.json` or `app.config.*` | safe |
| `metro-cache` | `.metro-cache`, `.metro` | `package.json` or `metro.config.*` | safe |
| `js-build`, `dist`, `web-build`, `out` | `build`, `dist`, `web-build`, `out` | `package.json` (or Next.js / electron-vite config for `out`), plus build output content | safe; `js-build`, `dist` and `out` become moderate when `package.json` (`main`, `exports`, `types`) points into them |
| `next`, `open-next`, `nuxt`, `nitro-output`, `svelte-kit`, `astro`, `docusaurus`, `react-router`, `vinxi`, `tanstack` | `.next`, `.open-next`, `.nuxt`, `.output`, `.svelte-kit`, `.astro`, `.docusaurus`, `.react-router`, `.vinxi`, `.tanstack` | `package.json` or the framework config | safe |
| `turbo`, `parcel-cache`, `vite-cache`, `swc-cache`, `wireit`, `rollup-cache`, `nx-cache`, `angular-cache`, `js-cache` | `.turbo`, `.parcel-cache`, `.vite`, `.swc`, `.wireit`, `.rollup.cache` or `.rpt2_cache`, `.nx/cache`, `.angular/cache`, `.cache` | `package.json` or the tool config | safe |
| `vercel-output`, `netlify-cache`, `netlify-functions-serve`, `wrangler-tmp` | `.vercel/output`, `.netlify/cache`, `.netlify/functions-serve`, `.wrangler/tmp` (site links and local data are kept) | `package.json` or the tool config | safe |
| `storybook-static`, `coverage`, `nyc-output` | `storybook-static`, `coverage` (with a coverage report inside), `.nyc_output` | `package.json` (or a Python project file for `coverage`) | safe |
| `playwright-report` | `playwright-report`, `test-results`, `blob-report` | `playwright.config.*` or `package.json` | moderate |
| `pnpm-store` | `.pnpm-store` (a `store-dir` inside the repository) | pnpm or npm project files | moderate |
| `yarn-cache` | `.yarn/cache` | `.yarnrc.yml` | moderate; recommended at once when `enableGlobalCache: true` makes it a leftover |
| `yarn-unplugged` | `.yarn/unplugged` | `.yarnrc.yml` | moderate |

### Apple

| Kind | Folder | Required next to it | Risk |
|---|---|---|---|
| `project-derived-data` | `DerivedData`, `DerivedData-*`, `.derived-data*`, `derived-data`, `derivedData` | Xcode project, `Podfile` or `Package.swift`, plus DerivedData content | safe |
| `swiftpm-build` | `.build` | `Package.swift` | safe |
| `carthage-build` | `Carthage/Build` | `Cartfile` or `Cartfile.resolved` | safe |

`ios-pods`, `ios-build` and `project-derived-data` are refused while `xcodebuild` runs.

### Other languages

| Kind | Folder | Required next to it | Risk |
|---|---|---|---|
| `cmake-build` | `build`, `build-*`, `build_*`, `cmake-build-*`, `out` with `CMakeCache.txt` inside | `CMakeLists.txt` | moderate |
| `flutter-build`, `dart-tool` | `build`, `.dart_tool` | `pubspec.yaml` | safe |
| `rust-target` | `target` | `Cargo.toml` | safe |
| `maven-target` | `target` | `pom.xml` or `build.sbt` | safe |
| `python-venv` | `.venv`, `venv` with `pyvenv.cfg` | a Python project file (`pyproject.toml`, `requirements*.txt`, `uv.lock`…) | moderate |
| `python-venv` | `.venv`, `venv`, `env`, `.env` or any folder holding `pyvenv.cfg`, without a requirements file | none | caution |
| `python-tox` | `.tox`, `.nox` | a Python project file | safe |
| `python-cache` | `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.pytype`, `.pyre`, grouped per project | none | safe |
| `htmlcov` | `htmlcov` with a report inside | a Python project file | safe |
| `ruby-vendor-bundle` | `vendor/bundle` | `Gemfile` | moderate |
| `elixir-build`, `elixir-deps` | `_build`, `deps` | `mix.exs` | safe, moderate |
| `zig-cache`, `haskell-stack-work`, `cabal-dist`, `dotnet-build` | `zig-cache`, `.zig-cache`, `zig-out`, `.stack-work`, `dist-newstyle`, `bin` and `obj` | `build.zig`, `stack.yaml`, Cabal files, `*.csproj` | safe |

### Generic

| Kind | What it is | Risk | Method |
|---|---|---|---|
| `cachedir-tag` | Any folder holding a valid `CACHEDIR.TAG` (written by cargo, uv, ccache…) | safe | `delete` |
| `extra-artifact` | A folder name listed in `extra_artifacts` | moderate | `delete` |
| `ignored-dir` | A git-ignored folder of 200 MB or more (not counting known artifacts inside) that no rule recognizes: agent QA caches, staging copies, recordings | caution | `delete`; `report` when it contains a git checkout |

## apple

Xcode and iOS simulators. Fixed Xcode caches (CocoaPods, SwiftPM, Carthage, CoreSimulator logs…) are [catalog](#catalog) entries.

| Kind | What it is | Risk | Method | Recommended |
|---|---|---|---|---|
| `xcode-derived-data` | One DerivedData folder (Xcode creates one per workspace path, so one per worktree), including a custom DerivedData location | safe | `delete`, refused while Xcode or `xcodebuild` runs | Yes; the name says `(workspace gone)` when its workspace no longer exists |
| `xcode-module-cache` | Shared module caches of a custom DerivedData location | safe | `delete` | Yes |
| `xcode-archive` | An `.xcarchive` (binary and dSYMs); the newest per app carries a warning | caution | `trash` | No |
| `xcode-device-support` | Debug symbols copied from a device, per OS version | moderate | `delete` | Stale; never the newest symbols of a platform |
| `xcode-app` | An installed Xcode other than the selected one | caution | `report` | No |
| `xcode-metal-toolchain` | The Metal shader compiler Xcode downloads separately | moderate | `command`: `xcodebuild -deleteComponent metalToolchain` | No |
| `ios-simulator` | One simulator device | moderate; caution when apps are installed in it | `command`: `xcrun simctl delete <udid>`; not selectable while booted | Shut down, no apps, stale; never a device that was never booted |
| `ios-simulators-unavailable` | Devices whose runtime is gone | safe | `command`: `xcrun simctl delete unavailable` | Yes |
| `ios-simulator-orphan` | A device folder `simctl` no longer knows (no or broken `device.plist`) | moderate | `delete` | Unused for 7 days |
| `ios-simulator-runtime` | A simulator runtime disk image | moderate; report-only when bundled with Xcode | `command`: `xcrun simctl runtime delete <id>` | Not used by any simulator and not the newest of its platform |
| `ios-simulator-device-set` | SwiftUI Previews, Interface Builder, parallel-testing and Playground device sets | safe | `command`: `xcrun simctl --set <dir> delete all` | Yes |
| `ios-simulator-attachments` | XCTest screen recordings left inside a simulator by UI-test automation | safe | `delete` | When the simulator is shut down |
| `ios-simulator-logs` | Unified logs of shut-down simulators | safe | `delete` | Yes |
| `ios-simulator-caches` | System caches of simulators unused for 2+ weeks | moderate | `delete` | Stale |
| `ios-simulator-dyld-cache` | Root-owned dyld caches for another macOS build or a removed runtime | safe | `report` (the note gives the `sudo rm` command) | No |
| `coresimulator-caches` | Per-user CoreSimulator caches and temp files | safe | `delete`, refused while Simulator runs | Yes |

Simulator devices, and the recordings, logs and caches taken from inside them, are re-checked with `simctl` right before cleaning: if a simulator was booted since the scan, the item is skipped.

## android

The Android toolchain of a React Native or Expo developer. SDKs and AVD folders that really live on another volume (often behind symlinks) are reported, never deleted, and dangling symlinks are never touched. Versioned SDK packages are compared with what your scanned projects use (Gradle wrapper versions, `compileSdk`, `ndkVersion`, build-tools and CMake versions): without project roots, usage is unknown and nothing versioned is recommended.

| Kind | What it is | Risk | Method | Recommended |
|---|---|---|---|---|
| `android-avd` | An emulator: folder and `.ini` | caution; moderate for x86 AVDs on Apple Silicon | `delete`, refused while the emulator runs | Only x86 AVDs on Apple Silicon (they cannot boot) |
| `android-avd-snapshots` | Quick Boot snapshots of an AVD | moderate; caution when it includes snapshots you saved | `delete` | Stale |
| `android-avd-orphan-ini` | An AVD `.ini` whose folder was deleted | safe | `delete` | Yes |
| `android-avd-home` | An AVD folder outside the internal volume | caution | `report` | No |
| `android-system-image` | An emulator system image | moderate; not selectable while an AVD uses it | `delete` | When no AVD uses it, or it is x86 on Apple Silicon |
| `android-ndk`, `android-build-tools`, `android-platform`, `android-cmake` | Versioned SDK packages | moderate; safe for leftovers of a failed install (build-tools, platforms, CMake) | `delete` | When no scanned project uses it; never the newest version; CMake 3.22.1 (AGP 8 default) is kept |
| `android-ndk-bundle` | The deprecated `ndk-bundle` folder | moderate | `delete` | Unless a project pins `ndk.dir` |
| `android-sources` | SDK sources for an API level | safe; moderate when used or newest | `delete` | Yes (safe ones) |
| `android-sdk-temp` | SDK Manager temp files and partial downloads | safe | `delete` | Yes |
| `android-sdk-legacy-tools`, `android-cmdline-tools-old`, `android-haxm` | Deprecated SDK Tools, superseded cmdline-tools, Intel HAXM on Apple Silicon | moderate | `delete` | Yes |
| `android-sdk` | An SDK outside the internal volume | moderate | `report` | No |
| `android-gradle-dist` | A Gradle wrapper distribution | moderate; safe for an interrupted download | `delete` | When no scanned project uses that version, or the download was interrupted |
| `android-gradle-version-cache` | Per-version Gradle caches | moderate | `delete` | When no scanned project nor installed Gradle uses that version; otherwise stale |
| `android-gradle-transforms`, `android-gradle-jars`, `android-gradle-build-cache` | Transform, jar and local build caches | moderate; safe for old cache formats and the build cache | `delete` | Old formats and build cache: yes; current transforms: stale |
| `android-gradle-modules` | The dependency cache (`modules-2`) | moderate | `delete` | Stale |
| `android-gradle-caches-other` | Other shared Gradle caches | moderate | `delete` | Stale |
| `android-gradle-daemon-logs` | Daemon logs of one Gradle version | safe | `delete` | Yes |
| `android-gradle-tmp` | Temp files and interrupted downloads older than a day | safe | `delete` | Yes |
| `android-gradle-jdk` | A JDK auto-provisioned by Gradle toolchains | moderate | `delete` | No |
| `android-gradle-misc` | Native helpers, Kotlin build reports, worker state | safe | `delete` | Yes |
| `android-gradle-home` | A Gradle user home outside the internal volume | moderate | `report` | No |
| `android-studio-cache` | Caches of an Android Studio version | safe for versions no longer installed; moderate for the current one | `delete`, refused while Android Studio runs | Yes for old versions; stale for the current one |
| `android-studio-logs` | `idea.log` and crash reports | safe | `delete` | Yes |
| `android-studio-config` | Settings of an older Android Studio (the current ones are never listed) | moderate | `delete` | When the current version has its own settings |
| `android-user-cache` | `~/.android` repository cache, legacy build cache, crash dumps (keys and settings are kept) | safe | `delete` | Yes |
| `android-jdk` | A JDK in your home folder, installed by an IDE, SDKMAN, mise or asdf | moderate; not selectable when in use | `delete` | No |
| `android-jdk-system` | A JDK in `/Library/Java/JavaVirtualMachines` | moderate | `report` | No |

Gradle items get a warning while a Gradle daemon runs (`./gradlew --stop` fixes it), which also keeps them out of smart select.

## ai

Data left by AI coding tools. Fixed locations (Claude Code debug logs, Codex caches, desktop app caches…) are [catalog](#catalog) entries; the scanner handles what needs logic. Most items are refused while the owning app runs.

| Kind | What it is | Category | Risk | Method | Recommended |
|---|---|---|---|---|---|
| `claude-code-orphan-project` | Transcripts and tool outputs of Claude Code sessions run in a folder that no longer exists (its `memory/` is kept) | ai | moderate | `delete`, re-checked right before | When the folder was read from the transcripts, not guessed from the directory name |
| `claude-code-old-sessions` | Sessions of a project untouched for 30+ days | ai | caution | `delete` | No |
| `claude-code-config-backups` | Timestamped `~/.claude.json` backups older than 7 days (the newest is kept) | ai | moderate | `delete` | Stale |
| `claude-code-old-version` | Superseded Claude Code native builds | ai | moderate | `delete` | Yes |
| `claude-desktop-old-claude-code`, `claude-desktop-old-claude-code-vm` | Old Claude Code copies downloaded by the Claude desktop app | ai | safe | `delete` | Yes |
| `cursor-agent-old-version`, `cursor-agent-bundled-old-version`, `cursor-origin-old-version`, `copilot-cli-old-version`, `vibe-kanban-old-version`, `conductor-old-agent-binary` | Superseded CLI and agent builds (the active one is kept) | ai | safe | `delete` | Yes |
| `codex-old-sessions` | Codex rollouts untouched for 30+ days, per month (pinned threads kept) | ai | caution | `delete` | No |
| `codex-archived-sessions` | Rollouts of threads you archived, per month | ai | caution | `delete` | No |
| `codex-logs-db` | Codex tracing logs database (with its `-wal`/`-shm`) | ai | safe | `delete` | Yes |
| `codex-stale-logs-db` | An old copy of the logs database | ai | safe | `delete` | Yes |
| `codex-visualizations` | Agent screenshots and recordings older than 30 days | ai | caution | `delete` | No |
| `multica-task-codex-homes` | Codex home copies of finished Multica tasks | ai | moderate | `delete` | Yes |
| `cursor-agent-orphan-projects` | Cursor agent data of deleted folders | ai | moderate | `delete` | Yes |
| `cursor-agent-mcp-caches` | Per-project MCP descriptor caches | ai | safe | `delete` | Yes |
| `cursor-agent-old-transcripts` | Agent transcripts untouched for 30+ days | ai | caution | `delete` | No |
| `cursor-cached-data-old-builds` | Code caches of previous Cursor builds | ide | safe | `delete` | Yes |
| `cursor-workspace-storage-orphans` | Workspace state of deleted folders | ide | moderate | `delete` | Yes |
| `cursor-workspace-dead-extension-data` | Workspace data of uninstalled extensions | ide | safe | `delete` | Yes |
| `cursor-extension-old-versions` | Superseded extension versions | ide | moderate | `delete` | Yes |
| `conductor-archived-contexts` | Notes and attachments of Conductor workspaces archived 30+ days ago | ai | caution | `delete` | No |
| `chatgpt-atlas-leftover` | ChatGPT Atlas data when the app is not installed | ai | caution | `delete` | No |
| `antigravity-browser-profile`, `antigravity-extensions` | Antigravity data when the app is not installed | ai | moderate | `delete` | Stale |
| `antigravity-browser-recordings` | Browser-agent recordings older than 90 days | ai | caution | `delete` | No |
| `antigravity-conversations`, `antigravity-brain` | Antigravity transcripts and agent memory | ai | caution | `report` | No |
| `ollama-model`, `lmstudio-model`, `huggingface-model`, `huggingface-dataset`, `huggingface-space`, `whisper-model`, `voiceink-whisper-model` | Local model weights | ai | caution | `delete` | No |

Configuration, credentials, memories, history and chat databases of these tools are [protected](/lu-cleaner/concepts/safety/#3-protected-paths). See [AI tools data](/lu-cleaner/guides/ai-tools-data/).

## js

The JavaScript and React Native toolchain, where logic is needed. Package-manager caches at fixed paths (npm, Yarn classic, bun, Metro, Jest…) are [catalog](#catalog) entries.

| Kind | What it is | Risk | Method | Recommended |
|---|---|---|---|---|
| `node-version` | A Node.js version installed by nvm, fnm, mise, asdf or volta | moderate; caution when it is your default or the `node` on your `PATH`; not selectable while a process runs it | `delete` | When nothing references it: not default, not on `PATH`, not pinned by a project (`.nvmrc`, `.node-version`, `.tool-versions`), not an alias, not running, and no global package installed only there |
| `npm-global-leftover` | A `.name-XXXXXXXX` folder left by an interrupted `npm install -g`, older than a day | safe | `delete` | Yes |
| `npx-package` | A package installed on the fly by `npx` (often an MCP server) | moderate | `delete` | Unused for 14 days and not used by a running process |
| `pnpm-store` | A pnpm content-addressable store (one per store version) | moderate | `delete` | Stale |
| `pnpm-store-prune` | Unreferenced packages of the active store (the size shown is the whole store, an upper bound) | safe | `command`: `pnpm store prune` | Yes |
| `yarn-berry-cache` | Yarn Berry global zip cache | safe; moderate when Plug'n'Play projects read it | `delete` | Yes; stale when PnP projects exist |
| `yarn-berry-metadata` | Yarn Berry registry metadata | safe | `delete` | Yes |
| `yarn-berry-store` | Yarn Berry `hardlinks-global` store (files still linked into `node_modules` survive) | moderate | `delete` | Stale |
| `expo-go-ios` | Expo Go builds cached for the iOS simulator | safe; moderate for the newest build of an SDK version | `delete` | Superseded builds: yes; the newest: stale, unless a project uses that SDK |
| `playwright-browser` | A Playwright browser revision | moderate | `delete` | When no installed Playwright needs it, or a newer revision of the same browser exists |
| `rn-codegen-tmp`, `vitest-tmp` | React Native codegen and Vitest temp folders in `$TMPDIR`, older than 2 hours | safe | `delete` | Yes |
| `fnm-multishells` | fnm's per-shell symlinks of shells that are gone (only the links, never Node itself) | safe | `delete` | Yes |
| `watchman-stale-watches` | Watchman watches on deleted folders (frees memory, not disk) | safe | `command`: `watchman watch-del-all` | Yes |

See [JS toolchain](/lu-cleaner/guides/js-toolchain/).

## system

Everything outside projects and mobile or AI toolchains: Trash, containers, Homebrew and language toolchains, editors, Downloads, and the macOS facts that explain a full disk.

| Kind | What it is | Category | Risk | Method | Recommended |
|---|---|---|---|---|---|
| `trash` | Everything in `~/.Trash` | system | moderate | `delete` (empties it) | Stale: nothing was trashed during `stale_after` |
| `trash-finder` | The Trash when lu-cleaner cannot read it (no Full Disk Access) | system | caution | `command`: Finder's "empty trash" through `osascript` | No |
| `downloads-installers`, `downloads-archives`, `downloads-mobile-builds` | `.dmg`/`.pkg`, archives and `.ipa`/`.apk` files in `~/Downloads` not downloaded nor opened for 30 days | system | caution | `delete` | No |
| `app-update-downloads` | Updates downloaded by apps' built-in updaters | system | safe | `delete` | Yes |
| `ios-device-backup`, `ios-device-backups` | Local iPhone and iPad backups | system | caution | `report` | No |
| `time-machine-snapshots`, `macos-swap`, `macos-staged-updates` | APFS local snapshots, swap files, staged macOS updates | system | never | `report` | No |
| `docker-build-cache` | Unused BuildKit cache | containers | safe | `command`: `docker builder prune -a -f` | Yes |
| `docker-unused-images` | Images no container uses | containers | moderate | `command`: `docker image prune -a -f` | No |
| `docker-stopped-containers` | Stopped containers and their writable layer | containers | caution | `command`: `docker container prune -f` | No |
| `docker-unused-volumes` | Volumes no container uses | containers | caution | `report` | No |
| `docker-desktop-disk`, `orbstack-data`, `colima-vm`, `lima-vm` | VM disk images holding containers and images | containers | caution | `report` | No |
| `apple-container-data` | Images and snapshots of Apple's `container` CLI | containers | moderate | `report` | No |
| `homebrew-cache` | Homebrew download cache (its API metadata is kept) | langs | safe | `delete` | Yes |
| `homebrew-cleanup` | Old versions, stale locks and logs, as computed by `brew cleanup -n` | langs | moderate | `command`: `brew cleanup --prune=all` | Yes |
| `homebrew-old-kegs` | Old versions kept by outdated formulae | langs | moderate | `report` | No |
| `go-build-cache` | Go build and test cache | langs | safe | `command`: `go clean -cache` (a plain delete when `go` is not installed) | Yes |
| `go-module-cache` | Go module cache | langs | moderate | `command`: `go clean -modcache` | Stale |
| `rustup-toolchain`, `rbenv-ruby` | Rust toolchains other than the default, rbenv Rubies neither global nor pinned | langs | moderate | `report` | No |
| `vscode-cached-data` (and `vscode-insiders-…`, `vscodium-…`) | Code caches of previous editor builds | ide | safe | `delete` | Yes |
| `vscode-vsix-cache`, `vscode-caches`, `vscode-ls-indexes` | Downloaded `.vsix` packages, caches and old logs, language-server indexes untouched for 3 days | ide | safe | `delete` | Yes |
| `vscode-workspace-orphans` | Workspace storage of deleted folders, untouched for 7+ days | ide | moderate | `delete` | Yes |
| `vscode-old-extensions` | Superseded extension versions | ide | safe | `delete` | Yes |
| `vscode-local-history` | Local History (Timeline) | ide | caution | `report` | No |
| `vscode-extensions-leftover` | Extensions of an editor that is no longer installed | ide | caution | `delete` | No |
| `zed-caches`, `zed-language-servers`, `zed-leftover` | Zed caches, downloaded language servers, data of an uninstalled Zed | ide | safe, moderate, caution | `delete` | Yes, stale, no |
| `jetbrains-old-caches`, `jetbrains-logs` | JetBrains caches of IDE versions no longer installed, IDE logs | ide | safe | `delete` | Yes |
| `jetbrains-old-settings`, `jetbrains-caches` | Settings of old IDE versions, caches of current ones | ide | moderate | `delete` | Stale |

See [Disk space not freed](/lu-cleaner/guides/disk-space-not-freed/) for snapshots, the Trash and Docker disk images.

## catalog

The catalog scanner evaluates about 200 entries: well-known cache, log and tool-data locations under your home or your per-user temporary folder. Each entry's `id` is the item kind. The full list, with risk, method, paths and notes, is in the [catalog reference](/lu-cleaner/reference/catalog/), and on your machine:

```bash
lu-cleaner catalog              # every entry
lu-cleaner catalog -c js        # one category
lu-cleaner catalog -k claude    # ids containing "claude"
lu-cleaner catalog --json
```

How entries become items:

- Paths are globs (`~/.cache/node/corepack/*`, `$TMPDIR/bunx-*`). By default all matches of an entry form **one group item** (`Metro / Haste file maps (18)`); some entries produce **one item per match** instead (one per downloaded version, for example), optionally keeping the newest ones out.
- An entry can require a binary on `PATH` (for its command), only keep matches older than a given age, hide results below a minimum size, and declare apps that must not run while it is cleaned. A running app shows as a warning at scan time.
- Excluded paths are never matched, protected paths only by report-only entries, dangling symlinks are ignored, and matches living on another volume are report-only.
- The last-used date of a group is the newest file written inside it, since a cache folder's own modification date only changes when entries are added or removed.
- Risks and recommendations follow the generic rules; an entry can force the recommendation (`Flipper leftovers`).

To add an entry, see [Contributing](/lu-cleaner/about/contributing/#add-a-catalog-entry).
