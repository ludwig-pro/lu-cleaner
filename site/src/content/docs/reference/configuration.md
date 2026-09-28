---
title: Configuration
description: Every key of config.toml with its default and examples, how project and worktree roots are found, and the environment variables lu-cleaner reads.
sidebar:
  order: 3
---

lu-cleaner works without any configuration. The configuration file is optional, and so is every key in it: a missing key keeps its default.

## The configuration file

The file is TOML. lu-cleaner looks for it at the first of these locations:

| Condition | Path |
| --- | --- |
| `LU_CLEANER_CONFIG` is set | Its value, used as the full path of the file. |
| `XDG_CONFIG_HOME` is set | `$XDG_CONFIG_HOME/lu-cleaner/config.toml` |
| Otherwise | `~/.config/lu-cleaner/config.toml` |

Four commands manage it:

```bash
lu-cleaner config init   # write a commented sample (never overwrites an existing file)
lu-cleaner config edit   # open it in $VISUAL or $EDITOR (default: open -t), creating it if needed
lu-cleaner config show   # effective values, resolved roots and the history file
lu-cleaner config path   # print the path of the file
```

`lu-cleaner config` alone is the same as `config show`, and `config show --json` prints the same information as JSON. See the [`config` command reference](/lu-cleaner/reference/commands/config/).

### Validation

The file is validated every time it is loaded. lu-cleaner refuses to scan or clean until the file is fixed if it contains:

- an unknown key, such as a typo like `stale_afer`;
- a size, age or category name that cannot be parsed.

This strictness is deliberate: a misspelled `protect` or `exclude` that was silently ignored would leave unprotected the very paths you meant to protect. `config edit` validates the file when the editor exits and reports any error.

Paths in every key accept `~` for your home folder. Relative paths are relative to your home folder.

## Example

```toml
# ~/.config/lu-cleaner/config.toml

# Scan only these folders for projects (instead of auto-detecting them).
roots = ["~/dev", "~/work/clients"]

# An in-house agent creates worktrees here.
worktree_roots = ["~/agents/worktrees"]

# Never look inside, never clean.
exclude = ["~/dev/legacy-monorepo"]

# Never clean these, nor anything that contains them.
protect = ["~/.android/avd/Pixel_8_API_35.avd", "~/Library/Developer/Xcode/Archives"]

# Hide the small stuff, and only call "stale" what is a month old.
min_size = "50MB"
stale_after = "30d"

# Keep two versions of versioned AI tool CLIs.
keep_latest = 2

# Not interested in these categories.
disabled_categories = ["containers", "langs"]

# A build output folder specific to your projects.
extra_artifacts = ["tmp-build"]
```

## Keys

| Key | Type | Default | Summary |
| --- | --- | --- | --- |
| [`roots`](#roots) | list of paths | auto-detected | Folders scanned for projects. |
| [`worktree_roots`](#worktree_roots) | list of paths | `[]` | Extra folders where tools create git worktrees. |
| [`exclude`](#exclude) | list of paths | `[]` | Never scanned, never cleaned. |
| [`protect`](#protect) | list of paths | `[]` | Never cleaned, in addition to the built-in list. |
| [`max_depth`](#max_depth) | integer | `8` | Depth of the project scan below each root. |
| [`min_size`](#min_size) | size | `"1MB"` | Hide smaller items in lists. |
| [`stale_after`](#stale_after) | age | `"14d"` | When an unused item becomes stale. |
| [`keep_latest`](#keep_latest) | integer | `1` | Versions kept for versioned AI tool CLIs. |
| [`disabled_categories`](#disabled_categories) | list of categories | `[]` | Categories skipped entirely. |
| [`use_trash`](#use_trash) | boolean | `false` | Move to the Trash instead of deleting. |
| [`extra_artifacts`](#extra_artifacts) | list of names | `[]` | More project artifact folder names. |

Command-line flags take precedence over the file: `--root` replaces `roots`, `--min-size` replaces `min_size`, and `--trash` turns Trash mode on even when `use_trash = false`.

### `roots`

Folders scanned for project artifacts (`node_modules`, `ios/Pods`, `ios/build`, `android/app/build`, `.expo`…). lu-cleaner also looks there for git repositories, to list their worktrees, and for the project files that pin a Node.js version.

```toml
roots = ["~/dev", "~/work/clients"]
```

- Folders that do not exist are ignored.
- A folder nested in another root is dropped: the outer one already covers it.
- `lu-cleaner --root <dir>` (repeatable) and the positional arguments of `lu-cleaner artifacts <dir>…` replace `roots` for one run. Those must exist, and relative paths are resolved from the current directory.

#### Auto-detection

When `roots` is empty (the default), lu-cleaner uses every folder of this list that exists:

`~/local_sources`, `~/dev`, `~/Dev`, `~/Developer`, `~/Projects`, `~/projects`, `~/code`, `~/Code`, `~/src`, `~/workspace`, `~/Workspace`, `~/repos`, `~/git`, `~/github`, `~/GitHub`, `~/Documents/GitHub`, `~/Documents/Projects`, `~/Documents/dev`, `~/Sites`, `~/conductor/repos`.

A candidate is used only if its name matches exactly, including case. On a case-insensitive APFS volume, `~/Dev` and `~/dev` are the same folder, and matching the exact name prevents scanning it twice. Excluded folders are skipped.

Run `lu-cleaner config show` to see the result: the roots are listed under **Project roots**, with where they came from (`auto-detected`, `config`, `--root` or `arguments`).

:::tip
If your projects live somewhere else, such as `~/work`, set `roots` explicitly. Auto-detection only knows the folder names above.
:::

### `worktree_roots`

Extra folders where tools create git worktrees. They are added to the built-in list, never replace it:

| Built-in worktree root | Created by |
| --- | --- |
| `~/.codex/worktrees` | Codex |
| `~/.cursor/worktrees` | Cursor |
| `~/conductor/workspaces` | Conductor |
| `~/.claude/worktrees`, `~/.claude-worktrees` | Claude Code |
| `~/Library/Application Support/Claude/worktrees` | Claude desktop app |
| `~/.superset/worktrees` | Superset |
| `~/.worktrees` | Manual or other tools |

Only folders that exist are used. Each one is searched, up to four levels deep, for linked worktrees (directories whose `.git` is a file), and scanned for project artifacts like a root.

You rarely need this key. Worktrees are also found through `git worktree list` on every repository found under your roots, and in the `.claude/worktrees`, `.worktrees` and `worktrees` folders of each repository (and a `<repo>-worktrees` folder next to it). Add a folder here when a tool creates worktrees in a custom location **and** the main repositories are outside your roots.

```toml
worktree_roots = ["~/agents/worktrees"]
```

See [Clean up AI agent worktrees](/lu-cleaner/guides/ai-worktrees/).

### `exclude`

Paths lu-cleaner never looks inside and never cleans. Use it for folders that are slow to scan and hold nothing to clean (a huge archive, a mounted dataset), or that you want out of the picture entirely.

```toml
exclude = ["~/dev/legacy-monorepo", "~/datasets"]
```

Matching is by whole path components: `~/dev/legacy` excludes `~/dev/legacy/app` but not `~/dev/legacy-app`. Excluded paths are also handed to the safety guard, so nothing inside them, and nothing containing them, can be deleted.

### `protect`

Paths that are never cleaned, nor anything inside them, nor anything that contains them. They add to the built-in list of protected paths: SSH and GPG keys, cloud credentials, shell profiles, the Keychain, Mail, Messages, iCloud Drive, AI tool settings, memories and session indexes, the Android debug keystore, Finder device backups, and more. See [Safety](/lu-cleaner/concepts/safety/).

```toml
# Keep this emulator and every Xcode archive, whatever the scan says.
protect = ["~/.android/avd/Pixel_8_API_35.avd", "~/Library/Developer/Xcode/Archives"]
```

Scanners do not propose protected paths, and the safety guard refuses them at deletion time if they show up anyway.

**`exclude` or `protect`?** Both prevent deletion. `exclude` also stops lu-cleaner from reading the path at all, which speeds up scans. Use `protect` for one specific thing inside a folder that should otherwise be scanned normally, such as one emulator among many.

### `max_depth`

How many levels below each root the project scan descends. The default, `8`, covers layouts like `~/dev/clients/acme/apps/mobile/node_modules`. The worktree scanner uses the same limit when it looks for repositories under your roots.

```toml
max_depth = 10
```

A value of `0` or less means the default. A deeper scan finds more nested projects but takes longer.

### `min_size`

Items smaller than this are hidden from the dashboard, the pickers, `scan` and `doctor`.

```toml
min_size = "50MB"
```

Sizes accept decimal units by default (`500MB`, `1.5GB`, `1.5G`) and binary units with an `i` (`2GiB`). A bare number is in bytes; `"0"` shows everything. Items still being measured, and items that run a command, are never hidden.

`--min-size` overrides it for one command. `clean --yes` ignores `min_size` and cleans items of every size, unless you pass `--min-size` explicitly. This keeps a scripted cleanup like `lu-cleaner clean --yes -k node_modules` from silently leaving the small ones behind.

### `stale_after`

An item not used for longer than this is **stale**. The picker shows its age in yellow with a `stale` tag, and <span class="risk moderate">moderate</span> items become eligible for smart select once they are stale. A few moderate items are recommended by their scanner whatever their age, such as superseded versions of a tool. [Risk levels and smart select](/lu-cleaner/concepts/risk-and-smart-select/) explains the rules.

```toml
stale_after = "30d"
```

Ages accept `h` (hours), `d` (days), `w` (weeks), `m` (months of 30 days) and `y` (years); a bare number means days. Examples: `"12h"`, `"14d"`, `"2w"`, `"6m"`, `"1y"`. The same format is used by `--older-than`.

### `keep_latest`

How many of the most recent versions to keep for versioned installs, besides the ones that are active, pinned or running. Default `1`, minimum `1`.

```toml
keep_latest = 2
```

In the current version, `keep_latest` applies to the old versions of AI tool CLIs: Claude Code (standalone, and the copies bundled by the Claude desktop app and by Conductor), cursor-agent, the Cursor `origin` CLI, Conductor's bundled Codex, GitHub Copilot CLI and vibe-kanban. Other versioned tools follow their own rules: for example, a Node.js version is never recommended while it is a default, pinned by a project, on your `PATH` or running, and the newest version of each Android SDK package (NDK, build-tools…) is never recommended. See [Scanners](/lu-cleaner/reference/scanners/).

### `disabled_categories`

Categories skipped entirely: their scanners do not run unless another enabled category needs them, and their items are never listed or cleaned.

```toml
disabled_categories = ["containers", "langs", "system"]
```

Valid names are `worktrees`, `artifacts`, `simulators`, `xcode`, `android`, `ai`, `js`, `ide`, `containers`, `langs` and `system`. See [Categories](/lu-cleaner/concepts/categories/).

A disabled category passed to `-c` is ignored. When every category you pass is disabled, for example `-c containers` with the configuration above, the command fails with an error that points back to this key.

### `use_trash`

Move items to `~/.Trash` instead of deleting them:

```toml
use_trash = true
```

It is the same as passing `--trash` to every command, and the picker starts in Trash mode (press `t` to switch back to delete mode for the session). Only items that would be deleted are affected: commands such as `xcrun simctl delete unavailable` and `git worktree remove` run as usual.

:::caution
Moving to the Trash frees **nothing** until you empty it, and the default is `false` for that reason. The summary of each cleanup reminds you of it. The safety of lu-cleaner does not rely on the Trash: use `--dry-run` and the confirmation dialog to check what will be removed.
:::

### `extra_artifacts`

More directory names to treat as project artifacts, in addition to the built-in ones (`node_modules`, `Pods`, `build`, `.gradle`, `.expo`, `.next`…).

```toml
extra_artifacts = ["tmp-build", ".cache-loader"]
```

- Names only: a value containing `/` is ignored, and so are names that are too generic to be safe: `src`, `source`, `app`, `apps`, `lib`, `packages`, `ios`, `android`, `.git`.
- Matches are listed in **Project artifacts** with the kind `extra-artifact` and the risk <span class="risk moderate">moderate</span>.
- Inside a git repository, a folder only matches if git ignores it, so a tracked folder that happens to have the same name is never proposed. Outside git, any folder with that name matches.

To clean only these: `lu-cleaner clean --yes -k extra-artifact --dry-run`.

## Environment variables

### lu-cleaner

| Variable | Effect |
| --- | --- |
| `LU_CLEANER_CONFIG` | Full path of the configuration file. Takes precedence over `XDG_CONFIG_HOME`. |
| `XDG_CONFIG_HOME` | The configuration file is `$XDG_CONFIG_HOME/lu-cleaner/config.toml`. Default: `~/.config`. |
| `XDG_STATE_HOME` | The history is `$XDG_STATE_HOME/lu-cleaner/history.jsonl`. Default: `~/.local/state`. |
| `NO_COLOR` | Any non-empty value disables colors, in command output and in the interactive screens (same as `--no-color`). `TERM=dumb` also disables them in command output. |
| `VISUAL`, `EDITOR` | Editor used by `lu-cleaner config edit`, `VISUAL` first. Without either, the file opens with `open -t`. |
| `TMPDIR` | Your per-user temporary folder. Caches in it (Metro, Jest, Xcode tools…) are scanned, and the safety guard allows deletions inside it. |
| `LU_WALKERS` | Maximum number of directories read concurrently while measuring sizes. Default: 3 × the number of CPU cores, at least 8. Lower it to reduce disk pressure during scans. |
| `LU_NO_BULK` | Any non-empty value measures sizes with one `lstat` call per entry instead of the macOS `getattrlistbulk` bulk call. Slower: only useful to troubleshoot a size difference. |

### Tool locations

Scanners follow the standard variables of the tools they inspect, so relocated installs are found:

| Ecosystem | Variables |
| --- | --- |
| Android | `ANDROID_HOME`, `ANDROID_SDK_ROOT`, `ANDROID_AVD_HOME`, `ANDROID_USER_HOME`, `ANDROID_EMULATOR_HOME`, `ANDROID_PREFS_ROOT`, `GRADLE_USER_HOME`, `JAVA_HOME` |
| Node.js versions | `NVM_DIR`, `FNM_DIR`, `FNM_MULTISHELL_PATH`, `VOLTA_HOME`, `ASDF_DATA_DIR`, `MISE_DATA_DIR`, `MISE_CONFIG_DIR`, `MISE_GLOBAL_CONFIG_FILE` |
| Package managers | `NPM_CONFIG_CACHE`, `NPM_CONFIG_PREFIX` (and their lowercase `npm_config_*` forms), `PNPM_HOME`, `YARN_GLOBAL_FOLDER`, `PLAYWRIGHT_BROWSERS_PATH` |
| AI models | `OLLAMA_MODELS`, `HF_HOME`, `HF_HUB_CACHE` |
| Other toolchains | `RUSTUP_HOME`, `RBENV_ROOT` |
| XDG | `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_RUNTIME_DIR`, `XDG_CONFIG_HOME` |

GUI apps and AI agents often run without your shell environment. For the Android variables, lu-cleaner therefore also reads simple `export` lines from `~/.zshenv`, `~/.zprofile`, `~/.zshrc`, `~/.bash_profile`, `~/.bashrc` and `~/.profile`.

## See also

- [`lu-cleaner config`](/lu-cleaner/reference/commands/config/) and its subcommands.
- [How it works](/lu-cleaner/concepts/how-it-works/): how roots feed the scanners.
- [Safety](/lu-cleaner/concepts/safety/): the built-in protected paths.
