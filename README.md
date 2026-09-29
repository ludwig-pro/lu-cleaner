<p align="center">
  <img src="site/src/assets/logo.svg" width="96" alt="lu-cleaner logo">
</p>

<h1 align="center">lu-cleaner</h1>

<p align="center">
  <b>Free disk space on a macOS developer machine.</b><br>
  AI agent worktrees · node_modules · Pods · iOS/Android builds · simulators · emulators · package-manager caches · AI tools data
</p>

<p align="center">
  <a href="https://ludwig-pro.github.io/lu-cleaner/">Documentation</a> ·
  <a href="https://ludwig-pro.github.io/lu-cleaner/getting-started/installation/">Install</a> ·
  <a href="https://ludwig-pro.github.io/lu-cleaner/fr/">Français</a> ·
  <a href="https://ludwig-pro.github.io/lu-cleaner/llms.txt">llms.txt</a>
</p>

<p align="center">
  <img src="https://ludwig-pro.github.io/lu-cleaner/demos/dashboard.gif" width="900" alt="lu-cleaner dashboard demo: the scan streams in and nothing is selected, AI agent worktrees with their status and details, the recommended project artifacts smart-selected with the a key and one more picked by hand with space, confirmation, cleaning and the freed-space summary">
  <br>
  <sub>Recorded on a synthetic home folder (<a href="demo/">demo/</a>), hence sizes in MB. More: <a href="https://ludwig-pro.github.io/lu-cleaner/demos/worktrees.gif">worktrees</a> · <a href="https://ludwig-pro.github.io/lu-cleaner/demos/scan.gif">scan &amp; dry-run</a></sub>
</p>

---

AI coding agents changed what fills a developer's disk. Every Codex, Cursor, Conductor or Claude Code task can spawn a **git worktree** with its own `node_modules`, `ios/Pods` and Android build; simulators silently collect gigabytes of UI-test recordings; package managers keep several global caches and node versions. General-purpose cleaners (CleanMyMac, mole, npkill…) either don't know these places or deliberately stay away from them.

**lu-cleaner** knows them — and knows which ones are safe to remove.

```
🧹 lu-cleaner  Macintosh HD ███████████████████░ 97% · 16 GB free   ✓ scan done in 39s · 560 items · 238 GB reclaimable

  CATEGORY                      SIZE   ITEMS   SELECTED
▸ 🌳 Worktrees                 45.3 GB      75
  📦 Project artifacts         63.2 GB     228
  📱 iOS Simulators            70.0 GB      23
  🧠 AI tools                  42.2 GB     139
  🟨 JS toolchain              33.3 GB      76
  …
Nothing is selected — space picks an item, a picks the recommended ones, d cleans the selection
nothing selected · delete                      space select · a smart · / filter · d clean · ? help
```

## Features

- **AI agent worktrees** — finds every linked worktree (Codex, Cursor, Conductor, Claude Code, manual) with its branch and status (clean, dirty, unpushed, merged, orphaned) and removes finished ones with `git worktree remove`. Branches are always kept; uncommitted work and commits on no branch block removal.
- **React Native aware artifacts** — `node_modules`, `ios/Pods`, `ios/build`, `android/app/build`, `.gradle`, `.cxx`, `.expo`, `.next`, `dist`… validated with marker files and git-ignore checks, so a `build/` folder that is source code is never touched. Hardlink-aware "really freed" sizes (pnpm, Yarn Berry).
- **Simulators & emulators** — iOS simulators, runtimes, orphan device folders, hidden XCTest recordings and unified logs; Android AVDs, unused system images, NDKs, Gradle distributions (SDKs on external drives are reported, never touched).
- **Package managers & node versions** — npm, Yarn v1 and Berry (cache, metadata, store), pnpm, bun, corepack, nvm/fnm/mise/volta node versions (keeps the ones you use), Metro/Jest/Vitest temp caches.
- **AI tools data** — Claude Code, Claude desktop, Codex, Cursor, ChatGPT, Conductor… old versions, orphaned project data, caches and logs. Credentials, configs, memories and chat databases are protected.
- **Safe by design** — every path is re-checked right before deletion: protected paths, inode identity since the scan, running apps, open databases, processes working inside, marker files. Dry-run everywhere. Nothing is preselected: nothing is deleted without your explicit selection.
- **Fast** — Go, macOS `getattrlistbulk` (≈3× faster than `du`), results streamed into a Bubble Tea TUI.
- **Scriptable** — `--json` outputs, strict rules for non-interactive `--yes`, exit codes, and LLM-ready docs (`llms.txt`).

## Install

```bash
go install github.com/ludwig-pro/lu-cleaner/cmd/lu-cleaner@latest
```

Or download the universal macOS binary from the [latest release](https://github.com/ludwig-pro/lu-cleaner/releases/latest), or build from source:

```bash
git clone https://github.com/ludwig-pro/lu-cleaner && cd lu-cleaner && make install
```

## Quick start

```bash
lu-cleaner                                  # interactive dashboard: you pick what to remove, nothing is preselected
lu-cleaner clean                            # same picker, straight from the command
lu-cleaner clean --smart                    # picker that starts with the recommended items selected
lu-cleaner worktrees                        # only git worktrees
lu-cleaner artifacts                        # npkill-like: node_modules, Pods, builds…
lu-cleaner devices                          # simulators, runtimes, emulators
lu-cleaner scan                             # report, no deletion
lu-cleaner clean --yes --smart --dry-run    # what the recommended items add up to, nothing deleted
lu-cleaner doctor                           # why is my disk still full?
lu-cleaner analyze ~                        # ncdu-like explorer
```

Full documentation: **https://ludwig-pro.github.io/lu-cleaner/**

## Safety in one paragraph

lu-cleaner never uses `sudo`, never touches system folders, never removes a git repository, your scan roots, credentials or tool configurations, and never follows symlinks. Risky items (user data, dirty worktrees, sessions) are marked *caution*: they are never picked by smart select (the `a` key, `--smart`), never cleaned by `clean --yes` unless you pass `--risk caution`, and need a typed `yes` in the UI. Read the [safety model](https://ludwig-pro.github.io/lu-cleaner/concepts/safety/).

## Development

```bash
make build      # bin/lu-cleaner
make test       # go test ./...
make docs       # regenerate the CLI reference pages of the site
make site-dev   # documentation site on http://localhost:4321/lu-cleaner/
```

See [contributing](https://ludwig-pro.github.io/lu-cleaner/about/contributing/) and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## License

[MIT](LICENSE)
