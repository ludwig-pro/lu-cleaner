---
title: Nettoyer les données des outils d'IA
description: Récupérez l'espace qu'accumulent Claude Code, Claude desktop, Codex, Cursor et les autres outils d'IA (anciennes transcriptions, données de worktrees supprimés, versions remplacées, caches, modèles locaux) sans toucher aux identifiants, réglages et mémoires.
sidebar:
  order: 6
---

Les outils de développement assistés par IA écrivent beaucoup dans votre dossier personnel, et la plupart ne font jamais le ménage derrière eux :

- **Les transcriptions** de chaque session, souvent avec des captures d'écran et des sorties d'outils, conservées des semaines, voire indéfiniment.
- **Les données par projet**, indexées par chemin de dossier. Chaque worktree créé par un agent reçoit sa propre entrée, et cette entrée reste après la suppression du worktree.
- **Les anciennes versions.** Les mécanismes de mise à jour automatique téléchargent un nouveau build (de 200 Mo à 600 Mo) et conservent les précédents.
- **Les caches, journaux et bases de données** qui ne font que grossir : caches Electron, journaux de débogage, base de journaux de Codex jamais purgée.
- **Les modèles locaux** (Ollama, LM Studio, Hugging Face) téléchargés une fois pour essayer.

lu-cleaner les trouve grâce à un scanner dédié (le fournisseur `ai`) et aux emplacements fixes du [catalogue](/lu-cleaner/reference/catalog/). Il distingue ce qui peut être supprimé sans risque de ce qui constitue vos données, et ne propose jamais les identifiants, les réglages, les mémoires ni les bases de conversations.

:::note
Cette page traite des données que les outils conservent dans votre dossier personnel. Les worktrees git créés par les agents sont traités dans [Nettoyer les worktrees des agents IA](/lu-cleaner/fr/guides/ai-worktrees/).
:::

## Voir ce qu'occupent vos outils d'IA

```bash
lu-cleaner scan -c ai          # données des outils d'IA
lu-cleaner scan -c ai,ide      # plus la partie éditeur de Cursor (anciens builds, état des espaces de travail, extensions)
lu-cleaner catalog -c ai       # les chemins fixes vérifiés par lu-cleaner, avec leur risque
```

Cursor est réparti sur deux [catégories](/lu-cleaner/fr/concepts/categories/) : ses données d'agent sont dans `ai`, et ses caches d'éditeur (la partie commune avec VS Code) dans `ide`.

```text frame="terminal" title="lu-cleaner scan -c ai"
AI tools  21.4 GB · 36 items · ★ 6.90 GB recommended
  NAME                                                                    SIZE  AGE  RISK      RECO  PATH
  Ollama model · llama3.1:8b                                           4.92 GB  4mo  caution         ~/.ollama/models
  Codex sessions > 30d · 2026-07 (184 rollouts)                        3.40 GB  2mo  caution         ~/.codex/sessions/2026/07/…
  Claude Code sessions > 30d · ~/dev/my-app (96 sessions)              2.10 GB   5w  caution         ~/.claude/projects/-Users-me-dev-my-app/…
  Codex app runtime (node / python / native deps)                      1.60 GB   3w  moderate  ★     ~/.cache/codex-runtimes/…
  Claude Code data of deleted folder · ~/.codex/worktrees/a1b2/my-app  1.20 GB   3w  moderate  ★     ~/.claude/projects/-Users-me--codex-worktrees-a1b2-my-app/…
  Codex logs database (logs_2.sqlite)                                  1.10 GB  now  safe      ★     ~/.codex/logs_2.sqlite
  Cursor · bundled cursor-agent · 2026.08.31-4057e58 (old version)      610 MB   6w  safe      ★     ~/Library/Application Support/Cursor/User/globalStorage/…
  …
```

Les tailles et les noms ci-dessus sont donnés à titre d'exemple. La colonne `★` correspond à la [sélection intelligente](/lu-cleaner/fr/concepts/risk-and-smart-select/) (smart select) : les éléments que lu-cleaner présélectionnerait.

## Nettoyer

Le sélecteur interactif est le meilleur moyen de passer cette catégorie en revue, car une bonne partie relève d'un choix personnel :

```bash
lu-cleaner clean -c ai
```

Les éléments recommandés sont présélectionnés. Les éléments de risque <span class="risk caution">caution</span> (transcriptions, modèles, images générées) ne sont jamais présélectionnés, et leur nettoyage vous demande de taper `yes`.

Dans un script, commencez par une simulation :

```bash
lu-cleaner clean --yes --smart -c ai --dry-run   # le plan, rien n'est supprimé
lu-cleaner clean --yes --smart -c ai             # nettoie les éléments recommandés
```

`clean --yes` écarte les éléments <span class="risk caution">caution</span>, sauf si vous passez `--risk caution`. Pour supprimer délibérément les anciennes transcriptions de Claude Code, indiquez leur type d'élément :

```bash
lu-cleaner clean --yes -k claude-code-old-sessions --risk caution --dry-run
```

:::tip[Quittez d'abord les applications]
De nombreux éléments sont protégés par l'application à laquelle ils appartiennent : Cursor, Codex, l'application ChatGPT, Claude desktop, Conductor, Ollama, LM Studio. Tant que l'application tourne, l'élément porte un avertissement (« Cursor is running — quit it before cleaning »), la sélection intelligente l'ignore et le nettoyage le saute. `lu-cleaner doctor` liste les applications en cours d'exécution qui bloquent un nettoyage.
:::

### Le bon ordre : les worktrees d'abord

Claude Code et Cursor conservent des données par dossier. lu-cleaner ne peut qualifier ces données d'orphelines qu'une fois le dossier disparu. Supprimez d'abord les worktrees d'agents terminés, puis relancez un scan. Leurs restes apparaissent alors comme éléments « data of deleted folder » et sont présélectionnés :

```bash
lu-cleaner worktrees        # supprimer les worktrees terminés
lu-cleaner clean -c ai      # leurs données Claude Code / Cursor apparaissent désormais comme orphelines
```

## Claude Code

| Quoi | Où | Risque | Sélection intelligente |
|---|---|---|---|
| **Sessions de plus de 30 jours**, un élément par projet<br/><sub>`claude-code-old-sessions`</sub> | `~/.claude/projects/<project>/` | <span class="risk caution">caution</span> | Non |
| **Données d'un dossier supprimé** (projet orphelin)<br/><sub>`claude-code-orphan-project`</sub> | `~/.claude/projects/<project>/` | <span class="risk moderate">moderate</span> | Oui, sauf si le dossier a été déduit du nom du répertoire |
| **Anciennes versions de la CLI**<br/><sub>`claude-code-old-version`</sub> | `~/.local/share/claude/versions/` | <span class="risk moderate">moderate</span> | Oui |
| **Journaux de débogage et instantanés du shell** (plus d'un jour)<br/><sub>`ai-claude-code-debug-logs`</sub> | `~/.claude/debug/`, `~/.claude/shell-snapshots/` | <span class="risk safe">safe</span> | Oui |
| **Télémétrie, cache des feature flags, changelog**<br/><sub>`ai-claude-code-telemetry`</sub> | `~/.claude/telemetry/`, `~/.claude/statsig/` | <span class="risk safe">safe</span> | Oui |
| **Dossiers temporaires et listes de tâches par session** (plus de 7 jours)<br/><sub>`ai-claude-code-session-temp`, `ai-claude-code-todos`</sub> | `~/.claude/session-env/`, `~/.claude/tasks/`, `~/.claude/todos/` | <span class="risk safe">safe</span> | Oui |
| **Journaux des serveurs MCP**<br/><sub>`ai-claude-code-mcp-logs`</sub> | `~/Library/Caches/claude-cli-nodejs/` | <span class="risk safe">safe</span> | Oui |
| **Cache des collages** (plus de 30 jours)<br/><sub>`ai-claude-code-paste-cache`</sub> | `~/.claude/paste-cache/` | <span class="risk moderate">moderate</span> | Si inactif |
| **Sauvegardes de `~/.claude.json`** (toutes sauf la plus récente, plus de 7 jours)<br/><sub>`claude-code-config-backups`</sub> | `~/.claude/backups/`, `~/` | <span class="risk moderate">moderate</span> | Si inactif |
| **Points de contrôle de fichiers utilisés par `/rewind`** (plus de 30 jours)<br/><sub>`ai-claude-code-file-history`</sub> | `~/.claude/file-history/` | <span class="risk caution">caution</span> | Non |
| **Fichiers de plan** (plus de 60 jours)<br/><sub>`ai-claude-code-plans`</sub> | `~/.claude/plans/` | <span class="risk caution">caution</span> | Non |

« Si inactif » signifie que l'élément est présélectionné dès qu'il n'a pas été utilisé depuis plus de `stale_after`, soit 14 jours par défaut. Voir [Configuration](/lu-cleaner/fr/reference/configuration/).

**Anciennes sessions.** Claude Code stocke une transcription par session (`<id>.jsonl`), avec à côté un dossier pour les journaux des sous-agents et les sorties d'outils. lu-cleaner regroupe, pour chaque projet, les sessions qui n'ont pas été modifiées depuis 30 jours. Il ignore toute session indiquée comme active dans `~/.claude/sessions/`. Supprimer d'anciennes sessions les retire de `claude --resume` et de l'historique. Claude Code purge aussi lui-même ses transcriptions au-delà de `cleanupPeriodDays` (30 jours par défaut). Si vous avez augmenté ce réglage, lu-cleaner vous laisse choisir les projets à alléger.

**Anciennes versions de la CLI.** L'installateur natif conserve chaque build téléchargé (environ 200 Mo chacun). lu-cleaner garde la version vers laquelle pointe `~/.local/bin/claude`, la plus récente (le mécanisme de mise à jour l'a peut-être déjà téléchargée) et toute version utilisée par un processus en cours. Pour conserver davantage de versions, réglez `keep_latest`.

### Comment les projets orphelins sont détectés

Claude Code nomme chaque dossier de projet d'après le répertoire de travail, en remplaçant par `-` chaque caractère qui n'est ni une lettre ni un chiffre. Par exemple, `~/.codex/worktrees/a1b2/my-app` devient `-Users-me--codex-worktrees-a1b2-my-app`. Ce nom ne peut pas être décodé de façon fiable, donc lu-cleaner ne commence pas par l'analyser :

1. Il lit le vrai `cwd` dans les transcriptions les plus récentes, ou dans `sessions-index.json`.
2. Si aucune de ces sources n'est disponible, il cherche sur le disque un dossier dont le nom encodé correspond. Un tel élément indique que le dossier a été *déduit du nom du répertoire* et n'est jamais présélectionné.
3. Un dossier situé sur un volume non monté est considéré comme « inconnu », jamais comme « supprimé ».

Un projet ne devient orphelin que si son dossier n'existe plus, qu'aucune session active n'y tourne et que rien n'y a été écrit au cours de la dernière heure. Sa mémoire automatique (`memory/`) est toujours conservée : lu-cleaner supprime les transcriptions qui l'entourent, jamais le dossier. Juste avant la suppression, il vérifie de nouveau que le dossier est toujours absent et qu'aucune session n'y a démarré.

## Claude desktop

| Quoi | Où | Risque | Sélection intelligente |
|---|---|---|---|
| **Image de la VM Cowork** (`rootfs.img`, `initrd`, `vmlinuz`)<br/><sub>`ai-claude-desktop-vm-image`</sub> | `~/Library/Application Support/Claude/vm_bundles/claudevm.bundle/` | <span class="risk moderate">moderate</span> | Si inactif |
| **Anciens builds de Claude Code embarqués**<br/><sub>`claude-desktop-old-claude-code`, `claude-desktop-old-claude-code-vm`</sub> | `…/Claude/claude-code/`, `…/Claude/claude-code-vm/` | <span class="risk safe">safe</span> | Oui |
| **Caches Electron** (HTTP, code, GPU, mise à jour)<br/><sub>`ai-claude-desktop-caches`</sub> | `…/Claude/Cache`, `…/Claude/Code Cache`… | <span class="risk safe">safe</span> | Oui |
| **Disque de session de la VM Cowork** (`sessiondata.img`) | `…/claudevm.bundle/` | <span class="risk caution">caution</span> | Signalé uniquement |
| **État des sessions Agent et Code, dépôts git fantômes** | `…/Claude/local-agent-mode-sessions`… | <span class="risk caution">caution</span> | Signalé uniquement |

L'image de la VM est un disque Linux creux (sparse) d'environ 10 Go alloués. Claude desktop la télécharge de nouveau au prochain démarrage de Cowork. Le disque de session, qui contient les fichiers de vos sessions Cowork, est seulement signalé. Pour les builds de Claude Code embarqués, lu-cleaner conserve la version la plus récente et celle indiquée dans `claude-code-vm/.sdk-version`. L'image de la VM et les caches attendent que l'application Claude soit fermée.

## Codex (CLI et application)

| Quoi | Où | Risque | Sélection intelligente |
|---|---|---|---|
| **Sessions de plus de 30 jours**, un élément par mois<br/><sub>`codex-old-sessions`</sub> | `~/.codex/sessions/YYYY/MM/` | <span class="risk caution">caution</span> | Non |
| **Sessions archivées**, un élément par mois<br/><sub>`codex-archived-sessions`</sub> | `~/.codex/archived_sessions/` | <span class="risk caution">caution</span> | Non |
| **Base de journaux**<br/><sub>`codex-logs-db`</sub> | `~/.codex/logs_*.sqlite` (+ `-wal`, `-shm`) | <span class="risk safe">safe</span> | Oui, quand Codex est fermé |
| **Copie obsolète de la base de journaux**<br/><sub>`codex-stale-logs-db`</sub> | `~/.codex/sqlite/logs_*.sqlite` | <span class="risk safe">safe</span> | Oui |
| **Sauvegardes de réparation de base** (plus de 7 jours)<br/><sub>`ai-codex-repair-backups`</sub> | `~/.codex/*.codex-repair-*.bak` | <span class="risk safe">safe</span> | Oui |
| **Environnement d'exécution de l'application** (node, python, dépendances natives)<br/><sub>`ai-codex-runtimes`</sub> | `~/.cache/codex-runtimes/` | <span class="risk moderate">moderate</span> | Si inactif |
| **Caches de plugins et clones de synchronisation**<br/><sub>`ai-codex-plugin-cache`, `ai-codex-plugin-staging`</sub> | `~/.codex/plugins/cache`, `~/.codex/.tmp/` | <span class="risk moderate">moderate</span> | Si inactif |
| **Caches de catalogue, instantanés du shell, caches Chromium de l'application**<br/><sub>`ai-codex-caches`, `ai-codex-shell-snapshots`, `ai-codex-app-caches`</sub> | `~/.codex/cache/`, `~/.codex/shell_snapshots/`, `~/Library/Application Support/Codex/` | <span class="risk safe">safe</span> | Oui |
| **Fichiers journaux de la CLI** (plus de 14 jours)<br/><sub>`ai-codex-log-files`</sub> | `~/.codex/log/` | <span class="risk moderate">moderate</span> | Si inactif |
| **Images et visualisations générées** (plus de 30 jours)<br/><sub>`ai-codex-generated-images`, `codex-visualizations`</sub> | `~/.codex/generated_images/`, `~/.codex/visualizations/` | <span class="risk caution">caution</span> | Non |
| **Base d'historique des fils, anciennes copies des bases d'état et de mémoires, archives de PR, pièces jointes, `~/Documents/Codex`** | `~/.codex/…` | <span class="risk caution">caution</span> | Signalé uniquement |

**Sessions.** Un seul fichier de rollout peut atteindre plusieurs centaines de Mo lorsqu'il contient des captures d'écran. Le supprimer rend impossible la reprise du fil correspondant. Les fils épinglés sont toujours conservés : lu-cleaner en lit la liste dans la base d'état de Codex avec une requête `sqlite3` en lecture seule.

**Base de journaux.** `logs_*.sqlite` ne contient que des journaux de traçage et de retours. Codex ne la purge ni ne la compacte jamais, et en recrée une vide au démarrage suivant. Elle est supprimée avec ses fichiers `-wal` et `-shm`, elle ne doit donc pas être ouverte. L'élément attend que `codex`, l'application Codex **et l'application ChatGPT** soient fermés (ChatGPT intègre Codex). Juste avant la suppression, lu-cleaner vérifie aussi avec `lsof` qu'aucun processus n'a le fichier ouvert.

## Cursor

| Quoi | Où | Catégorie | Risque | Sélection intelligente |
|---|---|---|---|---|
| **CachedData des anciens builds de l'application**<br/><sub>`cursor-cached-data-old-builds`</sub> | `~/Library/Application Support/Cursor/CachedData/` | ide | <span class="risk safe">safe</span> | Oui |
| **État des espaces de travail de dossiers supprimés**<br/><sub>`cursor-workspace-storage-orphans`</sub> | `…/Cursor/User/workspaceStorage/` | ide | <span class="risk moderate">moderate</span> | Oui |
| **Données d'espace de travail d'extensions désinstallées**<br/><sub>`cursor-workspace-dead-extension-data`</sub> | `…/workspaceStorage/*/<extension>` | ide | <span class="risk safe">safe</span> | Oui |
| **Versions d'extensions remplacées**<br/><sub>`cursor-extension-old-versions`</sub> | `~/.cursor/extensions/` | ide | <span class="risk moderate">moderate</span> | Oui |
| **Caches Electron, journaux, VSIX téléchargés**<br/><sub>`ai-cursor-electron-caches`, `ai-cursor-logs`, `ai-cursor-vsix-cache`</sub> | `~/Library/Application Support/Cursor/` | ide | <span class="risk safe">safe</span> | Oui |
| **Historique local (Timeline)** de plus de 180 jours<br/><sub>`ai-cursor-local-history`</sub> | `…/Cursor/User/History/` | ide | <span class="risk caution">caution</span> | Non |
| **Versions de cursor-agent embarquées**<br/><sub>`cursor-agent-bundled-old-version`</sub> | `…/globalStorage/anysphere.cursor-agent-worker/agent-cli/` | ai | <span class="risk safe">safe</span> | Oui |
| **Versions autonomes des CLI `cursor-agent` et `origin`**<br/><sub>`cursor-agent-old-version`, `cursor-origin-old-version`</sub> | `~/.local/share/cursor-agent/versions/`, `~/.local/share/cursor/origin/` | ai | <span class="risk safe">safe</span> | Oui |
| **Données d'agent de dossiers supprimés**<br/><sub>`cursor-agent-orphan-projects`</sub> | `~/.cursor/projects/` | ai | <span class="risk moderate">moderate</span> | Oui |
| **Caches des descripteurs MCP de l'agent**<br/><sub>`cursor-agent-mcp-caches`</sub> | `~/.cursor/projects/*/mcps` | ai | <span class="risk safe">safe</span> | Oui |
| **Transcriptions de l'agent** de plus de 30 jours<br/><sub>`cursor-agent-old-transcripts`</sub> | `~/.cursor/projects/*/agent-transcripts`… | ai | <span class="risk caution">caution</span> | Non |
| **Sauvegardes de la base d'état** de plus de 30 jours<br/><sub>`ai-cursor-state-db-backups`</sub> | `…/globalStorage/state.vscdb.backup*` | ai | <span class="risk moderate">moderate</span> | Si inactif |

**Anciens builds et versions.** Cursor crée un dossier `CachedData/<commit>` à chaque mise à jour, mais n'utilise que le commit du `Cursor.app` installé, que lu-cleaner lit dans le bundle de l'application. Le `cursor-agent` embarqué conserve chaque build qu'il télécharge (de 200 Mo à 600 Mo chacun). lu-cleaner ne garde que le build ciblé par son lien symbolique `bin/cursor-agent` et le plus récent.

**État des espaces de travail de dossiers supprimés.** Chaque dossier ou worktree que vous ouvrez reçoit une entrée dans `workspaceStorage`. lu-cleaner lit le dossier dans son `workspace.json` et propose l'entrée dès que ce dossier n'existe plus. Les espaces de travail distants sont ignorés. Juste avant la suppression, il vérifie de nouveau que les dossiers ne sont pas réapparus.

**Données d'extensions.** Les données des extensions désinstallées (les index des serveurs de langage peuvent peser plusieurs gigaoctets) ne sont proposées que si lu-cleaner parvient à lire la liste des extensions intégrées dans `Cursor.app`. Sans elle, lu-cleaner ne peut pas savoir qu'une extension a disparu.

:::caution[Vos conversations ne sont jamais supprimées]
`User/globalStorage/state.vscdb` contient toutes les conversations Cursor, l'état de vos réglages et votre connexion. Il est protégé et seulement signalé, avec le risque <span class="risk never">never</span>. Pour le réduire, supprimez d'anciennes conversations dans Cursor. Le fichier ne rétrécit qu'après un `VACUUM` SQLite exécuté pendant que Cursor est fermé. lu-cleaner ne le fait jamais à votre place.
:::

## Autres outils d'IA

| Outil | Ce que lu-cleaner propose | Risque |
|---|---|---|
| **ChatGPT desktop** | Cache des conversations (resynchronisé depuis le serveur), caches HTTP | <span class="risk moderate">moderate</span>, <span class="risk safe">safe</span> |
| **ChatGPT Atlas** | Tout son dossier de données, uniquement quand l'application n'est plus installée | <span class="risk caution">caution</span> |
| **Conductor** | Contextes d'espaces de travail archivés de plus de 30 jours, par dépôt (notes, plans, souvent de gros APK et enregistrements) | <span class="risk caution">caution</span> |
| | Anciens binaires Claude Code et Codex embarqués (conserve la version ciblée par `bin/claude` / `bin/codex`) | <span class="risk safe">safe</span> |
| | `conductor.db` (sessions et messages) et `cache.db` (brouillons non envoyés) : signalés uniquement | <span class="risk never">never</span>, <span class="risk caution">caution</span> |
| **Multica** | La copie `codex-home` de chaque tâche terminée depuis 7 jours ou plus. Les dossiers de travail des tâches (copies des dépôts) ne sont jamais touchés. | <span class="risk moderate">moderate</span> |
| **GitHub Copilot CLI, vibe-kanban** | Versions remplacées | <span class="risk safe">safe</span> |
| **Antigravity** | Profil de navigateur et extensions laissés après la désinstallation de l'application ; enregistrements du navigateur de plus de 90 jours. Les conversations et le « brain » sont seulement signalés. | <span class="risk moderate">moderate</span>, <span class="risk caution">caution</span> |
| **opencode, Continue, Windsurf, Raycast, Grok CLI, chrome-devtools MCP** | Caches, index, journaux, mises à jour et installateurs téléchargés. Les sessions et conversations restent (signalées uniquement). | <span class="risk safe">safe</span> |
| **VoiceInk** | Enregistrements de dictée de plus de 30 jours (les transcriptions restent dans l'application) | <span class="risk caution">caution</span> |
| **Gemini CLI, Warp** | `~/.gemini/tmp` (points de contrôle) et la base de données de Warp : signalés uniquement | <span class="risk caution">caution</span>, <span class="risk never">never</span> |

Lancez `lu-cleaner catalog -c ai` pour obtenir les chemins exacts, ou consultez le [catalogue](/lu-cleaner/reference/catalog/).

## Modèles locaux

Les poids de modèles sont des téléchargements volontaires de plusieurs gigaoctets, donc chacun constitue un élément distinct, avec le risque <span class="risk caution">caution</span>. Ils ne sont jamais présélectionnés, et `clean --yes` les ignore sauf si vous passez `--risk caution`.

| Stockage | Emplacement | Élément |
|---|---|---|
| **Ollama** | `~/.ollama/models` ou `$OLLAMA_MODELS` | Un par tag de modèle. Les couches partagées avec un autre modèle sont conservées. |
| **LM Studio** | `~/.lmstudio/models`, `~/.cache/lm-studio/models` | Un par dossier de modèle |
| **Hugging Face** | `~/.cache/huggingface/hub`, `$HF_HUB_CACHE` ou `$HF_HOME/hub` | Un par modèle, dataset ou space (toutes révisions confondues) |
| **Whisper** | `~/.cache/whisper`, `WhisperModels` de VoiceInk | Un par fichier de modèle |

```bash
lu-cleaner scan -k ollama-model,lmstudio-model,huggingface-model
lu-cleaner clean -c ai          # puis appuyez sur / et tapez "model" pour filtrer
```

Un stockage de modèles qui est un lien symbolique vers un autre disque est signalé, pas proposé : le supprimer ne libérerait aucun espace sur votre disque interne. La clé d'identité d'Ollama (`~/.ollama/id_ed25519`) est protégée.

## Ce qui est toujours protégé

Le [garde-fou de sécurité](/lu-cleaner/fr/concepts/safety/) refuse ces chemins, tout ce qu'ils contiennent et tout dossier qui les contient, quoi que propose un scanner ou une entrée du catalogue :

| Outil | Protégé |
|---|---|
| **Claude Code** | `~/.claude.json`, `.credentials.json`, `settings.json`, `settings.local.json`, `CLAUDE.md`, `skills/`, `agents/`, `commands/`, `hooks/`, `rules/`, plugins et marketplaces installés, `history.jsonl`, `sessions/`, le `memory/` de chaque projet |
| **Claude desktop** | `claude_desktop_config.json`, `config.json`, `git-worktrees.json` |
| **Codex** | `auth.json`, `config.toml`, `AGENTS.md`, `memories/`, `skills/`, `rules/`, `automations/`, `history.jsonl`, `session_index.jsonl`, le fichier d'état global et sa sauvegarde, les bases d'état, d'objectifs et de mémoires |
| **Cursor** | `~/.cursor/mcp.json`, `rules/`, `skills/`, `User/settings.json`, `keybindings.json`, `snippets/`, `globalStorage/state.vscdb` (toutes les conversations), `storage.json` |
| **Autres** | Clé d'identité d'Ollama, identifiants et réglages de Gemini CLI, `~/.multica`, réglages de Conductor |

Les dossiers des outils eux-mêmes (`~/.claude`, `~/.codex`, `~/.cursor`, `~/.conductor`, `~/conductor`) ne peuvent jamais être supprimés en bloc. Pour protéger davantage, ajoutez des chemins à `protect` dans votre configuration :

```toml
# ~/.config/lu-cleaner/config.toml
protect = ["~/.codex/sessions", "~/.ollama/models"]
```

## Voir aussi

- [Nettoyer les worktrees des agents IA](/lu-cleaner/fr/guides/ai-worktrees/)
- [Niveaux de risque et sélection intelligente](/lu-cleaner/fr/concepts/risk-and-smart-select/)
- [Ce qui est analysé](/lu-cleaner/fr/reference/scanners/)
- [Référence de `lu-cleaner clean`](/lu-cleaner/reference/commands/clean/)
