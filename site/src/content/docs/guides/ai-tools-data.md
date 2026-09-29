---
title: Clean AI tools data
description: Reclaim the space Claude Code, Claude desktop, Codex, Cursor and other AI tools accumulate (old transcripts, data of deleted worktrees, superseded versions, caches, local models) while credentials, settings and memories stay untouched.
sidebar:
  order: 6
---

AI coding tools write a lot to your home folder, and most of them never clean up after themselves:

- **Transcripts** of every session, often with screenshots and tool outputs, kept for weeks or forever.
- **Per-project data** keyed by folder path. Every worktree an agent creates gets its own entry, and that entry stays after the worktree is deleted.
- **Old versions.** Auto-updaters download a new build (200 MB to 600 MB) and keep the previous ones.
- **Caches, logs and databases** that only grow: Electron caches, debug logs, a Codex logs database that is never pruned.
- **Local models** (Ollama, LM Studio, Hugging Face) that you pulled once to try.

lu-cleaner finds these with a dedicated scanner (the `ai` provider) plus fixed locations from the [catalog](/lu-cleaner/reference/catalog/). It separates what is safe to remove from what is your data, and it never proposes credentials, settings, memories or chat databases.

:::note
This page covers the data the tools keep in your home folder. The git worktrees that agents create are covered in [Clean up AI agent worktrees](/lu-cleaner/guides/ai-worktrees/).
:::

## See what your AI tools use

```bash
lu-cleaner scan -c ai          # AI tools data
lu-cleaner scan -c ai,ide      # plus Cursor's editor side (old builds, workspace state, extensions)
lu-cleaner catalog -c ai       # the fixed paths lu-cleaner checks, with their risk
```

Cursor is split across two [categories](/lu-cleaner/concepts/categories/): its agent data is in `ai`, and its editor caches (the parts it shares with VS Code) are in `ide`.

```text frame="terminal" title="lu-cleaner scan -c ai"
AI tools  21.4 GB · 36 items · ★ 6.90 GB recommended
  NAME                                                                    SIZE  AGE  RISK      RECO  PATH
  Ollama model · llama3.1:8b                                           4.92 GB  4mo  caution         ~/.ollama/models
  Codex sessions > 30d · 2026-07 (184 rollouts)                        3.40 GB  2mo  caution         ~/.codex/sessions/2026/07/…
  Claude Code sessions > 30d · ~/dev/my-app (96 sessions)              2.10 GB   5w  caution         ~/.claude/projects/-Users-me-dev-my-app/…
  Codex app runtime (node / python / native deps)                      1.60 GB   3w  moderate  ★     ~/.cache/codex-runtimes/…
  Claude Code data of deleted folder · ~/.codex/worktrees/a1b2/my-app  1.20 GB   6w  moderate  ★     ~/.claude/projects/-Users-me--codex-worktrees-a1b2-my-app/…
  Codex logs database (logs_2.sqlite)                                  1.10 GB  now  safe      ★     ~/.codex/logs_2.sqlite
  Cursor · bundled cursor-agent · 2026.08.31-4057e58 (old version)      610 MB   6w  safe      ★     ~/Library/Application Support/Cursor/User/globalStorage/…
  …
```

Sizes and names above are illustrative. The `★` column is [smart select](/lu-cleaner/concepts/risk-and-smart-select/): items lu-cleaner recommends (they are the ones the `a` key selects).

## Clean it

The interactive picker is the best way to review this category, because much of it is a judgement call:

```bash
lu-cleaner clean -c ai
```

Nothing is selected when the picker opens. Press `a` to select the recommended items, or pick with `space`. Items with the <span class="risk caution">caution</span> risk (transcripts, models, generated images) are never recommended, and cleaning them asks you to type `yes`.

For scripts, start with a dry run:

```bash
lu-cleaner clean --yes --smart -c ai --dry-run   # the plan, nothing deleted
lu-cleaner clean --yes --smart -c ai             # clean the recommended items
```

`clean --yes` leaves out <span class="risk caution">caution</span> items, and items with a warning (an app running, for example), unless you pass `--risk caution`. The plan lists the warned items under `Held back`. To remove old Claude Code transcripts on purpose, name their kind:

```bash
lu-cleaner clean --yes -k claude-code-old-sessions --risk caution --dry-run
```

:::tip[Quit the apps first]
Many items are guarded by the app that owns them: Cursor, Codex, the ChatGPT app, Claude desktop, Conductor, Ollama, LM Studio. While the app runs, the item carries a warning ("Cursor is running — quit it before cleaning"), smart select skips it, and the clean skips it. `lu-cleaner doctor` lists the running apps that block a cleanup.
:::

### A good order: worktrees first

Claude Code and Cursor keep per-folder data. lu-cleaner can only call that data an orphan once the folder is gone. Remove finished agent worktrees first, then scan again. Their leftovers then appear as "data of deleted folder" items:

```bash
lu-cleaner worktrees        # remove the finished worktrees
lu-cleaner clean -c ai      # their Claude Code / Cursor data now shows up as orphaned
```

A folder that disappeared recently may come back (a renamed folder, a restored Conductor workspace, a re-created worktree), and transcripts and chats are never regenerated. So this data is <span class="risk caution">caution</span> and never recommended during the first 30 days after its last write. After that, it becomes <span class="risk moderate">moderate</span> and is recommended, unless its folder was only guessed from the directory name or is a Conductor workspace.

## Claude Code

| What | Where | Risk | Smart select |
|---|---|---|---|
| **Sessions older than 30 days**, one item per project<br/><sub>`claude-code-old-sessions`</sub> | `~/.claude/projects/<project>/` | <span class="risk caution">caution</span> | No |
| **Data of a deleted folder** (orphaned project)<br/><sub>`claude-code-orphan-project`</sub> | `~/.claude/projects/<project>/` | <span class="risk moderate">moderate</span> once untouched for 30 days; <span class="risk caution">caution</span> before, or when the folder was guessed or is a Conductor workspace | Only when moderate |
| **Old CLI versions**<br/><sub>`claude-code-old-version`</sub> | `~/.local/share/claude/versions/` | <span class="risk moderate">moderate</span> | Yes |
| **Debug logs** (older than 1 day)<br/><sub>`ai-claude-code-debug-logs`</sub> | `~/.claude/debug/` | <span class="risk safe">safe</span> | Yes |
| **Shell snapshots** (older than 7 days)<br/><sub>`ai-claude-code-shell-snapshots`</sub> | `~/.claude/shell-snapshots/` | <span class="risk safe">safe</span> | Yes |
| **Telemetry, feature-flag cache, changelog**<br/><sub>`ai-claude-code-telemetry`</sub> | `~/.claude/telemetry/`, `~/.claude/statsig/` | <span class="risk safe">safe</span> | Yes |
| **Per-session temp dirs and todo lists** (older than 7 days)<br/><sub>`ai-claude-code-session-temp`, `ai-claude-code-todos`</sub> | `~/.claude/session-env/`, `~/.claude/tasks/`, `~/.claude/todos/` | <span class="risk safe">safe</span> | Yes |
| **MCP server logs**<br/><sub>`ai-claude-code-mcp-logs`</sub> | `~/Library/Caches/claude-cli-nodejs/` | <span class="risk safe">safe</span> | Yes |
| **Paste cache** (older than 30 days)<br/><sub>`ai-claude-code-paste-cache`</sub> | `~/.claude/paste-cache/` | <span class="risk moderate">moderate</span> | When stale |
| **`~/.claude.json` backups** (all but the newest, older than 7 days)<br/><sub>`claude-code-config-backups`</sub> | `~/.claude/backups/`, `~/` | <span class="risk moderate">moderate</span> | When stale |
| **File checkpoints behind `/rewind`** (older than 30 days)<br/><sub>`ai-claude-code-file-history`</sub> | `~/.claude/file-history/` | <span class="risk caution">caution</span> | No |
| **Plan files** (older than 60 days)<br/><sub>`ai-claude-code-plans`</sub> | `~/.claude/plans/` | <span class="risk caution">caution</span> | No |

"When stale" means the item is recommended once it has been unused for longer than `stale_after`, which is 14 days by default. See [Configuration](/lu-cleaner/reference/configuration/).

A shell snapshot is sourced by every command of the session that created it, so a session left open for days still needs its snapshot: only snapshots older than a week are proposed.

**Old sessions.** Claude Code stores one transcript per session (`<id>.jsonl`), with a folder next to it for subagent logs and tool outputs. lu-cleaner groups the sessions of each project that have not been written for 30 days. It skips any session listed as alive in `~/.claude/sessions/`. Deleting old sessions removes them from `claude --resume` and the history. Claude Code also prunes transcripts itself after `cleanupPeriodDays` (30 days by default). If you raised that setting, lu-cleaner lets you choose which projects to trim.

**Old CLI versions.** The native installer keeps every downloaded build (about 200 MB each). lu-cleaner keeps the version `~/.local/bin/claude` points to, the newest one (the updater may have downloaded it already), and any version a running process uses. To keep more versions, set `keep_latest`.

### How orphaned projects are detected

Claude Code names each project folder after the working directory, with every character that is not a letter or a digit replaced by `-`. For example, `~/.codex/worktrees/a1b2/my-app` becomes `-Users-me--codex-worktrees-a1b2-my-app`. You cannot decode that name reliably, so lu-cleaner does not try to parse it first:

1. It reads the real `cwd` of each session from its transcript, or from `sessions-index.json`. Several folders can share one project directory (`my-app` and `my_app` encode the same way), so each session is judged on its own folder.
2. If no session names its folder, it looks for a folder on disk whose encoded name matches. Such an item says the folder was *guessed for the whole directory* and is never recommended.
3. A folder that cannot be checked (unmounted volume, unreadable parent, macOS privacy protection, a logged-out cloud storage provider) counts as "unknown", never as "deleted".

A session becomes an orphan only when its folder verifiably no longer exists, no live session runs there, and nothing was written in the last hour. It is recommended only when its folder was read from the sessions, is not a Conductor workspace (Conductor restores archived workspaces), and nothing was written for 30 days. Sessions of other folders sharing the directory are kept. The auto-memory (`memory/`) is always kept: lu-cleaner deletes the transcripts around it, never the folder. Right before deletion, it checks again that the folder is still missing and that no session has started there.

## Claude desktop

| What | Where | Risk | Smart select |
|---|---|---|---|
| **Cowork VM image** (`rootfs.img`, `initrd`, `vmlinuz`)<br/><sub>`ai-claude-desktop-vm-image`</sub> | `~/Library/Application Support/Claude/vm_bundles/claudevm.bundle/` | <span class="risk moderate">moderate</span> | When stale |
| **Old bundled Claude Code builds**<br/><sub>`claude-desktop-old-claude-code`, `claude-desktop-old-claude-code-vm`</sub> | `…/Claude/claude-code/`, `…/Claude/claude-code-vm/` | <span class="risk safe">safe</span> | Yes |
| **Electron caches** (HTTP, code, GPU, updater)<br/><sub>`ai-claude-desktop-caches`</sub> | `…/Claude/Cache`, `…/Claude/Code Cache`… | <span class="risk safe">safe</span> | Yes |
| **Cowork VM session disk** (`sessiondata.img`) | `…/claudevm.bundle/` | <span class="risk caution">caution</span> | Report only |
| **Agent and Code session state, git shadow repos** | `…/Claude/local-agent-mode-sessions`… | <span class="risk caution">caution</span> | Report only |

The VM image is a sparse Linux disk, about 10 GB allocated. Claude desktop downloads it again the next time Cowork starts. The session disk, which holds the files of your Cowork sessions, is only reported. For bundled Claude Code builds, lu-cleaner keeps the newest version and the one named in `claude-code-vm/.sdk-version`. The VM image and the caches wait until the Claude app is closed.

## Codex (CLI and app)

| What | Where | Risk | Smart select |
|---|---|---|---|
| **Sessions older than 30 days**, one item per month<br/><sub>`codex-old-sessions`</sub> | `~/.codex/sessions/YYYY/MM/` | <span class="risk caution">caution</span> | No |
| **Archived sessions**, one item per month<br/><sub>`codex-archived-sessions`</sub> | `~/.codex/archived_sessions/` | <span class="risk caution">caution</span> | No |
| **Logs database**<br/><sub>`codex-logs-db`</sub> | `~/.codex/logs_*.sqlite` (+ `-wal`, `-shm`) | <span class="risk safe">safe</span> | Yes, when Codex is closed |
| **Stale copy of the logs database**<br/><sub>`codex-stale-logs-db`</sub> | `~/.codex/sqlite/logs_*.sqlite` | <span class="risk safe">safe</span> | Yes |
| **Logs database repair backups** (older than 7 days)<br/><sub>`ai-codex-repair-backups`</sub> | `~/.codex/logs_*.codex-repair-*.bak` | <span class="risk safe">safe</span> | Yes |
| **Repair backups of your data databases** (memories, goals; older than 30 days)<br/><sub>`ai-codex-repair-backups-data`</sub> | `~/.codex/*.codex-repair-*.bak` | <span class="risk caution">caution</span> | No |
| **App runtime** (node, python, native deps)<br/><sub>`ai-codex-runtimes`</sub> | `~/.cache/codex-runtimes/` | <span class="risk moderate">moderate</span> | When stale |
| **Plugin caches and sync clones**<br/><sub>`ai-codex-plugin-cache`, `ai-codex-plugin-staging`</sub> | `~/.codex/plugins/cache`, `~/.codex/.tmp/` | <span class="risk moderate">moderate</span> | When stale |
| **Catalog caches, shell snapshots, app Chromium caches**<br/><sub>`ai-codex-caches`, `ai-codex-shell-snapshots`, `ai-codex-app-caches`</sub> | `~/.codex/cache/`, `~/.codex/shell_snapshots/`, `~/Library/Application Support/Codex/` | <span class="risk safe">safe</span> | Yes |
| **CLI log files** (older than 14 days)<br/><sub>`ai-codex-log-files`</sub> | `~/.codex/log/` | <span class="risk moderate">moderate</span> | When stale |
| **Generated images and visualizations** (older than 30 days)<br/><sub>`ai-codex-generated-images`, `codex-visualizations`</sub> | `~/.codex/generated_images/`, `~/.codex/visualizations/` | <span class="risk caution">caution</span> | No |
| **Thread history DB, old state and memories DB copies, PR archives, attachments, `~/Documents/Codex`** | `~/.codex/…` | <span class="risk caution">caution</span> | Report only |

**Sessions.** A single rollout file can reach hundreds of MB when it contains screenshots. Deleting one makes that thread impossible to resume. Pinned threads are always kept: lu-cleaner reads their list from the Codex state database with a read-only `sqlite3` query.

**Repair backups.** When Codex repairs a database, it keeps a copy of the file from before the repair. For the logs database that copy is waste. For the memories and goals databases, it is the only way to recover rows a repair dropped, so those copies are only proposed as <span class="risk caution">caution</span> items. Backups of the state database are protected.

**Logs database.** `logs_*.sqlite` only holds tracing and feedback logs. Codex never prunes or vacuums it, and creates an empty one at the next start. It is deleted together with its `-wal` and `-shm` files, so it must not be open. The item waits until `codex`, the Codex app **and the ChatGPT app** are closed (ChatGPT embeds Codex). Right before deletion, lu-cleaner also checks with `lsof` that no process has the file open.

## Cursor

| What | Where | Category | Risk | Smart select |
|---|---|---|---|---|
| **CachedData of old app builds**<br/><sub>`cursor-cached-data-old-builds`</sub> | `~/Library/Application Support/Cursor/CachedData/` | ide | <span class="risk safe">safe</span> | Yes |
| **Workspace state of deleted folders**<br/><sub>`cursor-workspace-storage-orphans`</sub> | `…/Cursor/User/workspaceStorage/` | ide | <span class="risk moderate">moderate</span> once untouched for 30 days and without chat data; <span class="risk caution">caution</span> otherwise ("to review") | Only when moderate |
| **Workspace data of uninstalled extensions**<br/><sub>`cursor-workspace-dead-extension-data`</sub> | `…/workspaceStorage/*/<extension>` | ide | <span class="risk safe">safe</span> | Yes |
| **Superseded extension versions**<br/><sub>`cursor-extension-old-versions`</sub> | `~/.cursor/extensions/` | ide | <span class="risk moderate">moderate</span> | Yes |
| **Electron caches, logs, downloaded VSIX**<br/><sub>`ai-cursor-electron-caches`, `ai-cursor-logs`, `ai-cursor-vsix-cache`</sub> | `~/Library/Application Support/Cursor/` | ide | <span class="risk safe">safe</span> | Yes |
| **Local History (Timeline)** older than 180 days<br/><sub>`ai-cursor-local-history`</sub> | `…/Cursor/User/History/` | ide | <span class="risk caution">caution</span> | No |
| **Bundled cursor-agent versions**<br/><sub>`cursor-agent-bundled-old-version`</sub> | `…/globalStorage/anysphere.cursor-agent-worker/agent-cli/` | ai | <span class="risk safe">safe</span> | Yes |
| **Standalone `cursor-agent` and `origin` CLI versions**<br/><sub>`cursor-agent-old-version`, `cursor-origin-old-version`</sub> | `~/.local/share/cursor-agent/versions/`, `~/.local/share/cursor/origin/` | ai | <span class="risk safe">safe</span> | Yes |
| **Agent data of deleted folders**<br/><sub>`cursor-agent-orphan-projects`</sub> | `~/.cursor/projects/` | ai | <span class="risk moderate">moderate</span> once untouched for 30 days, when Cursor recorded the folder; <span class="risk caution">caution</span> otherwise ("to review") | Only when moderate |
| **Agent MCP descriptor caches**<br/><sub>`cursor-agent-mcp-caches`</sub> | `~/.cursor/projects/*/mcps` | ai | <span class="risk safe">safe</span> | Yes |
| **Agent transcripts** older than 30 days<br/><sub>`cursor-agent-old-transcripts`</sub> | `~/.cursor/projects/*/agent-transcripts`… | ai | <span class="risk caution">caution</span> | No |
| **State database backups** older than 30 days<br/><sub>`ai-cursor-state-db-backups`</sub> | `…/globalStorage/state.vscdb.backup*` | ai | <span class="risk caution">caution</span>: the only restore points of your chats | No |

**Old builds and versions.** Cursor creates a `CachedData/<commit>` folder for each update, but only uses the commit of the installed `Cursor.app`, which lu-cleaner reads from the app bundle. The bundled `cursor-agent` keeps every build it downloads (200 MB to 600 MB each). lu-cleaner keeps only the build its `bin/cursor-agent` symlink targets and the newest one.

**Workspace state of deleted folders.** Each folder or worktree you open gets an entry in `workspaceStorage`. lu-cleaner reads the folder from its `workspace.json` and proposes the entry once that folder verifiably no longer exists. Remote workspaces are ignored. Entries untouched for 30 days and without chat data form a recommended item. The others (written recently, holding chat history, or of a Conductor workspace that can be restored) go into a separate "to review" item that is never recommended. The agent data in `~/.cursor/projects` follows the same split. Just before deletion, lu-cleaner checks again that the folders have not come back.

**Extension data.** Data of uninstalled extensions (language-server indexes can take gigabytes) is only proposed when lu-cleaner can read the built-in extension list from `Cursor.app`. Without it, lu-cleaner cannot tell that an extension is gone.

:::caution[Your chats are never deleted]
`User/globalStorage/state.vscdb` holds every Cursor chat, your settings state and your login. It is protected and only reported, with the <span class="risk never">never</span> risk. To shrink it, delete old chats inside Cursor. The file only gets smaller after a SQLite `VACUUM` run while Cursor is closed. lu-cleaner never does that for you.
:::

## Other AI tools

| Tool | What lu-cleaner proposes | Risk |
|---|---|---|
| **ChatGPT desktop** | Conversation cache (re-synced from the server), HTTP caches | <span class="risk moderate">moderate</span>, <span class="risk safe">safe</span> |
| **ChatGPT Atlas** | Its whole data folder, only when the app is no longer installed | <span class="risk caution">caution</span> |
| **Conductor** | Archived workspace contexts older than 30 days, per repository (notes, plans, often large APKs and recordings) | <span class="risk caution">caution</span> |
| | Old bundled Claude Code and Codex binaries (keeps the version `bin/claude` / `bin/codex` targets) | <span class="risk safe">safe</span> |
| | `conductor.db` (sessions and messages) and `cache.db` (unsent drafts): report only | <span class="risk never">never</span>, <span class="risk caution">caution</span> |
| **Multica** | The `codex-home` copy of each task completed 7+ days ago. Task workdirs (repository checkouts) are never touched. | <span class="risk moderate">moderate</span> |
| **GitHub Copilot CLI, vibe-kanban** | Superseded versions | <span class="risk safe">safe</span> |
| **Antigravity** | Extensions left after uninstalling the app (moderate). The browser-agent profile left after uninstalling (saved passwords, cookies) and browser recordings older than 90 days (caution, never recommended). Conversations and "brain" are report only. | <span class="risk moderate">moderate</span>, <span class="risk caution">caution</span> |
| **opencode, Continue, Windsurf, Raycast, Grok CLI, chrome-devtools MCP** | Caches, indexes, logs, downloaded updates and installers. Sessions and chats stay (report only). | <span class="risk safe">safe</span> |
| **VoiceInk** | Dictation recordings older than 30 days (transcripts stay in the app) | <span class="risk caution">caution</span> |
| **Gemini CLI, Warp** | `~/.gemini/tmp` (checkpoints) and Warp's database: report only | <span class="risk caution">caution</span>, <span class="risk never">never</span> |

Run `lu-cleaner catalog -c ai` for the exact paths, or see the [catalog](/lu-cleaner/reference/catalog/).

## Local models

Model weights are deliberate multi-gigabyte downloads, so each one is its own item with the <span class="risk caution">caution</span> risk. They are never recommended, and `clean --yes` ignores them unless you pass `--risk caution`.

| Store | Location | Item |
|---|---|---|
| **Ollama** | `~/.ollama/models` or `$OLLAMA_MODELS` | One per model tag. Layers shared with another model are kept. |
| **LM Studio** | `~/.lmstudio/models`, `~/.cache/lm-studio/models` | One per model folder |
| **Hugging Face** | `~/.cache/huggingface/hub`, `$HF_HUB_CACHE` or `$HF_HOME/hub` | One per model, dataset or space (all revisions) |
| **Whisper** | `~/.cache/whisper`, VoiceInk's `WhisperModels` | One per model file |

```bash
lu-cleaner scan -k ollama-model,lmstudio-model,huggingface-model
lu-cleaner clean -c ai          # then press / and type "model" to filter
```

A model store that is a symlink to another disk is reported, not proposed: deleting it would not free any space on your internal disk. The Ollama identity key (`~/.ollama/id_ed25519`) is protected.

## What is always protected

The [safety guard](/lu-cleaner/concepts/safety/) refuses these paths, anything inside them and any folder that contains them, whatever a scanner or catalog entry proposes:

| Tool | Protected |
|---|---|
| **Claude Code** | `~/.claude.json`, `.credentials.json`, `settings.json`, `settings.local.json`, `CLAUDE.md`, `skills/`, `agents/`, `commands/`, `hooks/`, `rules/`, installed plugins and marketplaces, `history.jsonl`, `sessions/`, every project's `memory/` |
| **Claude desktop** | `claude_desktop_config.json`, `config.json`, `git-worktrees.json` |
| **Codex** | `auth.json`, `config.toml`, `AGENTS.md`, `memories/`, `skills/`, `rules/`, `automations/`, `history.jsonl`, `session_index.jsonl`, the global state file and its backup, the state, goals and memories databases |
| **Cursor** | `~/.cursor/mcp.json`, `rules/`, `skills/`, `User/settings.json`, `keybindings.json`, `snippets/`, `globalStorage/state.vscdb` (all chats), `storage.json` |
| **Others** | Ollama identity key, Gemini CLI credentials and settings, `~/.multica`, Conductor settings |

The tool folders themselves (`~/.claude`, `~/.codex`, `~/.cursor`, `~/.conductor`, `~/conductor`) can never be removed as a whole. To protect more, add paths to `protect` in your configuration:

```toml
# ~/.config/lu-cleaner/config.toml
protect = ["~/.codex/sessions", "~/.ollama/models"]
```

These entries are literal paths, not patterns: `~` and `$VARS` are expanded, and case and Unicode normalization do not matter, as on APFS.

## Related

- [Clean up AI agent worktrees](/lu-cleaner/guides/ai-worktrees/)
- [Risk levels and smart select](/lu-cleaner/concepts/risk-and-smart-select/)
- [What gets scanned](/lu-cleaner/reference/scanners/)
- [`lu-cleaner clean` reference](/lu-cleaner/reference/commands/clean/)
