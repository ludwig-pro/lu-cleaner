# Demos

Animated demos of lu-cleaner, recorded with [VHS](https://github.com/charmbracelet/vhs)
on a **synthetic** home folder. They are served by the documentation site from
`site/public/demos/` (`https://ludwig-pro.github.io/lu-cleaner/demos/…`) and used by
`README.md`, the site home page and *Your first cleanup*.

| Tape | Shows | Outputs |
| --- | --- | --- |
| `tapes/dashboard.tape` | `lu-cleaner`: the scan streaming in (nothing is selected afterwards), worktree details, project artifacts, `a` smart-selects the recommended ones, one more item picked with space, confirmation, cleaning, summary | `dashboard.gif`, `.mp4`, `.jpg` (poster) |
| `tapes/worktrees.tape` | `lu-cleaner worktrees --list` (tool, branch, status), then the picker (nothing selected): a merged worktree is picked with space and removed, a dirty one is picked too and refused by git's checks after the typed `yes` | `worktrees.gif`, `.mp4`, `.jpg` |
| `tapes/scan.tape` | `lu-cleaner scan --summary`, then `lu-cleaner clean --yes --smart --dry-run` | `scan.gif`, `.mp4`, `.jpg` |

## Re-record

```bash
brew install vhs            # pulls ttyd and ffmpeg
demo/record.sh              # every tape (several minutes: VHS renders in real time)
demo/record.sh dashboard    # one tape
```

`record.sh` builds `demo/bin/lu-cleaner` from the working tree (`LU_DEMO_BIN=path` to use
another binary), then for each tape: builds a fresh sandbox, runs VHS, extracts the poster
frame, deletes the sandbox and prints the file sizes (a warning above 3 MB). Record on a quiet
machine: lu-cleaner holds items back while Xcode, `xcodebuild` or Codex run (in-use guards), so
`record.sh` waits until none has run for 5 s, and records a tape again when the sandbox's
`lu-cleaner` function saw one running meanwhile (the tape's `# busy:` comment or
`LU_DEMO_BUSY_RE` sets the list, empty to disable). Other lu-cleaner scans running on the machine
start `xcodebuild` for a few seconds: expect a retry now and then. The disk bar and the
"Measured freed" line show the real startup disk: they come from `statfs`.

## The sandbox

`setup-sandbox.sh [DIR]` (default `/tmp/lu-cleaner-demo`, about 185 MB) creates:

- `home/code/`: `my-app` and `shop-app` (Expo / React Native: `node_modules`, `ios/Pods`,
  `ios/build`, `android/app/build`, `android/.gradle`, `.expo`) and `landing-page` (Next.js),
  real git repositories with a local bare `origin` (`remotes/`), backdated so that some look
  active and others stale;
- AI agent worktrees made with real git: Codex (`~/.codex/worktrees`, merged and pushed, idle),
  Conductor (`~/conductor/workspaces`, merged, idle), Cursor (`~/.cursor/worktrees`, uncommitted
  changes) and Claude Code (`my-app/.claude/worktrees`, two commits never pushed);
- caches: npm `_cacache` and `_npx`, Yarn v1 and Berry, CocoaPods, Gradle, Xcode DerivedData
  (one folder per workspace, one whose workspace is gone) and iOS DeviceSupport;
- AI tools data: Claude Code sessions and MCP logs, Codex sessions and logs.

Files are real but small (lu-cleaner measures allocated blocks: sparse files would show 0 B);
`LU_DEMO_SCALE=<percent>` scales them (default 50).

`source DIR/env.sh` defines a `lu-cleaner` shell function that runs the binary with:

- `env -i`: `HOME`, `TMPDIR` and `XDG_*` inside the sandbox, nothing inherited from your session
  (`ANDROID_HOME`, `GOPATH`, `NVM_DIR`…), `LU_NO_CACHE=1`;
- a `PATH` holding only the demo binary, git, `plutil`, `sqlite3` and no-op stand-ins for
  `xcrun`, `xcodebuild`, `xcode-select`, `docker`, `colima`, `brew`, `pnpm`, `watchman`,
  `osascript`, `tmutil`, `mdfind`, `defaults`, `pgrep`, `open`… (they log to
  `DIR/fake-calls.log`, run nothing and fail);
- a macOS sandbox profile (`sandbox-exec -f DIR/sandbox.sb`) that denies reading your home, your
  per-user temp and cache folders, `/Library`, `/Applications`, Homebrew's Cellar listing and
  the swap size; denies running the real `xcrun`, `xcodebuild`, `brew`, `docker`, `tmutil`,
  `osascript`, `open`…; and denies **writing anywhere outside DIR**. Only `/bin/ps` runs
  outside the profile (it is setuid, and only lists processes for the in-use guards).

`demo/check-sandbox.sh` verifies those promises on a throwaway sandbox (about 30 s): every item
lu-cleaner reports lies inside it, the worktree statuses are the expected ones, the profile
denies reading your home and writing outside, the stand-ins are used, and a real
`clean --yes --smart` inside it removes the merged worktrees and keeps the dirty and unpushed
ones and every branch. Run it after changing `setup-sandbox.sh`.

You can also explore the sandbox by hand:

```bash
make build
DIR=$(demo/setup-sandbox.sh) && source "$DIR/env.sh"
lu-cleaner scan
cd - >/dev/null && rm -rf "$DIR"
```

## Writing a tape

Start with `Source demo/tapes/settings.tape` (size, theme, font, and the hidden
`source demo/tapes/shell.sh` that loads the sandbox and the prompt). Add `# poster: <seconds>`
to pick the poster frame, and `# busy: <regex>` when only some in-use guards matter to the tape
(the worktrees tape shows no Xcode-guarded item, so a running `xcodebuild` does not matter).
Nothing is ever preselected: after the scan the picker says "Nothing is selected", so wait for
that (`Wait+Screen@30s /Nothing is selected/`), then pick with `Space`, or with `a` (smart select:
the recommended items of the current view). Note that `a` sets the selection of the view, so press
it before adding items by hand. For a command whose output scrolls, wait for the prompt with `Wait+Line /❯ *$/`: `Wait+Screen`
only sees the first page. Do not type non-ASCII characters (VHS mistypes them).
