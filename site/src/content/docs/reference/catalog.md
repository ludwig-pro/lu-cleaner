---
title: "Catalog of known locations"
description: "Every well-known cache, log and tool-data path lu-cleaner checks, with its risk and cleaning method."
sidebar:
  order: 1
---

:::note[Generated]
This page is generated from the catalog in the source code (`make docs`). Run `lu-cleaner catalog` to print it on your machine. `~` is your home folder and `$TMPDIR` your per-user temporary folder.
:::

These are the **static** locations. Things that need logic — git worktrees, project artifacts, simulators, emulators, node versions, AI sessions by age… — are found by dedicated scanners, described in [What gets scanned](/lu-cleaner/reference/scanners/).

**202 entries.** Risk: `safe` = pure cache, `moderate` = regenerable at some cost, `caution` = may hold data you care about, `never` = report only.

## 📱 iOS Simulators

simulator devices, runtimes and caches — 2 entries.

#### CoreSimulator logs

<span class="risk safe">safe</span> · delete · <code>apple-coresimulator-logs</code>

- `~/Library/Logs/CoreSimulator/*`

Host-side logs of CoreSimulator and of each simulator (per-UDID folders) not written for a week; recreated by simulator activity. _(only items older than 7 days)_

#### Simulator device-set temp copies

<span class="risk safe">safe</span> · delete · <code>apple-simulator-device-set-tmp</code>

- `~/Library/Developer/CoreSimulator/Devices/device_set.plist.sb-*`

Leftovers of interrupted atomic writes of device_set.plist (the registry itself is never touched). _(only items older than 1 days)_

## 🔨 Xcode

DerivedData, Archives, DeviceSupport, CocoaPods, SwiftPM — 13 entries.

#### Carthage cache

<span class="risk moderate">moderate</span> · delete · <code>apple-carthage-cache</code>

- `~/Library/Caches/org.carthage.CarthageKit`

Carthage dependency clones and downloaded binaries; `carthage bootstrap` fetches them again (slow).

#### CocoaPods download cache

<span class="risk safe">safe</span> · delete · <code>apple-cocoapods-cache</code>

- `~/Library/Caches/CocoaPods`

Pod sources and podspecs downloaded by `pod install` (same as `pod cache clean --all`); the next pod install downloads what it needs again.

#### CocoaPods legacy master spec repo

<span class="risk moderate">moderate</span> · delete · <code>apple-cocoapods-master-repo</code>

- `~/.cocoapods/repos/master`
- `~/.cocoapods/repos/cocoapods`

Git clone of the whole public Specs repo, unused since CocoaPods 1.8 switched to the CDN (`pod repo remove master`); only re-cloned if a Podfile still declares the GitHub Specs source.

#### CocoaPods trunk spec index

<span class="risk moderate">moderate</span> · delete · <code>apple-cocoapods-trunk</code>

- `~/.cocoapods/repos/trunk`

CDN index of public podspecs; pod install re-fetches the specs it needs lazily (slower first install). Private spec repos are left alone.

#### Device crash logs imported by Xcode

<span class="risk caution">caution</span> · delete · <code>apple-device-logs</code>

- `~/Library/Developer/Xcode/iOS Device Logs`
- `~/Library/Developer/Xcode/watchOS Device Logs`

Crash and device logs copied from connected devices (Devices window). Devices purge their own crash logs, so these copies are often the only ones left: keep the ones you may still need to symbolicate; Xcode only re-imports logs still on a connected device. _(refused while Xcode runs)_

#### Xcode component downloads

<span class="risk moderate">moderate</span> · delete · <code>apple-dvt-downloads</code>

- `~/Library/Developer/DVTDownloads/*`

Staging area of Xcode component downloads (runtimes, Metal toolchain, personalization manifests); installed components are not affected, an interrupted download restarts. _(refused while Xcode, xcodebuild runs)_

#### fastlane caches

<span class="risk moderate">moderate</span> · delete · <code>apple-fastlane-caches</code>

- `~/Library/Caches/tools.fastlane`
- `~/.fastlane/frameit`

fastlane snapshot working files and frameit device frames; downloaded again when snapshot / frameit run. Session cookies (~/.fastlane/spaceship) are not touched.

#### Swift Package Manager cache

<span class="risk safe">safe</span> · delete · <code>apple-swiftpm-cache</code>

- `~/Library/Caches/org.swift.swiftpm`

Global SwiftPM repository & artifact cache (`swift package purge-cache`); packages are cloned again on the next resolve. Mirrors and registry settings (~/Library/org.swift.swiftpm) are not touched.

#### Xcode app caches

<span class="risk safe">safe</span> · delete · <code>apple-xcode-caches</code>

- `~/Library/Caches/com.apple.dt.*`

Caches of Xcode, xcodebuild and Instruments (~/Library/Caches/com.apple.dt.*); recreated automatically. _(refused while Xcode runs)_

#### Xcode export / distribution staging

<span class="risk safe">safe</span> · delete · <code>apple-xcode-dist-staging</code>

- `$TMPDIR/XcodeDistPipeline.*`
- `$TMPDIR/*.xcdistributionlogs`

Temporary copies and logs of Organizer › Distribute App / xcodebuild -exportArchive runs; recreated per export. _(refused while Xcode, xcodebuild runs; only items older than 1 days)_

#### Xcode documentation cache

<span class="risk safe">safe</span> · delete · <code>apple-xcode-docs-cache</code>

- `~/Library/Developer/Xcode/DocumentationCache/*`
- `~/Library/Developer/Xcode/DocumentationIndex/*`

Downloaded developer documentation and its search index; Xcode fetches them again when the documentation window is opened. _(refused while Xcode runs)_

#### Xcode shared module cache

<span class="risk safe">safe</span> · delete · <code>apple-xcode-module-cache</code>

- `~/Library/Developer/Xcode/DerivedData/*.noindex`

ModuleCache.noindex / SymbolCache.noindex shared by every project: precompiled clang & Swift modules, rebuilt by the next build (slower first build). _(refused while Xcode, xcodebuild runs)_

#### Xcode install products

<span class="risk safe">safe</span> · delete · <code>apple-xcode-products</code>

- `~/Library/Developer/Xcode/Products/*`

Output of Product › Build For › Installing / xcodebuild install; recreated by the next such build. _(refused while Xcode, xcodebuild runs)_

## 🤖 Android

Gradle, emulators (AVD), SDK images, NDK, Android Studio — 8 entries.

#### Android emulator temp files

<span class="risk safe">safe</span> · delete · <code>android-emulator-tmp</code>

- `$TMPDIR/android-*`

Emulator temp folder (running-instance discovery files, crash reports); recreated at the next emulator launch. _(refused while qemu-system-aarch64, qemu-system-x86_64 runs)_

#### Genymotion virtual device

<span class="risk caution">caution</span> · report · <code>android-genymotion-device</code>

- `~/.Genymobile/Genymotion/deployed/*`

Genymotion virtual device with its data; delete it from Genymotion if you no longer use it.

#### Kotlin/Native dependencies & caches (~/.konan)

<span class="risk moderate">moderate</span> · delete · <code>android-konan-dependencies</code>

- `~/.konan/dependencies`
- `~/.konan/cache`

Kotlin Multiplatform / Kotlin/Native toolchain dependencies (LLVM, sysroots) and caches; re-downloaded by the next Kotlin/Native build (1-3 GB).

#### Kotlin/Native compiler

<span class="risk moderate">moderate</span> · delete · <code>android-konan-prebuilt</code>

- `~/.konan/kotlin-native-prebuilt-*`

Kotlin/Native compiler of one Kotlin version (the newest one is kept); re-downloaded by the next KMP build using that version. _(keeps the 1 newest)_

#### Kotlin daemon state (~/.kotlin)

<span class="risk moderate">moderate</span> · delete · <code>android-kotlin-daemon</code>

- `~/.kotlin`
- `~/Library/Application Support/kotlin/daemon`

Kotlin compile daemon run files, session data and logs at user level; recreated by the next Kotlin/Gradle build.

#### APKs stored for Maestro

<span class="risk caution">caution</span> · report · <code>android-maestro-apks</code>

- `~/.maestro/apps/android/*.apk`

App builds copied under ~/.maestro/apps for Maestro flows (origin unknown); rebuild or download them again before removing them by hand.

#### Maven local repository (~/.m2/repository)

<span class="risk caution">caution</span> · delete · <code>android-maven-local</code>

- `~/.m2/repository`

Maven local repository: remote artifacts are re-downloaded, but artifacts published locally (publishToMavenLocal, mvn install) cannot be; settings.xml is kept.

#### Skiko native libs

<span class="risk safe">safe</span> · delete · <code>android-skiko</code>

- `~/.skiko/*`

Skia native libraries extracted by Compose Desktop apps (Maestro Studio...); re-extracted on launch (the newest copy is kept). _(keeps the 1 newest)_

## 🧠 AI tools

Claude, Codex, Cursor, ChatGPT, Conductor data & caches — 46 entries.

#### ChatGPT desktop caches

<span class="risk safe">safe</span> · delete · <code>ai-chatgpt-caches</code>

- `~/Library/Caches/com.openai.chat`
- `~/Library/Caches/ChatGPTHelper`

HTTP and helper caches of the ChatGPT desktop app; rebuilt. _(refused while ChatGPT, ChatGPT Classic runs)_

#### ChatGPT desktop conversation cache

<span class="risk moderate">moderate</span> · delete · <code>ai-chatgpt-conversations-cache</code>

- `~/Library/Application Support/com.openai.chat/conversations-v3-*`

Local copy of ChatGPT conversations; re-synced from the server on demand. _(refused while ChatGPT, ChatGPT Classic runs)_

#### chrome-devtools-mcp browser caches

<span class="risk safe">safe</span> · delete · <code>ai-chrome-devtools-mcp-cache</code>

- `~/.cache/chrome-devtools-mcp/chrome-profile/Default/Cache`
- `~/.cache/chrome-devtools-mcp/chrome-profile/Default/Code Cache`
- `~/.cache/chrome-devtools-mcp/chrome-profile/Default/Service Worker/CacheStorage`
- `~/.cache/chrome-devtools-mcp/chrome-profile/GraphiteDawnCache`
- `~/.cache/chrome-devtools-mcp/chrome-profile/GrShaderCache`

HTTP / code caches of the Chrome profile driven by the chrome-devtools MCP server; rebuilt (the profile's logins are kept).

#### Claude Code debug logs & shell snapshots

<span class="risk safe">safe</span> · delete · <code>ai-claude-code-debug-logs</code>

- `~/.claude/debug/*.txt`
- `~/.claude/shell-snapshots/*`

Per-session debug logs and shell environment snapshots of past Claude Code sessions; recreated for each new session. _(only items older than 1 days)_

#### Claude Code file checkpoints (rewind) > 30d

<span class="risk caution">caution</span> · delete · <code>ai-claude-code-file-history</code>

- `~/.claude/file-history/*`

File snapshots backing /rewind for sessions older than 30 days; those sessions can no longer restore files. _(only items older than 30 days)_

#### Claude Code MCP logs cache

<span class="risk safe">safe</span> · delete · <code>ai-claude-code-mcp-logs</code>

- `~/Library/Caches/claude-cli-nodejs/*`

Per-project MCP server logs written by the Claude Code CLI; recreated. _(only items older than 1 days)_

#### Claude Code paste cache

<span class="risk moderate">moderate</span> · delete · <code>ai-claude-code-paste-cache</code>

- `~/.claude/paste-cache/*`

Large pasted texts referenced by the prompt history; losing them only affects re-showing old pastes. _(only items older than 30 days)_

#### Claude Code plan files > 60d

<span class="risk caution">caution</span> · delete · <code>ai-claude-code-plans</code>

- `~/.claude/plans/*.md`

Plans written in plan mode by old sessions; not regenerated. _(only items older than 60 days)_

#### Claude Code per-session temp dirs

<span class="risk safe">safe</span> · delete · <code>ai-claude-code-session-temp</code>

- `~/.claude/session-env/*`
- `~/.claude/tasks/*`
- `~/.claude/plugins/.trash/*`

Per-session environment / task scratch dirs and removed plugins of sessions older than a week; recreated per session. _(only items older than 7 days)_

#### Claude Code telemetry & changelog cache

<span class="risk safe">safe</span> · delete · <code>ai-claude-code-telemetry</code>

- `~/.claude/telemetry/*`
- `~/.claude/statsig/*`
- `~/.claude/cache/changelog.md`

Failed telemetry batches, feature-flag cache and the cached changelog; re-fetched automatically. _(only items older than 1 days)_

#### Claude Code old todo lists

<span class="risk safe">safe</span> · delete · <code>ai-claude-code-todos</code>

- `~/.claude/todos/*`

Todo lists of sessions older than a week; recreated per session. _(only items older than 7 days)_

#### Claude desktop Electron caches

<span class="risk safe">safe</span> · delete · <code>ai-claude-desktop-caches</code>

- `~/Library/Application Support/Claude/Cache`
- `~/Library/Application Support/Claude/Code Cache`
- `~/Library/Application Support/Claude/GPUCache`
- `~/Library/Application Support/Claude/DawnCache`
- `~/Library/Application Support/Claude/DawnGraphiteCache`
- `~/Library/Application Support/Claude/DawnWebGPUCache`
- `~/Library/Application Support/Claude/GraphiteDawnCache`
- `~/Library/Application Support/Claude/GrShaderCache`
- `~/Library/Application Support/Claude/Shared Dictionary`
- `~/Library/Application Support/Claude/Partitions/*/Cache`
- `~/Library/Application Support/Claude/Partitions/*/Code Cache`
- `~/Library/Application Support/Claude/Partitions/*/GPUCache`
- `~/Library/Application Support/Claude/Partitions/*/Shared Dictionary`
- `~/Library/Caches/com.anthropic.claudefordesktop`
- `~/Library/Caches/com.anthropic.claudefordesktop.ShipIt`

Chromium HTTP / code / GPU caches and the updater cache of the Claude desktop app; rebuilt on next launch. Cookies, Local Storage and IndexedDB are kept. _(refused while Claude runs)_

#### Claude desktop agent & Code session state

<span class="risk caution">caution</span> · report · <code>ai-claude-desktop-sessions</code>

- `~/Library/Application Support/Claude/local-agent-mode-sessions`
- `~/Library/Application Support/Claude/claude-code-sessions`
- `~/Library/Application Support/Claude/git-shadow`

Session state of Cowork / Code sessions and git shadow repos used by the desktop app; kept (report only).

#### Claude desktop Cowork VM image

<span class="risk moderate">moderate</span> · delete · <code>ai-claude-desktop-vm-image</code>

- `~/Library/Application Support/Claude/vm_bundles/claudevm.bundle/rootfs.img`
- `~/Library/Application Support/Claude/vm_bundles/claudevm.bundle/initrd`
- `~/Library/Application Support/Claude/vm_bundles/claudevm.bundle/initrd-micro`
- `~/Library/Application Support/Claude/vm_bundles/claudevm.bundle/vmlinuz`

Linux VM disk image (sparse, ~10 GB allocated) used by Cowork / local agent mode; Claude desktop re-downloads it the next time Cowork starts. The session disk is kept. _(refused while Claude runs)_

#### Claude desktop Cowork VM session disk

<span class="risk caution">caution</span> · report · <code>ai-claude-desktop-vm-sessiondata</code>

- `~/Library/Application Support/Claude/vm_bundles/claudevm.bundle/sessiondata.img`

Disk holding files of Cowork sessions inside the VM; kept (report only).

#### Windsurf / Codeium Cascade data

<span class="risk caution">caution</span> · report · <code>ai-codeium-windsurf-data</code>

- `~/.codeium/windsurf`

Cascade chats and memories of Windsurf; report only.

#### Codex app Chromium caches

<span class="risk safe">safe</span> · delete · <code>ai-codex-app-caches</code>

- `~/Library/Application Support/Codex/Cache`
- `~/Library/Application Support/Codex/Code Cache`
- `~/Library/Application Support/Codex/GPUCache`
- `~/Library/Application Support/Codex/GraphiteDawnCache`
- `~/Library/Application Support/Codex/DawnGraphiteCache`
- `~/Library/Application Support/Codex/DawnWebGPUCache`
- `~/Library/Application Support/Codex/GrShaderCache`
- `~/Library/Application Support/Codex/GPUPersistentCache`
- `~/Library/Application Support/Codex/component_crx_cache`
- `~/Library/Application Support/Codex/Crashpad/pending/*`
- `~/Library/Caches/Codex`
- `~/Library/Caches/com.openai.codex/org.sparkle-project.Sparkle/Installation`

HTTP, code and GPU caches of the Codex / ChatGPT app's embedded Chromium; rebuilt on launch. The browser profile (Default/, cookies) is kept. _(refused while codex, Codex, ChatGPT, codex-code-mode-host runs)_

#### Codex remote catalog & apps caches

<span class="risk safe">safe</span> · delete · <code>ai-codex-caches</code>

- `~/.codex/cache/remote_plugin_catalog`
- `~/.codex/cache/codex_apps_tools`
- `~/.codex/cache/codex_apps_server_info`
- `~/.codex/cache/bundled_plugin_exclusions`

Remote plugin catalog and apps tool caches; re-fetched automatically (the session index and app directory caches are kept). _(refused while codex, Codex, ChatGPT, codex-code-mode-host runs)_

#### Codex chat workspaces (~/Documents/Codex)

<span class="risk caution">caution</span> · report · <code>ai-codex-documents-workspaces</code>

- `~/Documents/Codex/*`

Working folders of project-less Codex chats; they hold user files (report only).

#### Codex generated images > 30d

<span class="risk caution">caution</span> · delete · <code>ai-codex-generated-images</code>

- `~/.codex/generated_images/*`

Images generated in Codex threads older than 30 days; not regenerated. _(only items older than 30 days)_

#### Codex stale global-state temp files

<span class="risk safe">safe</span> · delete · <code>ai-codex-global-state-tmp</code>

- `~/.codex/..codex-global-state.json.tmp-*`
- `~/.codex/..codex-global-state.json.bak.tmp-*`

Leftovers of interrupted atomic writes of .codex-global-state.json (the real file and its .bak are kept). _(refused while codex, Codex, ChatGPT, codex-code-mode-host runs; only items older than 1 days)_

#### Codex CLI log files > 14d

<span class="risk moderate">moderate</span> · delete · <code>ai-codex-log-files</code>

- `~/.codex/log/*`

Old Codex CLI text logs; only useful for bug reports. _(only items older than 14 days)_

#### Codex old state / memories DB copies (sqlite/)

<span class="risk caution">caution</span> · report · <code>ai-codex-old-sqlite-state</code>

- `~/.codex/sqlite/state_*.sqlite`
- `~/.codex/sqlite/state_*.sqlite-wal`
- `~/.codex/sqlite/state_*.sqlite-shm`
- `~/.codex/sqlite/memories_*.sqlite`
- `~/.codex/sqlite/goals_*.sqlite`

Older copies of Codex state and memories DBs in ~/.codex/sqlite; may hold memories, report only.

#### Codex abandoned plugin sync backups

<span class="risk safe">safe</span> · delete · <code>ai-codex-plugin-backups</code>

- `~/.codex/.tmp/plugins-backup-*`

Backups of the plugin repository clone taken during plugin syncs; never restored. _(refused while codex, Codex, ChatGPT, codex-code-mode-host runs; only items older than 7 days)_

#### Codex plugin cache

<span class="risk moderate">moderate</span> · delete · <code>ai-codex-plugin-cache</code>

- `~/.codex/plugins/cache`

Downloaded plugin bundles (some run as helper processes); re-downloaded when a plugin is used. The active plugin app-server is kept. _(refused while ChatGPT for Chrome, codex, Codex, ChatGPT, codex-code-mode-host runs)_

#### Codex plugin sync clones

<span class="risk moderate">moderate</span> · delete · <code>ai-codex-plugin-staging</code>

- `~/.codex/.tmp/plugins`
- `~/.codex/.tmp/bundled-marketplaces`
- `~/.codex/.tmp/marketplaces/.staging`

Plugin marketplace clones used by plugin syncs; re-cloned on the next sync (bandwidth). _(refused while codex, Codex, ChatGPT, codex-code-mode-host runs)_

#### Codex DB repair backups

<span class="risk safe">safe</span> · delete · <code>ai-codex-repair-backups</code>

- `~/.codex/*.codex-repair-*.bak`
- `~/.codex/sqlite/*.codex-repair-*.bak`

One-off copies Codex made while repairing corrupted databases (*.codex-repair-<epoch>.bak); never read again. _(only items older than 7 days)_

#### Codex app runtime (node / python / native deps)

<span class="risk moderate">moderate</span> · delete · <code>ai-codex-runtimes</code>

- `~/.cache/codex-runtimes/*`

Runtime bundle downloaded by the Codex app for its tools; re-downloaded (~1.6 GB) on next use. _(refused while codex, Codex, ChatGPT, codex-code-mode-host runs)_

#### Codex shell snapshots

<span class="risk safe">safe</span> · delete · <code>ai-codex-shell-snapshots</code>

- `~/.codex/shell_snapshots/*`

Shell environment snapshots of past Codex sessions; recreated per session. _(only items older than 7 days)_

#### Codex thread history DB

<span class="risk caution">caution</span> · report · <code>ai-codex-thread-history-db</code>

- `~/.codex/thread_history_*.sqlite`

Projection of all Codex threads; shrinks only when threads are deleted in Codex (report only).

#### Codex PR archives, attachments & dictation history

<span class="risk caution">caution</span> · report · <code>ai-codex-user-content</code>

- `~/.codex/archives`
- `~/.codex/attachments`
- `~/.codex/dictation-history`

User content kept by Codex; report only.

#### Conductor client cache DB

<span class="risk caution">caution</span> · report · <code>ai-conductor-cache-db</code>

- `~/Library/Application Support/com.conductor.app/cache.db`

Message cache that also holds unsent drafts; report only. _(refused while conductor, Conductor runs)_

#### Conductor database (sessions & messages)

<span class="risk never">never</span> · report · <code>ai-conductor-db</code>

- `~/Library/Application Support/com.conductor.app/conductor.db`

Conductor sessions, messages, repos and settings; shrinks only by deleting sessions in Conductor.

#### Continue codebase index

<span class="risk safe">safe</span> · delete · <code>ai-continue-index</code>

- `~/.continue/index`

Embeddings index of the Continue extension; rebuilt on next indexing (config.* is kept).

#### Cursor background agent stores

<span class="risk caution">caution</span> · report · <code>ai-cursor-agent-stores</code>

- `~/Library/Application Support/Cursor/AgentStores/cursor_agent_stores/*`

State of Cursor background / cloud agents; kept (report only).

#### Cursor agent worker logs

<span class="risk safe">safe</span> · delete · <code>ai-cursor-agent-worker-logs</code>

- `~/Library/Application Support/Cursor/User/globalStorage/anysphere.cursor-agent-worker/cursor-agent-worker-*.log`

Logs of past Cursor background-agent worker runs. _(only items older than 7 days)_

#### Cursor global state DB (all chats)

<span class="risk never">never</span> · report · <code>ai-cursor-state-db</code>

- `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb`

Holds every Cursor agent / composer chat, settings state and auth; delete old chats in Cursor (the file only shrinks after a VACUUM with Cursor closed).

#### Cursor state DB backups

<span class="risk moderate">moderate</span> · delete · <code>ai-cursor-state-db-backups</code>

- `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb.backup`
- `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb.backup-*`

Old restore points of the chats/state DB (state.vscdb.backup is rewritten by Cursor, dated ones are one-off copies). _(refused while Cursor runs; only items older than 30 days)_

#### Cursor stale statsig temp files

<span class="risk safe">safe</span> · delete · <code>ai-cursor-statsig-tmp</code>

- `~/.cursor/statsig-cache.json.*.tmp`

Leftovers of interrupted writes of the feature-flag cache. _(only items older than 1 days)_

#### Gemini CLI checkpoints & chat state

<span class="risk caution">caution</span> · report · <code>ai-gemini-cli-tmp</code>

- `~/.gemini/tmp`

Despite its name, Gemini CLI keeps conversation checkpoints here; report only.

#### Grok CLI installer downloads

<span class="risk safe">safe</span> · delete · <code>ai-grok-downloads</code>

- `~/.grok/downloads/*`

Installer binaries kept after installing the Grok CLI (the installed ~/.grok/bin is kept).

#### opencode cache & logs

<span class="risk safe">safe</span> · delete · <code>ai-opencode-cache</code>

- `~/.cache/opencode`
- `~/.local/share/opencode/log`

opencode package cache and logs; recreated (sessions in storage/ are kept).

#### opencode sessions

<span class="risk caution">caution</span> · report · <code>ai-opencode-sessions</code>

- `~/.local/share/opencode/storage`

opencode session history; report only.

#### Raycast downloaded updates

<span class="risk safe">safe</span> · delete · <code>ai-raycast-updates</code>

- `~/Library/Application Support/com.raycast.macos/Updates/*`

Update packages downloaded by Raycast's updater; re-downloaded if needed. Raycast's encrypted DBs and extensions are untouched. _(refused while Raycast runs; only items older than 1 days)_

#### VoiceInk dictation recordings > 30d

<span class="risk caution">caution</span> · delete · <code>ai-voiceink-recordings</code>

- `~/Library/Application Support/com.prakashjoshipax.VoiceInk/Recordings/*`

Audio of past dictations (the transcripts stay in VoiceInk's history); not recoverable. _(refused while VoiceInk runs; only items older than 30 days)_

#### Warp database (history, blocks, AI conversations)

<span class="risk never">never</span> · report · <code>ai-warp-db</code>

- `~/Library/Group Containers/2BBY89MBSN.dev.warp/Library/Application Support/dev.warp.Warp-Stable/warp.sqlite*`

Warp terminal history and Agent Mode conversations; never deleted.

## 🟨 JS toolchain

npm, yarn, pnpm, bun, node versions, Metro, Expo — 43 entries.

#### asdf nodejs downloads

<span class="risk safe">safe</span> · delete · <code>js-asdf-node-downloads</code>

- `~/.asdf/downloads/nodejs`

Archives kept by asdf-nodejs after installing; not needed by installed versions.

#### Bun install cache

<span class="risk moderate">moderate</span> · delete · <code>js-bun-install-cache</code>

- `~/.bun/install/cache`

Packages downloaded by `bun install` (re-downloaded when needed). bun clones files into node_modules on APFS, so the real gain can be much smaller than the size shown.

#### Bun transpiler cache

<span class="risk safe">safe</span> · delete · <code>js-bun-transpiler-cache</code>

- `~/Library/Caches/bun`

Runtime transpiler cache of bun; rebuilt automatically.

#### bunx temporary installs

<span class="risk safe">safe</span> · delete · <code>js-bunx-tmp</code>

- `$TMPDIR/bunx-*`

Packages installed by `bunx <pkg>`; the next bunx call reinstalls them. _(only items older than 1 days)_

#### Convex local backend binaries

<span class="risk safe">safe</span> · delete · <code>js-convex-binaries</code>

- `~/.cache/convex/binaries`
- `~/.cache/convex/dashboard`

Local backend and dashboard downloaded by `npx convex dev`; fetched again for local deployments.

#### Corepack package manager downloads

<span class="risk safe">safe</span> · delete · <code>js-corepack-cache</code>

- `~/.cache/node/corepack/*`

Yarn/pnpm versions downloaded by corepack (and partial corepack-* downloads); fetched again on first use. lastKnownGood.json is kept.

#### Cypress binary

<span class="risk moderate">moderate</span> · delete · <code>js-cypress-binaries</code>

- `~/Library/Caches/Cypress/*`

Older Cypress app versions (the newest is kept); the cypress postinstall downloads the one a project needs. _(keeps the 1 newest)_

#### Deno cache

<span class="risk safe">safe</span> · delete · <code>js-deno-cache</code>

- `~/Library/Caches/deno`

Remote modules and npm packages cached by Deno (DENO_DIR); re-downloaded on the next run.

#### DotSlash cache (React Native DevTools)

<span class="risk safe">safe</span> · delete · <code>js-dotslash-cache</code>

- `~/Library/Caches/dotslash`

Binaries fetched by DotSlash, e.g. React Native DevTools; re-downloaded when the debugger is opened.

#### EAS CLI cache

<span class="risk safe">safe</span> · delete · <code>js-eas-cli-cache</code>

- `~/Library/Caches/eas-cli`

Metadata cached by eas-cli; re-fetched automatically.

#### EAS CLI upload archives

<span class="risk safe">safe</span> · delete · <code>js-eas-cli-tmp</code>

- `$TMPDIR/eas-cli-nodejs`

Project tarballs prepared by `eas build` / `eas update` uploads; recreated on the next upload.

#### EAS local build workdirs

<span class="risk safe">safe</span> · delete · <code>js-eas-local-build</code>

- `$TMPDIR/eas-build-local-nodejs`

Working directories of `eas build --local`; recreated per build (do not clean during a local build). _(only items older than 0 days)_

#### Electron download cache

<span class="risk safe">safe</span> · delete · <code>js-electron-cache</code>

- `~/Library/Caches/electron`
- `~/Library/Caches/electron-builder`
- `~/.cache/electron`
- `~/.cache/electron-builder`

Electron / electron-builder downloads; fetched again by the next install or build.

#### esbuild binary cache

<span class="risk safe">safe</span> · delete · <code>js-esbuild-cache</code>

- `~/Library/Caches/esbuild`
- `~/.cache/esbuild`

esbuild binaries downloaded as install fallback; fetched again when needed.

#### Expo CLI caches

<span class="risk safe">safe</span> · delete · <code>js-expo-cli-caches</code>

- `~/.expo/native-modules-cache`
- `~/.expo/schema-cache`
- `~/.expo/versions-cache`
- `~/.expo/cache`
- `~/.expo/template-cache`

Schemas, SDK versions and templates cached by expo-cli; re-fetched automatically. The Expo login (~/.expo/state.json) is never touched.

#### Expo Go Android APK cache

<span class="risk safe">safe</span> · delete · <code>js-expo-go-android</code>

- `~/.expo/android-apk-cache`

Expo Go APKs downloaded by `expo start --android`; downloaded again on demand.

#### Flipper leftovers

<span class="risk safe">safe</span> · delete · <code>js-flipper-leftovers</code>

- `~/.flipper`

Flipper was removed from React Native 0.74+; these files are no longer used.

#### giget template cache

<span class="risk safe">safe</span> · delete · <code>js-giget-cache</code>

- `~/.cache/giget`

Templates downloaded by create-* / nuxi scaffolders; fetched again when scaffolding.

#### Jest cache

<span class="risk safe">safe</span> · delete · <code>js-jest-cache</code>

- `$TMPDIR/jest_*`

Jest transform and haste-map cache, one set per project path (every worktree adds ~100 MB); rebuilt on the next run.

#### Metro bundler cache

<span class="risk safe">safe</span> · delete · <code>js-metro-cache</code>

- `$TMPDIR/metro-cache`
- `$TMPDIR/metro-bundler-cache*`
- `$TMPDIR/react-native-packager-cache-*`

Metro transform cache shared by every RN/Expo project; rebuilt on the next bundle (first start is slower, like --reset-cache).

#### Metro / Haste file maps

<span class="risk safe">safe</span> · delete · <code>js-metro-file-maps</code>

- `$TMPDIR/metro-file-map-*`
- `$TMPDIR/haste-map-*`

One file map per project root (every worktree adds one); rebuilt when Metro starts.

#### mise download cache

<span class="risk safe">safe</span> · delete · <code>js-mise-cache</code>

- `~/Library/Caches/mise`
- `~/.local/share/mise/downloads`

Archives and metadata cached by mise; installed tools are unaffected.

#### Next.js SWC binaries

<span class="risk safe">safe</span> · delete · <code>js-next-swc-cache</code>

- `~/Library/Caches/next-swc`
- `~/.cache/next-swc`

SWC binaries downloaded by Next.js as fallback; fetched again when needed.

#### Node module compile cache

<span class="risk safe">safe</span> · delete · <code>js-node-compile-cache</code>

- `$TMPDIR/node-compile-cache`
- `$TMPDIR/v8-compile-cache-*`

V8 code cache of CLIs using module.enableCompileCache (npm, AI CLIs...), one per Node version; rebuilt on the next run.

#### node-gyp Node headers

<span class="risk safe">safe</span> · delete · <code>js-node-gyp-headers</code>

- `~/Library/Caches/node-gyp`
- `~/.cache/node-gyp`
- `~/.node-gyp`
- `~/.electron-gyp`

Node headers downloaded to build native addons; re-downloaded (~60 MB) at the next native build.

#### npm postinstall binary downloads

<span class="risk safe">safe</span> · delete · <code>js-npm-binary-downloads</code>

- `~/.npm/sentry-cli`
- `~/.npm/_libvips`
- `~/.npm/_prebuilds`
- `~/Library/Caches/sentry-cli`
- `~/.cache/sentry-cli`

Binaries downloaded by postinstall scripts (@sentry/cli, sharp libvips, prebuild-install); fetched again by the next install that needs them.

#### npm cache (_cacache)

<span class="risk safe">safe</span> · delete · <code>js-npm-cache</code>

- `~/.npm/_cacache`

npm's content cache; the next `npm install` / `npx` re-downloads what it needs. Existing node_modules are unaffected.

#### npm debug logs

<span class="risk safe">safe</span> · delete · <code>js-npm-logs</code>

- `~/.npm/_logs`

Debug logs written by every npm run; npm recreates the folder.

#### nvm download cache

<span class="risk safe">safe</span> · delete · <code>js-nvm-download-cache</code>

- `~/.nvm/.cache`

Node archives kept by nvm after installing; only needed to reinstall the same version offline.

#### Playwright temp profiles & artifacts

<span class="risk safe">safe</span> · delete · <code>js-playwright-tmp</code>

- `$TMPDIR/playwright-transform-cache-*`
- `$TMPDIR/playwright-artifacts-*`
- `$TMPDIR/playwright_chromiumdev_profile-*`
- `$TMPDIR/playwright_firefoxdev_profile-*`
- `$TMPDIR/playwright_webkitdev_profile-*`

Temporary browser profiles, traces and transform cache left by Playwright runs. _(only items older than 1 days)_

#### pnpm metadata cache

<span class="risk safe">safe</span> · delete · <code>js-pnpm-metadata-cache</code>

- `~/Library/Caches/pnpm`
- `~/.cache/pnpm`

Registry metadata cached by pnpm; re-fetched on the next resolution.

#### Prisma engines cache

<span class="risk safe">safe</span> · delete · <code>js-prisma-engines</code>

- `~/.cache/prisma`

Prisma query/schema engines; downloaded again by the next `prisma generate`.

#### Puppeteer Chrome

<span class="risk moderate">moderate</span> · delete · <code>js-puppeteer-chrome</code>

- `~/.cache/puppeteer/chrome/*`

Older Chrome builds downloaded by Puppeteer (the newest is kept); `npx puppeteer browsers install` brings one back. _(keeps the 1 newest)_

#### Puppeteer Firefox

<span class="risk moderate">moderate</span> · delete · <code>js-puppeteer-firefox</code>

- `~/.cache/puppeteer/firefox/*`

Older Firefox builds downloaded by Puppeteer (the newest is kept); re-downloaded on demand. _(keeps the 1 newest)_

#### Puppeteer chrome-headless-shell

<span class="risk moderate">moderate</span> · delete · <code>js-puppeteer-headless-shell</code>

- `~/.cache/puppeteer/chrome-headless-shell/*`

Older headless shells downloaded by Puppeteer (the newest is kept); re-downloaded on demand. _(keeps the 1 newest)_

#### React Native prebuilt tarballs

<span class="risk safe">safe</span> · delete · <code>js-react-native-prebuilt</code>

- `~/Library/Caches/ReactNative`

React Native core/deps/Hermes prebuilt tarballs; downloaded again by the next `pod install` that needs them.

#### Selenium Manager drivers

<span class="risk safe">safe</span> · delete · <code>js-selenium-drivers</code>

- `~/.cache/selenium`

Browser drivers downloaded by selenium-manager; fetched again when needed.

#### tsx transform cache

<span class="risk safe">safe</span> · delete · <code>js-tsx-cache</code>

- `$TMPDIR/tsx-*`

Transpiled files cached by tsx; rebuilt automatically.

#### Turborepo global cache

<span class="risk safe">safe</span> · delete · <code>js-turbo-cache</code>

- `~/Library/Caches/turbo`
- `~/.cache/turbo`

Global Turborepo cache (project .turbo dirs are listed with project artifacts); tasks re-run and refill it.

#### TypeScript type acquisition cache

<span class="risk safe">safe</span> · delete · <code>js-typescript-ata</code>

- `~/Library/Caches/typescript`
- `~/.cache/typescript`

@types packages fetched by the editor TS server (VS Code, Cursor, Zed); re-acquired on demand.

#### Volta download inventory

<span class="risk safe">safe</span> · delete · <code>js-volta-inventory</code>

- `~/.volta/tools/inventory`

Tarballs downloaded by Volta; installed tools keep working and Volta re-fetches on demand.

#### Yarn classic (v1) cache

<span class="risk safe">safe</span> · delete · <code>js-yarn-classic-cache</code>

- `~/Library/Caches/Yarn/v*`
- `~/.cache/yarn`
- `~/.yarn-cache`

Yarn 1 package cache; re-downloaded by the next `yarn` v1 install (node_modules are real copies and keep working). Plain `yarn cache clean` may target the Berry cache instead when corepack runs Yarn 3/4.

#### Yarn run temp dirs

<span class="risk safe">safe</span> · delete · <code>js-yarn-tmp</code>

- `$TMPDIR/yarn--*`

Shim dirs created by each `yarn run` (v1) and never removed. _(only items older than 1 days)_

## 🧩 IDEs

VS Code, Cursor, Zed, JetBrains caches — 10 entries.

#### Cursor Electron caches

<span class="risk safe">safe</span> · delete · <code>ai-cursor-electron-caches</code>

- `~/Library/Application Support/Cursor/Cache`
- `~/Library/Application Support/Cursor/Code Cache`
- `~/Library/Application Support/Cursor/GPUCache`
- `~/Library/Application Support/Cursor/DawnCache`
- `~/Library/Application Support/Cursor/DawnGraphiteCache`
- `~/Library/Application Support/Cursor/DawnWebGPUCache`
- `~/Library/Application Support/Cursor/GraphiteDawnCache`
- `~/Library/Application Support/Cursor/GrShaderCache`
- `~/Library/Application Support/Cursor/CachedProfilesData`
- `~/Library/Application Support/Cursor/Service Worker/CacheStorage`
- `~/Library/Application Support/Cursor/Service Worker/ScriptCache`
- `~/Library/Application Support/Cursor/Partitions/*/Cache`
- `~/Library/Application Support/Cursor/Partitions/*/Code Cache`
- `~/Library/Application Support/Cursor/Partitions/*/GPUCache`
- `~/Library/Application Support/Cursor/Partitions/*/DawnWebGPUCache`
- `~/Library/Application Support/Cursor/Partitions/*/DawnGraphiteCache`
- `~/Library/Application Support/Cursor/Partitions/*/Shared Dictionary`
- `~/Library/Caches/com.todesktop.230313mzl4w4u92`
- `~/Library/Caches/com.todesktop.230313mzl4w4u92.ShipIt`

Chromium HTTP / code / GPU caches, the embedded browser's caches and the updater cache of Cursor; rebuilt on launch. Cookies, Local Storage and IndexedDB are kept. _(refused while Cursor runs)_

#### Cursor Local History (Timeline) > 180d

<span class="risk caution">caution</span> · delete · <code>ai-cursor-local-history</code>

- `~/Library/Application Support/Cursor/User/History/*`

Per-file edit snapshots behind the Timeline view, untouched for 6 months; they cannot be restored afterwards. _(refused while Cursor runs; only items older than 180 days)_

#### Cursor logs

<span class="risk safe">safe</span> · delete · <code>ai-cursor-logs</code>

- `~/Library/Application Support/Cursor/logs/*`

One log folder per Cursor launch; only useful for bug reports. _(refused while Cursor runs)_

#### Cursor cached extension downloads (VSIX)

<span class="risk safe">safe</span> · delete · <code>ai-cursor-vsix-cache</code>

- `~/Library/Application Support/Cursor/CachedExtensionVSIXs/*`

Downloaded extension packages kept after install; re-downloaded on install or update. _(refused while Cursor runs)_

#### Cursor webview CacheStorage

<span class="risk safe">safe</span> · delete · <code>ai-cursor-webstorage-cache</code>

- `~/Library/Application Support/Cursor/WebStorage/*/CacheStorage`

Service-worker caches of Cursor webviews; repopulated on demand. _(refused while Cursor runs)_

#### Windsurf editor caches

<span class="risk safe">safe</span> · delete · <code>ai-windsurf-caches</code>

- `~/Library/Application Support/Windsurf/Cache`
- `~/Library/Application Support/Windsurf/Code Cache`
- `~/Library/Application Support/Windsurf/GPUCache`
- `~/Library/Application Support/Windsurf/CachedData`
- `~/Library/Application Support/Windsurf/CachedExtensionVSIXs`
- `~/Library/Application Support/Windsurf/logs`

Electron caches, old-build code caches, VSIX downloads and logs of Windsurf; rebuilt on launch. _(refused while Windsurf, /Windsurf.app/ runs)_

#### Sublime Merge cache

<span class="risk safe">safe</span> · delete · <code>ide-sublime-merge-cache</code>

- `~/Library/Caches/com.sublimemerge`

Sublime Merge cache; rebuilt at launch. _(refused while sublime_merge runs)_

#### Sublime Text caches & index

<span class="risk safe">safe</span> · delete · <code>ide-sublime-text-cache</code>

- `~/Library/Caches/com.sublimetext.4`
- `~/Library/Caches/com.sublimetext.3`
- `~/Library/Application Support/Sublime Text/Cache`
- `~/Library/Application Support/Sublime Text/Index`
- `~/Library/Application Support/Sublime Text 3/Cache`
- `~/Library/Application Support/Sublime Text 3/Index`

Sublime Text caches and symbol index (settings and packages are kept); rebuilt at launch. _(refused while sublime_text runs)_

#### VS Code C/C++ IntelliSense cache (ipch)

<span class="risk safe">safe</span> · delete · <code>ide-vscode-cpptools-cache</code>

- `~/Library/Caches/vscode-cpptools`

Precompiled headers of the Microsoft C/C++ extension; rebuilt when a C/C++ file is opened.

#### VS Code ripgrep downloads

<span class="risk safe">safe</span> · delete · <code>ide-vscode-ripgrep-cache</code>

- `~/.cache/vscode-ripgrep`

ripgrep binaries downloaded by @vscode/ripgrep installs; downloaded again by the next install that needs them.

## 🐳 Containers

Docker, colima, OrbStack — 5 entries.

#### Docker buildx local cache

<span class="risk safe">safe</span> · delete · <code>containers-docker-buildx-cache</code>

- `~/.docker/buildx/cache`

Local build cache exported by docker buildx (registry credentials and contexts in ~/.docker are kept); rebuilt by the next build.

#### Docker Desktop logs

<span class="risk safe">safe</span> · delete · <code>containers-docker-desktop-logs</code>

- `~/Library/Containers/com.docker.docker/Data/log`

Docker Desktop VM and backend logs; rewritten while Docker Desktop runs. _(refused while Docker Desktop, com.docker.backend runs)_

#### Rancher Desktop cache

<span class="risk safe">safe</span> · delete · <code>containers-rancher-desktop-cache</code>

- `~/Library/Caches/rancher-desktop`

Downloads cached by Rancher Desktop (k3s, images); downloaded again when needed. _(refused while Rancher Desktop runs)_

#### Tart / Vagrant download caches

<span class="risk safe">safe</span> · delete · <code>containers-tart-vagrant-cache</code>

- `~/.tart/cache`
- `~/.vagrant.d/tmp`

Images and boxes being downloaded or cached by Tart and Vagrant; downloaded again when a VM is created.

#### Lima / colima downloaded VM images

<span class="risk safe">safe</span> · delete · <code>containers-vm-image-cache</code>

- `~/Library/Caches/lima`
- `~/Library/Caches/colima`
- `~/.lima/_cache`

Linux images downloaded to create Lima / colima VMs (existing VMs keep their own disks); downloaded again at the next VM creation.

## 🧰 Other toolchains

Homebrew, Go, Rust, Python, Ruby — 16 entries.

#### Cargo registry index & git databases

<span class="risk moderate">moderate</span> · delete · <code>langs-cargo-index</code>

- `~/.cargo/registry/index`
- `~/.cargo/git/db`

crates.io index cache and bare git repositories of git dependencies; fetched again by the next build. _(refused while cargo runs)_

#### Cargo downloaded crates

<span class="risk safe">safe</span> · delete · <code>langs-cargo-registry-cache</code>

- `~/.cargo/registry/cache`

Compressed .crate files downloaded by cargo; downloaded again by the next build that needs them. _(refused while cargo runs)_

#### Cargo extracted crate sources & git checkouts

<span class="risk moderate">moderate</span> · delete · <code>langs-cargo-sources</code>

- `~/.cargo/registry/src`
- `~/.cargo/git/checkouts`

Sources of dependencies extracted by cargo (read by rust-analyzer); re-extracted or downloaded again by the next build. _(refused while cargo, rust-analyzer runs)_

#### conda package cache

<span class="risk moderate">moderate</span> · runs `conda clean --all --yes` · <code>langs-conda-pkgs</code>

- `~/miniconda3/pkgs`
- `~/anaconda3/pkgs`
- `~/miniforge3/pkgs`
- `~/mambaforge/pkgs`
- `~/.conda/pkgs`
- `~/opt/miniconda3/pkgs`
- `~/opt/anaconda3/pkgs`

Package tarballs and extracted packages cached by conda; `conda clean --all` keeps what environments need.

#### Dev CLI caches (gh, pre-commit)

<span class="risk moderate">moderate</span> · delete · <code>langs-dev-cli-caches</code>

- `~/.cache/gh`
- `~/.cache/pre-commit`

GitHub CLI API cache and pre-commit hook environments; recreated by the next command (pre-commit reinstalls its hooks).

#### Ivy / sbt caches

<span class="risk moderate">moderate</span> · delete · <code>langs-ivy-sbt-cache</code>

- `~/.ivy2/cache`
- `~/.sbt/boot`
- `~/.cache/coursier`

JVM dependencies cached by Ivy, sbt and Coursier; downloaded again by the next build.

#### NuGet HTTP & plugin caches

<span class="risk safe">safe</span> · delete · <code>langs-nuget-http-cache</code>

- `~/.local/share/NuGet/http-cache`
- `~/.local/share/NuGet/plugins-cache`
- `~/.local/share/NuGet/v3-cache`

HTTP responses and plugins cached by NuGet / dotnet; downloaded again at the next restore.

#### NuGet global packages

<span class="risk moderate">moderate</span> · delete · <code>langs-nuget-packages</code>

- `~/.nuget/packages`

Packages restored by dotnet / NuGet for all projects; restored again (downloaded) by the next build.

#### pip download cache

<span class="risk safe">safe</span> · delete · <code>langs-pip-cache</code>

- `~/Library/Caches/pip`
- `~/.cache/pip`

Wheels and HTTP responses cached by pip; downloaded again at the next install.

#### pipenv / pyenv download caches

<span class="risk safe">safe</span> · delete · <code>langs-pipenv-pyenv-cache</code>

- `~/Library/Caches/pipenv`
- `~/.pyenv/cache`

Downloads cached by pipenv and pyenv (installed Pythons are kept); downloaded again when needed.

#### Poetry package cache

<span class="risk safe">safe</span> · delete · <code>langs-poetry-cache</code>

- `~/Library/Caches/pypoetry/cache`
- `~/Library/Caches/pypoetry/artifacts`

Package metadata and archives cached by Poetry (its virtualenvs are never touched); downloaded again at the next install.

#### Python user packages (pip --user)

<span class="risk caution">caution</span> · report · <code>langs-python-user-site</code>

- `~/Library/Python/*`

Packages installed with `pip install --user` for this Python version (often forgotten); list them with `python3 -m pip list --user` before uninstalling.

#### RubyGems / Bundler download caches

<span class="risk safe">safe</span> · delete · <code>langs-ruby-gem-caches</code>

- `~/.rbenv/versions/*/lib/ruby/gems/*/cache`
- `~/.gem/ruby/*/cache`
- `~/.gem/specs`
- `~/.bundle/cache`
- `~/.rbenv/cache`

Downloaded .gem archives, gem specs and Ruby source tarballs kept after installation (installed gems keep working); downloaded again when needed.

#### rustup downloads & temp files

<span class="risk safe">safe</span> · delete · <code>langs-rustup-downloads</code>

- `~/.rustup/downloads`
- `~/.rustup/tmp`

Component archives downloaded by rustup and leftovers of interrupted installs (toolchains are kept).

#### uv cache

<span class="risk safe">safe</span> · delete · <code>langs-uv-cache</code>

- `~/.cache/uv`
- `~/Library/Caches/uv`

Packages and Python builds cached by uv (environments are copies, they keep working); downloaded again when needed. _(refused while uv runs)_

#### Zig / Bazel global caches

<span class="risk moderate">moderate</span> · delete · <code>langs-zig-bazel-cache</code>

- `~/.cache/zig`
- `~/.cache/bazel`
- `~/Library/Caches/bazel`

Global build caches of Zig and Bazel; rebuilt by the next build (can take long for big Bazel workspaces).

## 💻 System

Library caches & logs, Trash, browsers, downloads — 59 entries.

#### Discord caches

<span class="risk safe">safe</span> · delete · <code>app-discord-cache</code>

- `~/Library/Application Support/discord/Cache`
- `~/Library/Application Support/discord/Code Cache`
- `~/Library/Application Support/discord/GPUCache`
- `~/Library/Application Support/discord/DawnCache`
- `~/Library/Application Support/discord/DawnGraphiteCache`
- `~/Library/Application Support/discord/DawnWebGPUCache`
- `~/Library/Application Support/discord/GrShaderCache`
- `~/Library/Application Support/discord/ShaderCache`
- `~/Library/Application Support/discord/GraphiteDawnCache`
- `~/Library/Application Support/discord/Crashpad/completed`

Discord web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Discord runs)_

#### Discord offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-discord-service-worker-cache</code>

- `~/Library/Application Support/discord/Service Worker/CacheStorage`

Discord offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Discord runs)_

#### Figma caches

<span class="risk safe">safe</span> · delete · <code>app-figma-cache</code>

- `~/Library/Application Support/Figma/DesktopProfile/*/Cache`
- `~/Library/Application Support/Figma/DesktopProfile/*/Code Cache`
- `~/Library/Application Support/Figma/DesktopProfile/*/GPUCache`
- `~/Library/Application Support/Figma/DesktopProfile/*/DawnCache`
- `~/Library/Application Support/Figma/DesktopProfile/*/DawnGraphiteCache`
- `~/Library/Application Support/Figma/DesktopProfile/*/DawnWebGPUCache`
- `~/Library/Application Support/Figma/DesktopProfile/*/GrShaderCache`
- `~/Library/Application Support/Figma/DesktopProfile/*/ShaderCache`
- `~/Library/Application Support/Figma/DesktopProfile/*/GraphiteDawnCache`
- `~/Library/Application Support/Figma/DesktopProfile/*/Crashpad/completed`

Figma web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Figma runs)_

#### Figma offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-figma-service-worker-cache</code>

- `~/Library/Application Support/Figma/DesktopProfile/*/Service Worker/CacheStorage`

Figma offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Figma runs)_

#### GitHub Desktop caches

<span class="risk safe">safe</span> · delete · <code>app-github-desktop-cache</code>

- `~/Library/Application Support/GitHub Desktop/Cache`
- `~/Library/Application Support/GitHub Desktop/Code Cache`
- `~/Library/Application Support/GitHub Desktop/GPUCache`
- `~/Library/Application Support/GitHub Desktop/DawnCache`
- `~/Library/Application Support/GitHub Desktop/DawnGraphiteCache`
- `~/Library/Application Support/GitHub Desktop/DawnWebGPUCache`
- `~/Library/Application Support/GitHub Desktop/GrShaderCache`
- `~/Library/Application Support/GitHub Desktop/ShaderCache`
- `~/Library/Application Support/GitHub Desktop/GraphiteDawnCache`
- `~/Library/Application Support/GitHub Desktop/Crashpad/completed`

GitHub Desktop web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while GitHub Desktop runs)_

#### GitHub Desktop offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-github-desktop-service-worker-cache</code>

- `~/Library/Application Support/GitHub Desktop/Service Worker/CacheStorage`

GitHub Desktop offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while GitHub Desktop runs)_

#### Linear caches

<span class="risk safe">safe</span> · delete · <code>app-linear-cache</code>

- `~/Library/Application Support/Linear/Cache`
- `~/Library/Application Support/Linear/Code Cache`
- `~/Library/Application Support/Linear/GPUCache`
- `~/Library/Application Support/Linear/DawnCache`
- `~/Library/Application Support/Linear/DawnGraphiteCache`
- `~/Library/Application Support/Linear/DawnWebGPUCache`
- `~/Library/Application Support/Linear/GrShaderCache`
- `~/Library/Application Support/Linear/ShaderCache`
- `~/Library/Application Support/Linear/GraphiteDawnCache`
- `~/Library/Application Support/Linear/Crashpad/completed`

Linear web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Linear runs)_

#### Linear offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-linear-service-worker-cache</code>

- `~/Library/Application Support/Linear/Service Worker/CacheStorage`

Linear offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Linear runs)_

#### Notion caches

<span class="risk safe">safe</span> · delete · <code>app-notion-cache</code>

- `~/Library/Application Support/Notion/Cache`
- `~/Library/Application Support/Notion/Code Cache`
- `~/Library/Application Support/Notion/GPUCache`
- `~/Library/Application Support/Notion/DawnCache`
- `~/Library/Application Support/Notion/DawnGraphiteCache`
- `~/Library/Application Support/Notion/DawnWebGPUCache`
- `~/Library/Application Support/Notion/GrShaderCache`
- `~/Library/Application Support/Notion/ShaderCache`
- `~/Library/Application Support/Notion/GraphiteDawnCache`
- `~/Library/Application Support/Notion/Crashpad/completed`

Notion web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Notion runs)_

#### Notion Calendar caches

<span class="risk safe">safe</span> · delete · <code>app-notion-calendar-cache</code>

- `~/Library/Application Support/Notion Calendar/Cache`
- `~/Library/Application Support/Notion Calendar/Code Cache`
- `~/Library/Application Support/Notion Calendar/GPUCache`
- `~/Library/Application Support/Notion Calendar/DawnCache`
- `~/Library/Application Support/Notion Calendar/DawnGraphiteCache`
- `~/Library/Application Support/Notion Calendar/DawnWebGPUCache`
- `~/Library/Application Support/Notion Calendar/GrShaderCache`
- `~/Library/Application Support/Notion Calendar/ShaderCache`
- `~/Library/Application Support/Notion Calendar/GraphiteDawnCache`
- `~/Library/Application Support/Notion Calendar/Crashpad/completed`

Notion Calendar web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Notion Calendar runs)_

#### Notion Calendar offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-notion-calendar-service-worker-cache</code>

- `~/Library/Application Support/Notion Calendar/Service Worker/CacheStorage`

Notion Calendar offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Notion Calendar runs)_

#### Notion offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-notion-service-worker-cache</code>

- `~/Library/Application Support/Notion/Service Worker/CacheStorage`

Notion offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Notion runs)_

#### Obsidian caches

<span class="risk safe">safe</span> · delete · <code>app-obsidian-cache</code>

- `~/Library/Application Support/obsidian/Cache`
- `~/Library/Application Support/obsidian/Code Cache`
- `~/Library/Application Support/obsidian/GPUCache`
- `~/Library/Application Support/obsidian/DawnCache`
- `~/Library/Application Support/obsidian/DawnGraphiteCache`
- `~/Library/Application Support/obsidian/DawnWebGPUCache`
- `~/Library/Application Support/obsidian/GrShaderCache`
- `~/Library/Application Support/obsidian/ShaderCache`
- `~/Library/Application Support/obsidian/GraphiteDawnCache`
- `~/Library/Application Support/obsidian/Crashpad/completed`

Obsidian web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Obsidian runs)_

#### Obsidian offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-obsidian-service-worker-cache</code>

- `~/Library/Application Support/obsidian/Service Worker/CacheStorage`

Obsidian offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Obsidian runs)_

#### Postman caches

<span class="risk safe">safe</span> · delete · <code>app-postman-cache</code>

- `~/Library/Application Support/Postman/Cache`
- `~/Library/Application Support/Postman/Code Cache`
- `~/Library/Application Support/Postman/GPUCache`
- `~/Library/Application Support/Postman/DawnCache`
- `~/Library/Application Support/Postman/DawnGraphiteCache`
- `~/Library/Application Support/Postman/DawnWebGPUCache`
- `~/Library/Application Support/Postman/GrShaderCache`
- `~/Library/Application Support/Postman/ShaderCache`
- `~/Library/Application Support/Postman/GraphiteDawnCache`
- `~/Library/Application Support/Postman/Crashpad/completed`

Postman web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Postman runs)_

#### Postman offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-postman-service-worker-cache</code>

- `~/Library/Application Support/Postman/Service Worker/CacheStorage`

Postman offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Postman runs)_

#### Slack caches

<span class="risk safe">safe</span> · delete · <code>app-slack-cache</code>

- `~/Library/Application Support/Slack/Cache`
- `~/Library/Application Support/Slack/Code Cache`
- `~/Library/Application Support/Slack/GPUCache`
- `~/Library/Application Support/Slack/DawnCache`
- `~/Library/Application Support/Slack/DawnGraphiteCache`
- `~/Library/Application Support/Slack/DawnWebGPUCache`
- `~/Library/Application Support/Slack/GrShaderCache`
- `~/Library/Application Support/Slack/ShaderCache`
- `~/Library/Application Support/Slack/GraphiteDawnCache`
- `~/Library/Application Support/Slack/Crashpad/completed`

Slack web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while Slack runs)_

#### Slack offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-slack-service-worker-cache</code>

- `~/Library/Application Support/Slack/Service Worker/CacheStorage`

Slack offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while Slack runs)_

#### Microsoft Teams caches

<span class="risk safe">safe</span> · delete · <code>app-teams-cache</code>

- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/Cache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/Code Cache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/GPUCache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/DawnCache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/DawnGraphiteCache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/DawnWebGPUCache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/GrShaderCache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/ShaderCache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/GraphiteDawnCache`
- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/Crashpad/completed`

Microsoft Teams web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept. _(refused while MSTeams, Microsoft Teams runs)_

#### Microsoft Teams offline web cache (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>app-teams-service-worker-cache</code>

- `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*/Service Worker/CacheStorage`

Microsoft Teams offline copy of its web client; downloaded again at the next launch (slower first start). _(refused while MSTeams, Microsoft Teams runs)_

#### Arc caches

<span class="risk safe">safe</span> · delete · <code>browser-arc-cache</code>

- `~/Library/Caches/Arc/User Data/*/Cache`
- `~/Library/Caches/Arc/User Data/*/Code Cache`
- `~/Library/Application Support/Arc/User Data/*/GPUCache`
- `~/Library/Application Support/Arc/User Data/*/DawnCache`
- `~/Library/Application Support/Arc/User Data/*/DawnGraphiteCache`
- `~/Library/Application Support/Arc/User Data/*/DawnWebGPUCache`
- `~/Library/Application Support/Arc/User Data/GrShaderCache`
- `~/Library/Application Support/Arc/User Data/ShaderCache`
- `~/Library/Application Support/Arc/User Data/GraphiteDawnCache`
- `~/Library/Application Support/Arc/User Data/GPUPersistentCache`
- `~/Library/Application Support/Arc/User Data/component_crx_cache`
- `~/Library/Application Support/Arc/User Data/extensions_crx_cache`

Arc HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Arc runs)_

#### Arc on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-arc-models</code>

- `~/Library/Application Support/Arc/User Data/OptGuideOnDeviceModel`
- `~/Library/Application Support/Arc/User Data/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/Arc/User Data/optimization_guide_model_store`
- `~/Library/Application Support/Arc/User Data/screen_ai`

Machine-learning models Arc downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Arc runs)_

#### Arc site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-arc-service-worker-cache</code>

- `~/Library/Application Support/Arc/User Data/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Arc); sites download them again, installed web apps lose offline data. _(refused while Arc runs)_

#### Brave caches

<span class="risk safe">safe</span> · delete · <code>browser-brave-cache</code>

- `~/Library/Caches/BraveSoftware/Brave-Browser/*/Cache`
- `~/Library/Caches/BraveSoftware/Brave-Browser/*/Code Cache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/*/GPUCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/*/DawnCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/*/DawnGraphiteCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/*/DawnWebGPUCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/GrShaderCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/ShaderCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/GraphiteDawnCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/GPUPersistentCache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/component_crx_cache`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/extensions_crx_cache`

Brave HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Brave Browser runs)_

#### Brave on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-brave-models</code>

- `~/Library/Application Support/BraveSoftware/Brave-Browser/OptGuideOnDeviceModel`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/optimization_guide_model_store`
- `~/Library/Application Support/BraveSoftware/Brave-Browser/screen_ai`

Machine-learning models Brave downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Brave Browser runs)_

#### Brave site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-brave-service-worker-cache</code>

- `~/Library/Application Support/BraveSoftware/Brave-Browser/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Brave); sites download them again, installed web apps lose offline data. _(refused while Brave Browser runs)_

#### Google Chrome caches

<span class="risk safe">safe</span> · delete · <code>browser-chrome-cache</code>

- `~/Library/Caches/Google/Chrome/*/Cache`
- `~/Library/Caches/Google/Chrome/*/Code Cache`
- `~/Library/Application Support/Google/Chrome/*/GPUCache`
- `~/Library/Application Support/Google/Chrome/*/DawnCache`
- `~/Library/Application Support/Google/Chrome/*/DawnGraphiteCache`
- `~/Library/Application Support/Google/Chrome/*/DawnWebGPUCache`
- `~/Library/Application Support/Google/Chrome/GrShaderCache`
- `~/Library/Application Support/Google/Chrome/ShaderCache`
- `~/Library/Application Support/Google/Chrome/GraphiteDawnCache`
- `~/Library/Application Support/Google/Chrome/GPUPersistentCache`
- `~/Library/Application Support/Google/Chrome/component_crx_cache`
- `~/Library/Application Support/Google/Chrome/extensions_crx_cache`

Google Chrome HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Google Chrome runs)_

#### Google Chrome on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-chrome-models</code>

- `~/Library/Application Support/Google/Chrome/OptGuideOnDeviceModel`
- `~/Library/Application Support/Google/Chrome/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/Google/Chrome/optimization_guide_model_store`
- `~/Library/Application Support/Google/Chrome/screen_ai`

Machine-learning models Google Chrome downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Google Chrome runs)_

#### Google Chrome site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-chrome-service-worker-cache</code>

- `~/Library/Application Support/Google/Chrome/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Google Chrome); sites download them again, installed web apps lose offline data. _(refused while Google Chrome runs)_

#### Chromium caches

<span class="risk safe">safe</span> · delete · <code>browser-chromium-cache</code>

- `~/Library/Caches/Chromium/*/Cache`
- `~/Library/Caches/Chromium/*/Code Cache`
- `~/Library/Application Support/Chromium/*/GPUCache`
- `~/Library/Application Support/Chromium/*/DawnCache`
- `~/Library/Application Support/Chromium/*/DawnGraphiteCache`
- `~/Library/Application Support/Chromium/*/DawnWebGPUCache`
- `~/Library/Application Support/Chromium/GrShaderCache`
- `~/Library/Application Support/Chromium/ShaderCache`
- `~/Library/Application Support/Chromium/GraphiteDawnCache`
- `~/Library/Application Support/Chromium/GPUPersistentCache`
- `~/Library/Application Support/Chromium/component_crx_cache`
- `~/Library/Application Support/Chromium/extensions_crx_cache`

Chromium HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Chromium runs)_

#### Chromium on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-chromium-models</code>

- `~/Library/Application Support/Chromium/OptGuideOnDeviceModel`
- `~/Library/Application Support/Chromium/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/Chromium/optimization_guide_model_store`
- `~/Library/Application Support/Chromium/screen_ai`

Machine-learning models Chromium downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Chromium runs)_

#### Chromium site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-chromium-service-worker-cache</code>

- `~/Library/Application Support/Chromium/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Chromium); sites download them again, installed web apps lose offline data. _(refused while Chromium runs)_

#### Comet caches

<span class="risk safe">safe</span> · delete · <code>browser-comet-cache</code>

- `~/Library/Caches/Comet/*/Cache`
- `~/Library/Caches/Comet/*/Code Cache`
- `~/Library/Application Support/Comet/*/GPUCache`
- `~/Library/Application Support/Comet/*/DawnCache`
- `~/Library/Application Support/Comet/*/DawnGraphiteCache`
- `~/Library/Application Support/Comet/*/DawnWebGPUCache`
- `~/Library/Application Support/Comet/GrShaderCache`
- `~/Library/Application Support/Comet/ShaderCache`
- `~/Library/Application Support/Comet/GraphiteDawnCache`
- `~/Library/Application Support/Comet/GPUPersistentCache`
- `~/Library/Application Support/Comet/component_crx_cache`
- `~/Library/Application Support/Comet/extensions_crx_cache`

Comet HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Comet runs)_

#### Comet on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-comet-models</code>

- `~/Library/Application Support/Comet/OptGuideOnDeviceModel`
- `~/Library/Application Support/Comet/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/Comet/optimization_guide_model_store`
- `~/Library/Application Support/Comet/screen_ai`

Machine-learning models Comet downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Comet runs)_

#### Comet site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-comet-service-worker-cache</code>

- `~/Library/Application Support/Comet/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Comet); sites download them again, installed web apps lose offline data. _(refused while Comet runs)_

#### Dia caches

<span class="risk safe">safe</span> · delete · <code>browser-dia-cache</code>

- `~/Library/Caches/Dia/User Data/*/Cache`
- `~/Library/Caches/Dia/User Data/*/Code Cache`
- `~/Library/Application Support/Dia/User Data/*/GPUCache`
- `~/Library/Application Support/Dia/User Data/*/DawnCache`
- `~/Library/Application Support/Dia/User Data/*/DawnGraphiteCache`
- `~/Library/Application Support/Dia/User Data/*/DawnWebGPUCache`
- `~/Library/Application Support/Dia/User Data/GrShaderCache`
- `~/Library/Application Support/Dia/User Data/ShaderCache`
- `~/Library/Application Support/Dia/User Data/GraphiteDawnCache`
- `~/Library/Application Support/Dia/User Data/GPUPersistentCache`
- `~/Library/Application Support/Dia/User Data/component_crx_cache`
- `~/Library/Application Support/Dia/User Data/extensions_crx_cache`

Dia HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Dia runs)_

#### Dia on-device models

<span class="risk moderate">moderate</span> · delete · <code>browser-dia-model-cache</code>

- `~/Library/Caches/company.thebrowser.dia/ModelFileCache`

Classification models Dia downloads for its assistant; downloaded again when needed. _(refused while Dia runs)_

#### Dia on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-dia-models</code>

- `~/Library/Application Support/Dia/User Data/OptGuideOnDeviceModel`
- `~/Library/Application Support/Dia/User Data/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/Dia/User Data/optimization_guide_model_store`
- `~/Library/Application Support/Dia/User Data/screen_ai`

Machine-learning models Dia downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Dia runs)_

#### Dia site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-dia-service-worker-cache</code>

- `~/Library/Application Support/Dia/User Data/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Dia); sites download them again, installed web apps lose offline data. _(refused while Dia runs)_

#### Microsoft Edge caches

<span class="risk safe">safe</span> · delete · <code>browser-edge-cache</code>

- `~/Library/Caches/Microsoft Edge/*/Cache`
- `~/Library/Caches/Microsoft Edge/*/Code Cache`
- `~/Library/Application Support/Microsoft Edge/*/GPUCache`
- `~/Library/Application Support/Microsoft Edge/*/DawnCache`
- `~/Library/Application Support/Microsoft Edge/*/DawnGraphiteCache`
- `~/Library/Application Support/Microsoft Edge/*/DawnWebGPUCache`
- `~/Library/Application Support/Microsoft Edge/GrShaderCache`
- `~/Library/Application Support/Microsoft Edge/ShaderCache`
- `~/Library/Application Support/Microsoft Edge/GraphiteDawnCache`
- `~/Library/Application Support/Microsoft Edge/GPUPersistentCache`
- `~/Library/Application Support/Microsoft Edge/component_crx_cache`
- `~/Library/Application Support/Microsoft Edge/extensions_crx_cache`

Microsoft Edge HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Microsoft Edge runs)_

#### Microsoft Edge on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-edge-models</code>

- `~/Library/Application Support/Microsoft Edge/OptGuideOnDeviceModel`
- `~/Library/Application Support/Microsoft Edge/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/Microsoft Edge/optimization_guide_model_store`
- `~/Library/Application Support/Microsoft Edge/screen_ai`

Machine-learning models Microsoft Edge downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Microsoft Edge runs)_

#### Microsoft Edge site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-edge-service-worker-cache</code>

- `~/Library/Application Support/Microsoft Edge/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Microsoft Edge); sites download them again, installed web apps lose offline data. _(refused while Microsoft Edge runs)_

#### Firefox caches

<span class="risk safe">safe</span> · delete · <code>browser-firefox-cache</code>

- `~/Library/Caches/Firefox/Profiles/*/cache2`
- `~/Library/Caches/Firefox/Profiles/*/startupCache`
- `~/Library/Caches/Firefox/Profiles/*/thumbnails`

Firefox HTTP cache, startup cache and thumbnails (the profile in Application Support is never touched); rebuilt while browsing. _(refused while firefox runs)_

#### Google Updater downloads

<span class="risk safe">safe</span> · delete · <code>browser-google-updater-cache</code>

- `~/Library/Application Support/Google/GoogleUpdater/crx_cache`
- `~/Library/Application Support/Google/GoogleUpdater/*.old`

Update packages and previous versions kept by Google's updater (Chrome); downloaded again when an update is pending.

#### Vivaldi caches

<span class="risk safe">safe</span> · delete · <code>browser-vivaldi-cache</code>

- `~/Library/Caches/Vivaldi/*/Cache`
- `~/Library/Caches/Vivaldi/*/Code Cache`
- `~/Library/Application Support/Vivaldi/*/GPUCache`
- `~/Library/Application Support/Vivaldi/*/DawnCache`
- `~/Library/Application Support/Vivaldi/*/DawnGraphiteCache`
- `~/Library/Application Support/Vivaldi/*/DawnWebGPUCache`
- `~/Library/Application Support/Vivaldi/GrShaderCache`
- `~/Library/Application Support/Vivaldi/ShaderCache`
- `~/Library/Application Support/Vivaldi/GraphiteDawnCache`
- `~/Library/Application Support/Vivaldi/GPUPersistentCache`
- `~/Library/Application Support/Vivaldi/component_crx_cache`
- `~/Library/Application Support/Vivaldi/extensions_crx_cache`

Vivaldi HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing. _(refused while Vivaldi runs)_

#### Vivaldi on-device AI models

<span class="risk moderate">moderate</span> · delete · <code>browser-vivaldi-models</code>

- `~/Library/Application Support/Vivaldi/OptGuideOnDeviceModel`
- `~/Library/Application Support/Vivaldi/OptGuideOnDeviceClassifierModel`
- `~/Library/Application Support/Vivaldi/optimization_guide_model_store`
- `~/Library/Application Support/Vivaldi/screen_ai`

Machine-learning models Vivaldi downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them. _(refused while Vivaldi runs)_

#### Vivaldi site offline caches (Service Worker)

<span class="risk moderate">moderate</span> · delete · <code>browser-vivaldi-service-worker-cache</code>

- `~/Library/Application Support/Vivaldi/*/Service Worker/CacheStorage`

Offline copies of web apps kept by their service workers (Vivaldi); sites download them again, installed web apps lose offline data. _(refused while Vivaldi runs)_

#### Zen browser caches

<span class="risk safe">safe</span> · delete · <code>browser-zen-cache</code>

- `~/Library/Caches/zen/Profiles/*/cache2`
- `~/Library/Caches/zen/Profiles/*/startupCache`

Zen browser HTTP and startup caches (profiles are kept); rebuilt while browsing. _(refused while zen runs)_

#### fontconfig cache

<span class="risk safe">safe</span> · delete · <code>system-fontconfig-cache</code>

- `~/.cache/fontconfig`

Font index built by fontconfig (Homebrew tools, LibreOffice, ImageMagick...); rebuilt in seconds.

#### Apple Maps tile cache (GeoServices)

<span class="risk safe">safe</span> · delete · <code>system-geoservices-cache</code>

- `~/Library/Caches/GeoServices`

Map tiles cached by macOS location services and Maps; downloaded again when a map is displayed.

#### Image & crash-reporter caches (Kingfisher, Sentry)

<span class="risk safe">safe</span> · delete · <code>system-image-caches</code>

- `~/Library/Caches/com.onevcat.Kingfisher.ImageCache.*`
- `~/Library/Caches/io.sentry`
- `~/Library/Caches/SentryCrash`

Image caches of apps using Kingfisher and crash reports already sent by the Sentry SDK; rebuilt by the apps.

#### LanguageTool server download (language_tool_python)

<span class="risk moderate">moderate</span> · delete · <code>system-languagetool-cache</code>

- `~/.cache/language_tool_python`

LanguageTool server downloaded by the language_tool_python package; downloaded again (~400 MB) on its next use.

#### Microsoft AutoUpdate downloads

<span class="risk safe">safe</span> · delete · <code>system-microsoft-autoupdate-cache</code>

- `~/Library/Caches/com.microsoft.autoupdate.fba`
- `~/Library/Caches/com.microsoft.autoupdate2`

Update packages downloaded by Microsoft AutoUpdate; downloaded again when an update is pending.

#### mole analyzer cache

<span class="risk safe">safe</span> · delete · <code>system-mole-cache</code>

- `~/.cache/mole`

Scan cache of the mole cleaner (its settings in ~/.config/mole are kept); rebuilt by its next analysis.

#### Apple Music / TV artwork cache

<span class="risk moderate">moderate</span> · delete · <code>system-music-artwork-cache</code>

- `~/Library/Containers/com.apple.AMPArtworkAgent/Data/Documents/artwork`

Album and show artwork cached by the Music and TV apps; downloaded again while browsing the library. _(refused while Music, TV runs)_

#### Proton Mail Bridge logs

<span class="risk safe">safe</span> · delete · <code>system-proton-bridge-logs</code>

- `~/Library/Application Support/protonmail/bridge-v3/logs`

Proton Mail Bridge log files (its local mail store and credentials are never touched); rewritten by the bridge. _(refused while bridge, Proton Mail Bridge runs)_

#### Spotify streaming cache

<span class="risk safe">safe</span> · delete · <code>system-spotify-cache</code>

- `~/Library/Caches/com.spotify.client`

Streaming cache of the Spotify app (downloaded playlists live elsewhere and are kept); refilled while listening. _(refused while Spotify runs)_

#### App log files (~/Library/Logs)

<span class="risk safe">safe</span> · delete · <code>system-user-log-files</code>

- `~/Library/Logs/*.log`
- `~/Library/Logs/*.log.*`

Loose log files in ~/Library/Logs not written for a week; recreated by the app. _(only items older than 7 days)_

#### App logs & crash reports (~/Library/Logs)

<span class="risk safe">safe</span> · delete · <code>system-user-logs</code>

- `~/Library/Logs/*`

Log folders and crash reports untouched for a week; apps create new ones as needed (running apps keep writing to their open file). _(only items older than 7 days)_

