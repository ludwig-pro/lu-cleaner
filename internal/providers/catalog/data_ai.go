package catalog

import (
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// AI tools domain: static, well-known locations of Claude Code, Claude
// desktop, Codex, Cursor, ChatGPT, Conductor and other AI tools.
//
// Everything that needs logic lives in internal/providers/aitools: Claude Code
// transcripts per project (~/.claude/projects) and ~/.claude.json backups,
// versioned binaries (~/.local/share/claude/versions, Claude desktop
// claude-code*/, cursor-agent, Conductor agent-binaries...), Codex
// sessions / archived_sessions / visualizations per month, Codex logs DBs,
// Multica task homes, Cursor CachedData / workspaceStorage / extensions /
// ~/.cursor/projects, Conductor archived contexts, ChatGPT Atlas and
// Antigravity leftovers, local models (Ollama, LM Studio, Hugging Face,
// Whisper). Never list those paths here.
//
// Credentials, configs, memories, skills, rules, live state DBs are
// protected by safety.Guard and are never matched below.

// ProcessGuard patterns (sysx.Running: plain names match an executable
// basename exactly, case-sensitively; patterns with "/" match a substring of
// the executable path). ChatGPT.app embeds Codex.
var (
	aiProcClaude    = []string{"Claude"}
	aiProcCodex     = []string{"codex", "Codex", "ChatGPT", "codex-code-mode-host"}
	aiProcCursor    = []string{"Cursor"}
	aiProcChatGPT   = []string{"ChatGPT", "ChatGPT Classic"}
	aiProcWindsurf  = []string{"Windsurf", "/Windsurf.app/"}
	aiProcRaycast   = []string{"Raycast"}
	aiProcVoiceInk  = []string{"VoiceInk"}
	aiProcConductor = []string{"conductor", "Conductor"}
)

const aiDay = 24 * time.Hour

func init() {
	const (
		claudeApp = "~/Library/Application Support/Claude"
		cursorApp = "~/Library/Application Support/Cursor"
		codexApp  = "~/Library/Application Support/Codex"
	)
	add(
		// ------------------------------------------------------------ Claude Code
		Entry{
			ID: "ai-claude-code-debug-logs", Category: core.CatAI,
			Name:  "Claude Code debug logs & shell snapshots",
			Paths: []string{"~/.claude/debug/*.txt", "~/.claude/shell-snapshots/*"},
			Files: true, OlderThan: aiDay,
			Risk: core.RiskSafe, Recommended: true,
			Note: "Per-session debug logs and shell environment snapshots of past Claude Code sessions; recreated for each new session.",
		},
		Entry{
			ID: "ai-claude-code-telemetry", Category: core.CatAI,
			Name:  "Claude Code telemetry & changelog cache",
			Paths: []string{"~/.claude/telemetry/*", "~/.claude/statsig/*", "~/.claude/cache/changelog.md"},
			Files: true, OlderThan: aiDay,
			Risk: core.RiskSafe,
			Note: "Failed telemetry batches, feature-flag cache and the cached changelog; re-fetched automatically.",
		},
		Entry{
			ID: "ai-claude-code-session-temp", Category: core.CatAI,
			Name:      "Claude Code per-session temp dirs",
			Paths:     []string{"~/.claude/session-env/*", "~/.claude/tasks/*", "~/.claude/plugins/.trash/*"},
			OlderThan: 7 * aiDay,
			Risk:      core.RiskSafe,
			Note:      "Per-session environment / task scratch dirs and removed plugins of sessions older than a week; recreated per session.",
		},
		Entry{
			ID: "ai-claude-code-todos", Category: core.CatAI,
			Name:  "Claude Code old todo lists",
			Paths: []string{"~/.claude/todos/*"},
			Files: true, OlderThan: 7 * aiDay,
			Risk: core.RiskSafe,
			Note: "Todo lists of sessions older than a week; recreated per session.",
		},
		Entry{
			ID: "ai-claude-code-paste-cache", Category: core.CatAI,
			Name:  "Claude Code paste cache",
			Paths: []string{"~/.claude/paste-cache/*"},
			Files: true, OlderThan: 30 * aiDay,
			Risk: core.RiskModerate,
			Note: "Large pasted texts referenced by the prompt history; losing them only affects re-showing old pastes.",
		},
		Entry{
			ID: "ai-claude-code-file-history", Category: core.CatAI,
			Name:      "Claude Code file checkpoints (rewind) > 30d",
			Paths:     []string{"~/.claude/file-history/*"},
			OlderThan: 30 * aiDay,
			Risk:      core.RiskCaution,
			Note:      "File snapshots backing /rewind for sessions older than 30 days; those sessions can no longer restore files.",
		},
		Entry{
			ID: "ai-claude-code-plans", Category: core.CatAI,
			Name:  "Claude Code plan files > 60d",
			Paths: []string{"~/.claude/plans/*.md"},
			Files: true, OlderThan: 60 * aiDay,
			Risk: core.RiskCaution,
			Note: "Plans written in plan mode by old sessions; not regenerated.",
		},
		Entry{
			ID: "ai-claude-code-mcp-logs", Category: core.CatAI,
			Name:      "Claude Code MCP logs cache",
			Paths:     []string{"~/Library/Caches/claude-cli-nodejs/*"},
			OlderThan: aiDay,
			Risk:      core.RiskSafe,
			Note:      "Per-project MCP server logs written by the Claude Code CLI; recreated.",
		},

		// ------------------------------------------------------------ Claude desktop
		Entry{
			ID: "ai-claude-desktop-vm-image", Category: core.CatAI,
			Name: "Claude desktop Cowork VM image",
			Paths: []string{
				claudeApp + "/vm_bundles/claudevm.bundle/rootfs.img",
				claudeApp + "/vm_bundles/claudevm.bundle/initrd",
				claudeApp + "/vm_bundles/claudevm.bundle/initrd-micro",
				claudeApp + "/vm_bundles/claudevm.bundle/vmlinuz",
			},
			Files: true, Risk: core.RiskModerate, ProcessGuard: aiProcClaude,
			Note: "Linux VM disk image (sparse, ~10 GB allocated) used by Cowork / local agent mode; Claude desktop re-downloads it the next time Cowork starts. The session disk is kept.",
		},
		Entry{
			ID: "ai-claude-desktop-vm-sessiondata", Category: core.CatAI,
			Name:  "Claude desktop Cowork VM session disk",
			Paths: []string{claudeApp + "/vm_bundles/claudevm.bundle/sessiondata.img"},
			Files: true, Risk: core.RiskCaution, Method: core.MethodReport,
			Note: "Disk holding files of Cowork sessions inside the VM; kept (report only).",
		},
		Entry{
			ID: "ai-claude-desktop-caches", Category: core.CatAI,
			Name: "Claude desktop Electron caches",
			Paths: []string{
				claudeApp + "/Cache", claudeApp + "/Code Cache", claudeApp + "/GPUCache",
				claudeApp + "/DawnCache", claudeApp + "/DawnGraphiteCache", claudeApp + "/DawnWebGPUCache",
				claudeApp + "/GraphiteDawnCache", claudeApp + "/GrShaderCache", claudeApp + "/Shared Dictionary",
				claudeApp + "/Partitions/*/Cache", claudeApp + "/Partitions/*/Code Cache",
				claudeApp + "/Partitions/*/GPUCache", claudeApp + "/Partitions/*/Shared Dictionary",
				"~/Library/Caches/com.anthropic.claudefordesktop", "~/Library/Caches/com.anthropic.claudefordesktop.ShipIt",
			},
			Risk: core.RiskSafe, ProcessGuard: aiProcClaude,
			Note: "Chromium HTTP / code / GPU caches and the updater cache of the Claude desktop app; rebuilt on next launch. Cookies, Local Storage and IndexedDB are kept.",
		},
		Entry{
			ID: "ai-claude-desktop-sessions", Category: core.CatAI,
			Name: "Claude desktop agent & Code session state",
			Paths: []string{
				claudeApp + "/local-agent-mode-sessions", claudeApp + "/claude-code-sessions", claudeApp + "/git-shadow",
			},
			Risk: core.RiskCaution, Method: core.MethodReport,
			Note: "Session state of Cowork / Code sessions and git shadow repos used by the desktop app; kept (report only).",
		},

		// ------------------------------------------------------------ Codex
		// Repair backups are pre-repair copies: only the logs DB ones are pure
		// waste. Those of memories_* / goals_* (and state_*, protected by the
		// guard) are the only way back if a repair dropped rows.
		Entry{
			ID: "ai-codex-repair-backups", Category: core.CatAI,
			Name:  "Codex logs DB repair backups",
			Paths: []string{"~/.codex/logs_*.codex-repair-*.bak", "~/.codex/sqlite/logs_*.codex-repair-*.bak"},
			Files: true, OlderThan: 7 * aiDay,
			Risk: core.RiskSafe, Recommended: true,
			Note: "One-off copies of the Codex logs database made while repairing it (logs_*.codex-repair-<epoch>.bak); never read again.",
		},
		Entry{
			ID: "ai-codex-repair-backups-data", Category: core.CatAI,
			Name:    "Codex DB repair backups (memories, goals…)",
			Paths:   []string{"~/.codex/*.codex-repair-*.bak", "~/.codex/sqlite/*.codex-repair-*.bak"},
			Exclude: []string{"logs_*", "state_*"},
			Files:   true, OlderThan: 30 * aiDay,
			Risk: core.RiskCaution, ProcessGuard: aiProcCodex,
			Note: "Pre-repair copies of Codex databases holding your data (memories, goals…): the only way to recover rows a repair dropped (sqlite3 .recover). Kept unless you choose them.",
		},
		Entry{
			ID: "ai-codex-global-state-tmp", Category: core.CatAI,
			Name:  "Codex stale global-state temp files",
			Paths: []string{"~/.codex/..codex-global-state.json.tmp-*", "~/.codex/..codex-global-state.json.bak.tmp-*"},
			Files: true, OlderThan: aiDay,
			Risk: core.RiskSafe, Recommended: true, ProcessGuard: aiProcCodex,
			Note: "Leftovers of interrupted atomic writes of .codex-global-state.json (the real file and its .bak are kept).",
		},
		Entry{
			ID: "ai-codex-plugin-backups", Category: core.CatAI,
			Name:      "Codex abandoned plugin sync backups",
			Paths:     []string{"~/.codex/.tmp/plugins-backup-*"},
			OlderThan: 7 * aiDay,
			Risk:      core.RiskSafe, Recommended: true, ProcessGuard: aiProcCodex,
			Note: "Backups of the plugin repository clone taken during plugin syncs; never restored.",
		},
		Entry{
			ID: "ai-codex-plugin-staging", Category: core.CatAI,
			Name:  "Codex plugin sync clones",
			Paths: []string{"~/.codex/.tmp/plugins", "~/.codex/.tmp/bundled-marketplaces", "~/.codex/.tmp/marketplaces/.staging"},
			Risk:  core.RiskModerate, ProcessGuard: aiProcCodex,
			Note: "Plugin marketplace clones used by plugin syncs; re-cloned on the next sync (bandwidth).",
		},
		Entry{
			ID: "ai-codex-plugin-cache", Category: core.CatAI,
			Name:  "Codex plugin cache",
			Paths: []string{"~/.codex/plugins/cache"},
			Risk:  core.RiskModerate, ProcessGuard: append([]string{"ChatGPT for Chrome"}, aiProcCodex...),
			Note: "Downloaded plugin bundles (some run as helper processes); re-downloaded when a plugin is used. The active plugin app-server is kept.",
		},
		Entry{
			ID: "ai-codex-caches", Category: core.CatAI,
			Name: "Codex remote catalog & apps caches",
			Paths: []string{
				"~/.codex/cache/remote_plugin_catalog", "~/.codex/cache/codex_apps_tools",
				"~/.codex/cache/codex_apps_server_info", "~/.codex/cache/bundled_plugin_exclusions",
			},
			Risk: core.RiskSafe, ProcessGuard: aiProcCodex,
			Note: "Remote plugin catalog and apps tool caches; re-fetched automatically (the session index and app directory caches are kept).",
		},
		Entry{
			ID: "ai-codex-shell-snapshots", Category: core.CatAI,
			Name:  "Codex shell snapshots",
			Paths: []string{"~/.codex/shell_snapshots/*"},
			Files: true, OlderThan: 7 * aiDay,
			Risk: core.RiskSafe,
			Note: "Shell environment snapshots of past Codex sessions; recreated per session.",
		},
		Entry{
			ID: "ai-codex-log-files", Category: core.CatAI,
			Name:  "Codex CLI log files > 14d",
			Paths: []string{"~/.codex/log/*"},
			Files: true, OlderThan: 14 * aiDay,
			Risk: core.RiskModerate,
			Note: "Old Codex CLI text logs; only useful for bug reports.",
		},
		Entry{
			ID: "ai-codex-generated-images", Category: core.CatAI,
			Name:      "Codex generated images > 30d",
			Paths:     []string{"~/.codex/generated_images/*"},
			OlderThan: 30 * aiDay,
			Risk:      core.RiskCaution,
			Note:      "Images generated in Codex threads older than 30 days; not regenerated.",
		},
		Entry{
			ID: "ai-codex-runtimes", Category: core.CatAI,
			Name:  "Codex app runtime (node / python / native deps)",
			Paths: []string{"~/.cache/codex-runtimes/*"},
			Risk:  core.RiskModerate, ProcessGuard: aiProcCodex,
			Note: "Runtime bundle downloaded by the Codex app for its tools; re-downloaded (~1.6 GB) on next use.",
		},
		Entry{
			ID: "ai-codex-app-caches", Category: core.CatAI,
			Name: "Codex app Chromium caches",
			Paths: []string{
				codexApp + "/Cache", codexApp + "/Code Cache", codexApp + "/GPUCache", codexApp + "/GraphiteDawnCache",
				codexApp + "/DawnGraphiteCache", codexApp + "/DawnWebGPUCache", codexApp + "/GrShaderCache",
				codexApp + "/GPUPersistentCache", codexApp + "/component_crx_cache", codexApp + "/Crashpad/pending/*",
				"~/Library/Caches/Codex", "~/Library/Caches/com.openai.codex/org.sparkle-project.Sparkle/Installation",
			},
			Risk: core.RiskSafe, ProcessGuard: aiProcCodex,
			Note: "HTTP, code and GPU caches of the Codex / ChatGPT app's embedded Chromium; rebuilt on launch. The browser profile (Default/, cookies) is kept.",
		},
		Entry{
			ID: "ai-codex-thread-history-db", Category: core.CatAI,
			Name:  "Codex thread history DB",
			Paths: []string{"~/.codex/thread_history_*.sqlite"},
			Files: true, Risk: core.RiskCaution, Method: core.MethodReport,
			Note: "Projection of all Codex threads; shrinks only when threads are deleted in Codex (report only).",
		},
		Entry{
			ID: "ai-codex-old-sqlite-state", Category: core.CatAI,
			Name: "Codex old state / memories DB copies (sqlite/)",
			Paths: []string{
				"~/.codex/sqlite/state_*.sqlite", "~/.codex/sqlite/state_*.sqlite-wal", "~/.codex/sqlite/state_*.sqlite-shm",
				"~/.codex/sqlite/memories_*.sqlite", "~/.codex/sqlite/goals_*.sqlite",
			},
			Files: true, Risk: core.RiskCaution, Method: core.MethodReport,
			Note: "Older copies of Codex state and memories DBs in ~/.codex/sqlite; may hold memories, report only.",
		},
		Entry{
			ID: "ai-codex-user-content", Category: core.CatAI,
			Name:  "Codex PR archives, attachments & dictation history",
			Paths: []string{"~/.codex/archives", "~/.codex/attachments", "~/.codex/dictation-history"},
			Risk:  core.RiskCaution, Method: core.MethodReport,
			Note: "User content kept by Codex; report only.",
		},
		Entry{
			ID: "ai-codex-documents-workspaces", Category: core.CatAI,
			Name:  "Codex chat workspaces (~/Documents/Codex)",
			Paths: []string{"~/Documents/Codex/*"},
			Risk:  core.RiskCaution, Method: core.MethodReport,
			Note: "Working folders of project-less Codex chats; they hold user files (report only).",
		},

		// ------------------------------------------------------------ Cursor (editor part: ide)
		Entry{
			ID: "ai-cursor-electron-caches", Category: core.CatIDE,
			Name: "Cursor Electron caches",
			Paths: []string{
				cursorApp + "/Cache", cursorApp + "/Code Cache", cursorApp + "/GPUCache", cursorApp + "/DawnCache",
				cursorApp + "/DawnGraphiteCache", cursorApp + "/DawnWebGPUCache", cursorApp + "/GraphiteDawnCache",
				cursorApp + "/GrShaderCache", cursorApp + "/CachedProfilesData",
				cursorApp + "/Service Worker/CacheStorage", cursorApp + "/Service Worker/ScriptCache",
				cursorApp + "/Partitions/*/Cache", cursorApp + "/Partitions/*/Code Cache",
				cursorApp + "/Partitions/*/GPUCache", cursorApp + "/Partitions/*/DawnWebGPUCache",
				cursorApp + "/Partitions/*/DawnGraphiteCache", cursorApp + "/Partitions/*/Shared Dictionary",
				"~/Library/Caches/com.todesktop.230313mzl4w4u92", "~/Library/Caches/com.todesktop.230313mzl4w4u92.ShipIt",
			},
			Risk: core.RiskSafe, ProcessGuard: aiProcCursor,
			Note: "Chromium HTTP / code / GPU caches, the embedded browser's caches and the updater cache of Cursor; rebuilt on launch. Cookies, Local Storage and IndexedDB are kept.",
		},
		Entry{
			ID: "ai-cursor-webstorage-cache", Category: core.CatIDE,
			Name:  "Cursor webview CacheStorage",
			Paths: []string{cursorApp + "/WebStorage/*/CacheStorage"},
			Risk:  core.RiskSafe, ProcessGuard: aiProcCursor,
			Note: "Service-worker caches of Cursor webviews; repopulated on demand.",
		},
		Entry{
			ID: "ai-cursor-logs", Category: core.CatIDE,
			Name:  "Cursor logs",
			Paths: []string{cursorApp + "/logs/*"},
			Risk:  core.RiskSafe, ProcessGuard: aiProcCursor,
			Note: "One log folder per Cursor launch; only useful for bug reports.",
		},
		Entry{
			ID: "ai-cursor-vsix-cache", Category: core.CatIDE,
			Name:  "Cursor cached extension downloads (VSIX)",
			Paths: []string{cursorApp + "/CachedExtensionVSIXs/*"},
			Files: true, Risk: core.RiskSafe, ProcessGuard: aiProcCursor,
			Note: "Downloaded extension packages kept after install; re-downloaded on install or update.",
		},
		Entry{
			ID: "ai-cursor-local-history", Category: core.CatIDE,
			Name:      "Cursor Local History (Timeline) > 180d",
			Paths:     []string{cursorApp + "/User/History/*"},
			OlderThan: 180 * aiDay,
			Risk:      core.RiskCaution, ProcessGuard: aiProcCursor,
			Note: "Per-file edit snapshots behind the Timeline view, untouched for 6 months; they cannot be restored afterwards.",
		},

		// ------------------------------------------------------------ Cursor (agent part: ai)
		Entry{
			ID: "ai-cursor-state-db", Category: core.CatAI,
			Name:  "Cursor global state DB (all chats)",
			Paths: []string{cursorApp + "/User/globalStorage/state.vscdb"},
			Files: true, Risk: core.RiskNever, Method: core.MethodReport,
			Note: "Holds every Cursor agent / composer chat, settings state and auth; delete old chats in Cursor (the file only shrinks after a VACUUM with Cursor closed).",
		},
		// The only restore points of the DB holding every Cursor chat: user
		// data, never preselected (caution).
		Entry{
			ID: "ai-cursor-state-db-backups", Category: core.CatAI,
			Name:  "Cursor state DB backups",
			Paths: []string{cursorApp + "/User/globalStorage/state.vscdb.backup", cursorApp + "/User/globalStorage/state.vscdb.backup-*"},
			Files: true, OlderThan: 30 * aiDay,
			Risk: core.RiskCaution, ProcessGuard: aiProcCursor,
			Note: "Restore points of the chats/state DB (state.vscdb.backup is rewritten by Cursor, dated ones are one-off copies): the only way back to chats lost or corrupted since. Not regenerated.",
		},
		Entry{
			ID: "ai-cursor-agent-worker-logs", Category: core.CatAI,
			Name:  "Cursor agent worker logs",
			Paths: []string{cursorApp + "/User/globalStorage/anysphere.cursor-agent-worker/cursor-agent-worker-*.log"},
			Files: true, OlderThan: 7 * aiDay,
			Risk: core.RiskSafe,
			Note: "Logs of past Cursor background-agent worker runs.",
		},
		Entry{
			ID: "ai-cursor-agent-stores", Category: core.CatAI,
			Name:  "Cursor background agent stores",
			Paths: []string{cursorApp + "/AgentStores/cursor_agent_stores/*"},
			Risk:  core.RiskCaution, Method: core.MethodReport,
			Note: "State of Cursor background / cloud agents; kept (report only).",
		},
		Entry{
			ID: "ai-cursor-statsig-tmp", Category: core.CatAI,
			Name:  "Cursor stale statsig temp files",
			Paths: []string{"~/.cursor/statsig-cache.json.*.tmp"},
			Files: true, OlderThan: aiDay,
			Risk: core.RiskSafe, Recommended: true,
			Note: "Leftovers of interrupted writes of the feature-flag cache.",
		},

		// ------------------------------------------------------------ ChatGPT
		Entry{
			ID: "ai-chatgpt-conversations-cache", Category: core.CatAI,
			Name:  "ChatGPT desktop conversation cache",
			Paths: []string{"~/Library/Application Support/com.openai.chat/conversations-v3-*"},
			Risk:  core.RiskModerate, ProcessGuard: aiProcChatGPT,
			Note: "Local copy of ChatGPT conversations; re-synced from the server on demand.",
		},
		Entry{
			ID: "ai-chatgpt-caches", Category: core.CatAI,
			Name:  "ChatGPT desktop caches",
			Paths: []string{"~/Library/Caches/com.openai.chat", "~/Library/Caches/ChatGPTHelper"},
			Risk:  core.RiskSafe, ProcessGuard: aiProcChatGPT,
			Note: "HTTP and helper caches of the ChatGPT desktop app; rebuilt.",
		},

		// ------------------------------------------------------------ Conductor
		Entry{
			ID: "ai-conductor-db", Category: core.CatAI,
			Name:  "Conductor database (sessions & messages)",
			Paths: []string{"~/Library/Application Support/com.conductor.app/conductor.db"},
			Files: true, Risk: core.RiskNever, Method: core.MethodReport,
			Note: "Conductor sessions, messages, repos and settings; shrinks only by deleting sessions in Conductor.",
		},
		Entry{
			ID: "ai-conductor-cache-db", Category: core.CatAI,
			Name:  "Conductor client cache DB",
			Paths: []string{"~/Library/Application Support/com.conductor.app/cache.db"},
			Files: true, Risk: core.RiskCaution, Method: core.MethodReport, ProcessGuard: aiProcConductor,
			Note: "Message cache that also holds unsent drafts; report only.",
		},

		// ------------------------------------------------------------ other AI tools
		Entry{
			ID: "ai-chrome-devtools-mcp-cache", Category: core.CatAI,
			Name: "chrome-devtools-mcp browser caches",
			Paths: []string{
				"~/.cache/chrome-devtools-mcp/chrome-profile/Default/Cache",
				"~/.cache/chrome-devtools-mcp/chrome-profile/Default/Code Cache",
				"~/.cache/chrome-devtools-mcp/chrome-profile/Default/Service Worker/CacheStorage",
				"~/.cache/chrome-devtools-mcp/chrome-profile/GraphiteDawnCache",
				"~/.cache/chrome-devtools-mcp/chrome-profile/GrShaderCache",
			},
			Risk: core.RiskSafe,
			Note: "HTTP / code caches of the Chrome profile driven by the chrome-devtools MCP server; rebuilt (the profile's logins are kept).",
		},
		Entry{
			ID: "ai-gemini-cli-tmp", Category: core.CatAI,
			Name:  "Gemini CLI checkpoints & chat state",
			Paths: []string{"~/.gemini/tmp"},
			Risk:  core.RiskCaution, Method: core.MethodReport,
			Note: "Despite its name, Gemini CLI keeps conversation checkpoints here; report only.",
		},
		Entry{
			ID: "ai-warp-db", Category: core.CatAI,
			Name:  "Warp database (history, blocks, AI conversations)",
			Paths: []string{"~/Library/Group Containers/2BBY89MBSN.dev.warp/Library/Application Support/dev.warp.Warp-Stable/warp.sqlite*"},
			Files: true, Risk: core.RiskNever, Method: core.MethodReport,
			Note: "Warp terminal history and Agent Mode conversations; never deleted.",
		},
		Entry{
			ID: "ai-raycast-updates", Category: core.CatAI,
			Name:      "Raycast downloaded updates",
			Paths:     []string{"~/Library/Application Support/com.raycast.macos/Updates/*"},
			OlderThan: aiDay,
			Risk:      core.RiskSafe, ProcessGuard: aiProcRaycast,
			Note: "Update packages downloaded by Raycast's updater; re-downloaded if needed. Raycast's encrypted DBs and extensions are untouched.",
		},
		Entry{
			ID: "ai-voiceink-recordings", Category: core.CatAI,
			Name:  "VoiceInk dictation recordings > 30d",
			Paths: []string{"~/Library/Application Support/com.prakashjoshipax.VoiceInk/Recordings/*"},
			Files: true, OlderThan: 30 * aiDay,
			Risk: core.RiskCaution, ProcessGuard: aiProcVoiceInk,
			Note: "Audio of past dictations (the transcripts stay in VoiceInk's history); not recoverable.",
		},
		Entry{
			ID: "ai-grok-downloads", Category: core.CatAI,
			Name:  "Grok CLI installer downloads",
			Paths: []string{"~/.grok/downloads/*"},
			Files: true, Risk: core.RiskSafe, Recommended: true,
			Note: "Installer binaries kept after installing the Grok CLI (the installed ~/.grok/bin is kept).",
		},
		Entry{
			ID: "ai-opencode-cache", Category: core.CatAI,
			Name:  "opencode cache & logs",
			Paths: []string{"~/.cache/opencode", "~/.local/share/opencode/log"},
			Risk:  core.RiskSafe,
			Note:  "opencode package cache and logs; recreated (sessions in storage/ are kept).",
		},
		Entry{
			ID: "ai-opencode-sessions", Category: core.CatAI,
			Name:  "opencode sessions",
			Paths: []string{"~/.local/share/opencode/storage"},
			Risk:  core.RiskCaution, Method: core.MethodReport,
			Note: "opencode session history; report only.",
		},
		Entry{
			ID: "ai-continue-index", Category: core.CatAI,
			Name:  "Continue codebase index",
			Paths: []string{"~/.continue/index"},
			Risk:  core.RiskSafe,
			Note:  "Embeddings index of the Continue extension; rebuilt on next indexing (config.* is kept).",
		},
		Entry{
			ID: "ai-codeium-windsurf-data", Category: core.CatAI,
			Name:  "Windsurf / Codeium Cascade data",
			Paths: []string{"~/.codeium/windsurf"},
			Risk:  core.RiskCaution, Method: core.MethodReport,
			Note: "Cascade chats and memories of Windsurf; report only.",
		},
		Entry{
			ID: "ai-windsurf-caches", Category: core.CatIDE,
			Name: "Windsurf editor caches",
			Paths: []string{
				"~/Library/Application Support/Windsurf/Cache", "~/Library/Application Support/Windsurf/Code Cache",
				"~/Library/Application Support/Windsurf/GPUCache", "~/Library/Application Support/Windsurf/CachedData",
				"~/Library/Application Support/Windsurf/CachedExtensionVSIXs", "~/Library/Application Support/Windsurf/logs",
			},
			Risk: core.RiskSafe, ProcessGuard: aiProcWindsurf,
			Note: "Electron caches, old-build code caches, VSIX downloads and logs of Windsurf; rebuilt on launch.",
		},
	)
}
