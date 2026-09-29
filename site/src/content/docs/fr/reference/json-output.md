---
title: Sortie JSON
description: Schémas de scan --json, clean --yes --json, doctor --json et history --json, avec des exemples anonymisés, les codes de sortie et des notes de stabilité pour les scripts.
sidebar:
  order: 5
---

Toutes les commandes qui produisent un rapport acceptent `--json`. Utilisez-le dans les scripts, les jobs de CI et les agents d'IA plutôt que d'analyser les tableaux, qui sont destinés aux humains et changent avec la largeur du terminal.

```bash
lu-cleaner scan --json | jq '.totals'
lu-cleaner clean --yes --smart --dry-run --json | jq '.results | length'
```

## Règles générales

- Le document JSON est écrit sur **stdout**, indenté. Les lignes de progression, les avertissements des scanners (providers) et la sortie de débogage de `--verbose` vont sur **stderr** : `2>/dev/null` est donc sans danger.
- **Les tailles** sont des entiers en octets. Les tailles lisibles par un humain n'apparaissent que dans les chaînes de `meta` et de `note`, en unités décimales (`1.34 GB` = 1 340 000 000 octets).
- **Les dates** sont des chaînes RFC 3339. `generated_at` est en UTC ; `last_used` garde le décalage horaire local.
- **Les durées** sont en millisecondes quand le nom du champ se termine par `_ms` ; les champs `took` de `clean` sont en nanosecondes.
- **Le risque** vaut `safe`, `moderate`, `caution` ou `never`. **La méthode** vaut `delete`, `trash`, `command`, `worktree` ou `report`.
- Les champs optionnels sont omis quand ils sont vides, sauf mention contraire.

## `scan --json`

Le rapport d'analyse. Le même document est produit par `lu-cleaner --json` sans commande, et par `artifacts`, `worktrees` et `devices` avec `--json` (restreint à leurs catégories).

```bash
lu-cleaner scan --json                    # tous les éléments
lu-cleaner scan --json --top 5            # au plus 5 éléments par catégorie
lu-cleaner scan --json --smart            # éléments recommandés uniquement
lu-cleaner scan --json -c worktrees,artifacts --older-than 30d
```

Sans `--top`, le JSON liste **tous** les éléments qui passent les filtres (le rapport texte en affiche 15 par catégorie). Comme dans le rapport texte, les éléments plus petits que `min_size` (1 Mo par défaut) sont masqués, sauf si vous passez `--min-size 0`, et les éléments en rapport seul sont inclus.

### Champs de premier niveau

| Champ | Type | Description |
|---|---|---|
| `version` | string | Version de lu-cleaner qui a produit le document |
| `generated_at` | string | Heure de l'analyse, en UTC, à la seconde près |
| `disk` | object | Volume personnel : `total`, `free` (disponible pour vous), `used`, en octets |
| `items` | array | Éléments, groupés par catégorie dans l'ordre d'affichage, triés par `--sort` (la taille par défaut) au sein de chaque catégorie |
| `totals.size` | integer | Ce que libérerait le nettoyage de tous les éléments nettoyables correspondants (tailles réellement libérées, chemins imbriqués comptés une fois) |
| `totals.recommended` | integer | La même chose pour les éléments recommandés |
| `totals.items` | integer | Nombre d'éléments nettoyables |
| `totals.by_category` | object | Octets libérables par identifiant de catégorie. Les valeurs se recouvrent quand des éléments d'une catégorie se trouvent dans des éléments d'une autre (artefacts dans des worktrees) |
| `totals.shared_by_category` | object | Pour chaque catégorie concernée par un tel recouvrement, la part de ses octets située dans des éléments d'une autre catégorie. `size` compte ces octets une seule fois. Toujours présent, `{}` s'il n'y en a pas |
| `errors` | object | Identifiant de scanner → message d'erreur, pour les scanners qui ont échoué. Toujours présent, `{}` s'il n'y en a aucun |
| `timing_ms` | object | Identifiant de scanner → durée d'analyse en millisecondes |
| `took_ms` | integer | Durée totale de l'analyse en millisecondes |

Les totaux sont calculés sur tous les éléments correspondants, même quand `--top` raccourcit `items`.

### Champs d'un élément

| Champ | Type | Description |
|---|---|---|
| `id` | string | Identifiant stable, généralement `<scanner>:<kind>:<path>` |
| `provider` | string | Identifiant du scanner (`worktrees`, `artifacts`, `apple`, `android`, `ai`, `js`, `system`, `catalog`) |
| `category`, `category_title` | string | Identifiant et titre affiché de la catégorie |
| `kind` | string | Type d'élément, tel qu'accepté par `--kind`. Voir [Ce qui est analysé](/lu-cleaner/fr/reference/scanners/) |
| `name` | string | Libellé lisible |
| `path` | string | Le chemin supprimé, pour les éléments à chemin unique |
| `paths` | array | Les chemins supprimés, pour les éléments groupés (`path` est alors absent) |
| `location` | string | Où agit un élément groupé ou un élément commande, pour l'affichage |
| `size` | integer | Octets alloués, liens physiques comptés une fois |
| `reclaim` | integer | Octets réellement libérés, présent seulement quand c'est moins que `size` (données partagées avec des fichiers extérieurs à l'élément par des liens physiques ou des clones APFS) |
| `freed` | integer | Meilleure estimation des octets libérés : `reclaim` s'il est présent, sinon `size`. Toujours présent |
| `files` | integer | Nombre de fichiers |
| `last_used` | string | Meilleure estimation de la dernière activité |
| `age_days` | number ou null | Jours écoulés depuis `last_used`, à une décimale ; `null` quand la date est inconnue |
| `risk` | string | `safe`, `moderate`, `caution` ou `never` |
| `method` | string | `delete`, `trash`, `command`, `worktree` ou `report` |
| `command` | array | Commande exécutée par les éléments `command`, sous forme d'argv |
| `post_commands` | array | Commandes exécutées après l'action principale (tableau d'argv) |
| `process_guard` | array | Processus qui ne doivent pas tourner pendant le nettoyage |
| `require_sibling` | array | Fichiers marqueurs qui doivent toujours exister à côté du chemin |
| `project` | string | Projet ou dépôt principal auquel appartient l'élément |
| `meta` | object | Détails propres au scanner ; toutes les valeurs sont des chaînes |
| `note` | string | Ce qu'est l'élément et comment il revient |
| `warn` | string | Avertissement ; un élément qui porte un avertissement n'est jamais recommandé, et `clean --yes` le retient sauf avec `--risk caution` |
| `require_force` | boolean | `true` quand le nettoyage refuse l'élément sans `--force`, en simulation aussi (dossiers de worktrees orphelins). Omis sinon |
| `recommended` | boolean | Sélectionné par la sélection intelligente. Toujours présent |
| `cleanable` | boolean | `false` pour les éléments en rapport seul, non sélectionnables et `never`. Toujours présent |

### Exemple

Abrégé et anonymisé : trois éléments tirés d'une analyse complète.

```json
{
  "version": "v0.1.0",
  "generated_at": "2026-09-28T09:12:44Z",
  "disk": { "total": 500000000000, "free": 43000000000, "used": 457000000000 },
  "items": [
    {
      "id": "worktrees:worktree:/Users/me/.codex/worktrees/a1b2/my-app",
      "provider": "worktrees",
      "category": "worktrees",
      "kind": "codex-worktree",
      "name": "my-app · feature/login",
      "path": "/Users/me/.codex/worktrees/a1b2/my-app",
      "size": 2310000000,
      "files": 187000,
      "risk": "moderate",
      "method": "worktree",
      "project": "/Users/me/dev/my-app",
      "meta": {
        "artifacts": "1.95 GB",
        "branch": "feature/login",
        "checkout": "360 MB",
        "dirty": "0",
        "ios/Pods": "610 MB",
        "locked": "false",
        "main": "/Users/me/dev/my-app",
        "merged": "true",
        "node_modules": "1.34 GB",
        "status": "merged",
        "tool": "codex",
        "unpushed": "0"
      },
      "note": "Codex worktree: git worktree remove deletes the checkout and keeps branch feature/login in my-app; Codex threads using it lose their folder (archiving them in Codex snapshots first).",
      "last_used": "2026-09-19T17:40:02+02:00",
      "category_title": "Worktrees",
      "age_days": 8.6,
      "recommended": true,
      "cleanable": true,
      "freed": 2310000000
    },
    {
      "id": "artifacts:node_modules:/Users/me/dev/shop-app/node_modules",
      "provider": "artifacts",
      "category": "artifacts",
      "kind": "node_modules",
      "name": "shop-app › node_modules",
      "path": "/Users/me/dev/shop-app/node_modules",
      "size": 1210000000,
      "reclaim": 84000000,
      "files": 98000,
      "risk": "moderate",
      "method": "delete",
      "require_sibling": ["package.json"],
      "project": "/Users/me/dev/shop-app",
      "meta": {
        "git": "ignored",
        "package_manager": "pnpm",
        "project_type": "expo",
        "reclaim": "84.0 MB"
      },
      "note": "Installed JS dependencies; reinstall with `pnpm i` when you need the project again (minutes, network). Hardlinked with the pnpm store: only 84.0 MB is really freed.",
      "last_used": "2026-07-02T10:03:11+02:00",
      "category_title": "Project artifacts",
      "age_days": 88.1,
      "recommended": true,
      "cleanable": true,
      "freed": 84000000
    },
    {
      "id": "apple:ios-simulator:6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B",
      "provider": "apple",
      "category": "simulators",
      "kind": "ios-simulator",
      "name": "iPhone 16 Pro · iOS 18.6",
      "location": "/Users/me/Library/Developer/CoreSimulator/Devices/6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B",
      "size": 3400000000,
      "files": 41000,
      "risk": "caution",
      "method": "command",
      "command": ["xcrun", "simctl", "delete", "6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B"],
      "meta": {
        "apps": "com.example.myapp",
        "device_type": "iPhone 16 Pro",
        "runtime": "iOS 18.6",
        "state": "Shutdown",
        "udid": "6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B"
      },
      "note": "Simulator with 1 installed app(s) and their data (com.example.myapp): deleting it loses app data, logins and settings — reinstall the dev build afterwards. …",
      "last_used": "2026-08-30T11:22:05+02:00",
      "category_title": "iOS Simulators",
      "age_days": 29,
      "recommended": false,
      "cleanable": true,
      "freed": 3400000000
    }
  ],
  "totals": {
    "size": 61200000000,
    "recommended": 23400000000,
    "items": 214,
    "by_category": { "artifacts": 18300000000, "simulators": 21500000000, "worktrees": 21400000000 },
    "shared_by_category": { "artifacts": 1280000000 }
  },
  "errors": {},
  "timing_ms": { "apple": 5120, "artifacts": 9870, "worktrees": 11240 },
  "took_ms": 11251
}
```

Requêtes utiles :

```bash
# éléments recommandés, les plus gros d'abord
lu-cleaner scan --json --smart | jq -r '.items | sort_by(-.freed)[] | "\(.freed)\t\(.kind)\t\(.name)"'

# worktrees dont le statut est clean ou merged
lu-cleaner scan --json -c worktrees | jq '.items[] | select(.meta.status == "clean" or .meta.status == "merged") | .path'

# ce que chaque catégorie pourrait libérer
lu-cleaner scan --json | jq '.totals.by_category'
```

## `clean --yes --json`

Le résultat d'un nettoyage non interactif ou d'une simulation. `--json` exige `--yes` (le sélecteur interactif n'a pas de sortie JSON). `artifacts`, `worktrees` et `devices` produisent le même document avec `--yes --json`. Les éléments retenus à cause d'un avertissement ne figurent pas dans le document : une ligne `warning: N items with a warning left out (…)` est écrite sur stderr à la place.

```bash
lu-cleaner clean --yes --smart --dry-run --json
lu-cleaner clean --yes -c artifacts --older-than 30d --json
```

### Champs du récapitulatif

| Champ | Type | Description |
|---|---|---|
| `results` | array | Une entrée par élément traité, dans l'ordre de fin de traitement. `[]` quand rien ne correspond |
| `estimated_freed` | integer | Espace réellement libéré : somme de `freed` sur les résultats `done`, plus ce que les suppressions en échec ont libéré avant d'échouer. Les déplacements dans la Corbeille ne sont jamais comptés. Toujours `0` en simulation |
| `trashed` | integer | Octets déplacés dans la Corbeille par les résultats `done` : toujours occupés tant que la Corbeille n'est pas vidée. Toujours présent |
| `disk_before`, `disk_after` | object | `total`, `free` et `used` du volume personnel avant et après l'exécution |
| `measured_freed` | integer | `disk_after.free − disk_before.free` : ce que rapporte le système de fichiers. Peut être inférieur à l'estimation (snapshots, Corbeille, fichiers ouverts) et n'est que du bruit en simulation |
| `trash` | boolean | Mode Corbeille : les fichiers et dossiers ont été déplacés dans la Corbeille, les éléments de type worktree et commande ont été ignorés |
| `dry_run` | boolean | Rien n'a été exécuté |
| `took` | integer | Durée totale, en nanosecondes |

### Champs d'un résultat

| Champ | Type | Description |
|---|---|---|
| `item` | object | L'élément, avec les mêmes champs que dans `scan --json`, sauf les champs calculés (`category_title`, `age_days`, `cleanable`, `freed`). `recommended` n'apparaît que lorsque le scanner l'a forcé |
| `status` | string | `done`, `dry-run`, `skipped` ou `failed` |
| `method` | string | Méthode réellement utilisée, ou qui le serait en simulation : `trash` pour un élément `delete` nettoyé en mode Corbeille |
| `freed` | integer | Octets réellement libérés estimés pour les résultats `done` et `dry-run`, ce qu'une suppression `failed` a libéré avant d'échouer, `0` sinon. Un déplacement dans la Corbeille ne libère rien : sa taille figure dans `trashed` |
| `trashed` | integer | Octets déplacés dans la Corbeille (ou qui le seraient, en simulation). Omis quand il vaut `0` |
| `message` | string | Ce qui s'est passé (`would delete …`, `would run: xcrun simctl delete …`, `would move to Trash: …`, `moved to …`, `git worktree removed (branch kept)`, `orphaned worktree directory removed`) ou la raison pour laquelle l'élément a été ignoré (`needs --force: …`, `not possible in Trash mode (it would delete permanently)`…) |
| `error` | string | Raison de l'échec, pour les résultats `failed` |
| `took` | integer | Durée, en nanosecondes |

### Exemple

`lu-cleaner clean --yes -c js,xcode --risk caution --dry-run --json` avec Xcode ouvert, abrégé à deux résultats. Sans `--risk caution`, l'élément DerivedData, qui porte un avertissement, serait retenu au lieu d'être ignoré :

```json
{
  "results": [
    {
      "item": {
        "id": "catalog:js-npm-cache",
        "provider": "catalog",
        "category": "js",
        "kind": "js-npm-cache",
        "name": "npm cache (_cacache)",
        "path": "/Users/me/.npm/_cacache",
        "size": 1140000000,
        "files": 23000,
        "last_used": "2026-09-28T10:41:12+02:00",
        "risk": "safe",
        "method": "delete",
        "note": "npm's content cache; the next `npm install` / `npx` re-downloads what it needs. Existing node_modules are unaffected."
      },
      "status": "dry-run",
      "method": "delete",
      "freed": 1140000000,
      "message": "would delete /Users/me/.npm/_cacache",
      "took": 8167084
    },
    {
      "item": {
        "id": "apple:xcode-derived-data:/Users/me/Library/Developer/Xcode/DerivedData/MyApp-abcdefghijklmnopqrstuvwxyzab",
        "provider": "apple",
        "category": "xcode",
        "kind": "xcode-derived-data",
        "name": "MyApp",
        "path": "/Users/me/Library/Developer/Xcode/DerivedData/MyApp-abcdefghijklmnopqrstuvwxyzab",
        "size": 4800000000,
        "risk": "safe",
        "method": "delete",
        "process_guard": ["Xcode", "xcodebuild"],
        "warn": "Xcode is running — quit it before cleaning"
      },
      "status": "skipped",
      "method": "delete",
      "freed": 0,
      "message": "Xcode is running — quit it first (or use --force)",
      "took": 2140
    }
  ],
  "estimated_freed": 0,
  "trashed": 0,
  "disk_before": { "total": 500000000000, "free": 43000000000, "used": 457000000000 },
  "disk_after": { "total": 500000000000, "free": 42999920000, "used": 457000080000 },
  "measured_freed": -80000,
  "trash": false,
  "dry_run": true,
  "took": 383662875
}
```

En simulation, additionnez les valeurs `freed` des résultats pour obtenir ce que l'exécution libérerait (et `trashed` pour ce qu'elle déplacerait dans la Corbeille) :

```bash
lu-cleaner clean --yes --smart --dry-run --json \
  | jq '[.results[] | select(.status == "dry-run") | .freed] | add'
```

## `doctor --json`

La santé du disque et les raisons pour lesquelles l'espace peut ne pas revenir.

```bash
lu-cleaner doctor --json            # inclut un résumé de l'analyse par catégorie
lu-cleaner doctor --json --no-scan  # disque, snapshots, Corbeille et applications en cours d'exécution uniquement
```

| Champ | Type | Description |
|---|---|---|
| `version`, `generated_at` | string | Comme dans `scan --json` |
| `disk` | object | `total`, `free` et `used` du volume personnel |
| `used_pct` | number | Pourcentage utilisé, à une décimale |
| `snapshots` | array | Noms des snapshots locaux APFS (Time Machine) du volume racine |
| `trash` | object | `path`, `size`, `files`, `readable` (`false` sans Accès complet au disque), `note` facultatif |
| `running` | array | Applications qui bloquent certains nettoyages : `app`, `processes`, `impact` |
| `scanned` | boolean | `false` avec `--no-scan` |
| `categories` | array | Par catégorie : `id`, `title`, `size`, `recommended`, `items`, et `shared` (octets situés dans des éléments d'une autre catégorie) quand il ne vaut pas `0`. Vide avec `--no-scan` |
| `total`, `recommended` | integer | Octets libérables, au total et pour les éléments recommandés |
| `errors` | object | Erreurs des scanners, s'il y en a |
| `use_trash` | boolean | Réglage `use_trash` de la configuration |
| `tips` | array | Explications des raisons pour lesquelles l'espace libéré peut ne pas apparaître |

```json
{
  "version": "v0.1.0",
  "generated_at": "2026-09-28T09:20:03Z",
  "disk": { "total": 500000000000, "free": 43000000000, "used": 457000000000 },
  "used_pct": 91.4,
  "snapshots": [],
  "trash": { "path": "/Users/me/.Trash", "size": 0, "files": 0, "readable": false, "note": "cannot read the Trash (grant Full Disk Access to your terminal)" },
  "running": [
    { "app": "Simulator", "processes": ["Simulator", "launchd_sim"], "impact": "booted simulators and runtimes cannot be deleted" }
  ],
  "scanned": false,
  "categories": [],
  "total": 0,
  "recommended": 0,
  "use_trash": false,
  "tips": ["APFS local snapshots pin the blocks of deleted files until they expire (about 24h) or are thinned.", "…"]
}
```

## `history --json`

Ce qu'ont fait les nettoyages passés, lu depuis `~/.local/state/lu-cleaner/history.jsonl`.

```bash
lu-cleaner history --json             # les 30 dernières entrées
lu-cleaner history --json --limit 0   # toutes les entrées
```

| Champ | Type | Description |
|---|---|---|
| `path` | string | Le fichier d'historique |
| `total` | integer | Nombre d'entrées dans le fichier |
| `freed` | integer | Somme de `freed` sur toutes les entrées du fichier (pas seulement celles affichées) : l'espace réellement libéré. Les déplacements dans la Corbeille ne sont pas comptés |
| `trashed` | integer | Somme de `size` sur les déplacements dans la Corbeille `done` : toujours occupés tant que la Corbeille n'est pas vidée |
| `entries` | array | Les plus récentes d'abord, au plus `--limit` (30 par défaut, `0` = toutes) |

Chaque entrée contient ces champs :

| Champ | Description |
|---|---|
| `time`, `kind`, `category`, `name` | Quand, et quel élément |
| `path` | Le chemin supprimé, pour les éléments à un seul chemin |
| `location`, `paths`, `count` | Pour un élément groupé : son emplacement affiché, ses 50 premiers chemins et le nombre total de chemins |
| `command` | La commande exécutée, pour les éléments commande |
| `method` | Méthode réellement utilisée : `trash` quand le mode Corbeille a transformé une suppression en déplacement |
| `status` | `done`, `skipped` ou `failed` ; les simulations ne sont jamais enregistrées |
| `size` | La taille mesurée de l'élément |
| `freed` | Octets réellement libérés : moins que `size` quand des données sont partagées par des liens physiques ou des clones APFS, ce qu'une suppression en échec a libéré avant d'échouer, `0` pour les déplacements dans la Corbeille, les éléments ignorés et les autres échecs. Toujours présent |
| `error`, `message` | Raison de l'échec, et ce qui s'est passé ou la raison pour laquelle l'élément a été ignoré |

```json
{
  "entries": [
    {
      "time": "2026-09-27T18:46:23.836594+02:00",
      "kind": "ios-simulator",
      "category": "simulators",
      "name": "iPad Air 11-inch (M2) · iOS 18.6",
      "command": "xcrun simctl delete A1B2C3D4-0000-4000-8000-000000000001",
      "method": "command",
      "status": "done",
      "size": 18341888,
      "freed": 18341888
    },
    {
      "time": "2026-09-27T18:46:20.112004+02:00",
      "kind": "node_modules",
      "category": "artifacts",
      "name": "shop-app › node_modules",
      "path": "/Users/me/dev/shop-app/node_modules",
      "method": "delete",
      "status": "done",
      "size": 1210000000,
      "freed": 84000000
    }
  ],
  "freed": 37100000000,
  "path": "/Users/me/.local/state/lu-cleaner/history.jsonl",
  "total": 88,
  "trashed": 0
}
```

Le fichier lui-même est au format JSON Lines, avec le même format d'entrée et les plus anciennes d'abord, si bien que vous pouvez aussi le lire directement : `jq -s 'map(select(.status == "done")) | length' ~/.local/state/lu-cleaner/history.jsonl`. Les lignes écrites par des versions plus anciennes n'ont pas de champ `freed` : `history` suppose alors la taille entière pour une suppression définitive `done`, et `0` sinon.

## Autres commandes

| Commande | Sortie |
|---|---|
| `catalog --json` | Tableau des entrées du catalogue : `id`, `category`, `name`, `paths`, `risk`, `method`, `mode` (`group` ou `each`) et, quand ils sont définis, `exclude`, `command`, `requires`, `process_guard`, `note`, `older_than`, `keep_latest`, `allow_git_repo`, `recommended`, `min_bytes`, `files` |
| `config show --json` | Configuration effective : `path`, `exists`, `config` (les valeurs du fichier), `roots` résolues et `roots_source`, `worktree_roots`, `exclude`, `protect`, `missing` (les entrées `exclude` et `protect` qui n'existent pas), `stale_after`, `min_size` (en octets), `disabled_categories`, `state_dir`, `history_file`, `clean` (`trash`, `force`) |
| `analyze [path] --json` | `root`, `total` et `entries` (chaque enfant direct : `name`, `path`, `dir`, `size`, `files`, `unreadable`) |
| `version --json` | `version`, `go`, `os`, `arch` |

## Codes de sortie

| Code | Signification |
|---|---|
| `0` | Succès. Les éléments ignorés pour des raisons de sécurité ne changent pas le code de sortie ; consultez `results[].status`. |
| `1` | Échec à l'exécution : fichier de configuration invalide, erreur d'entrée/sortie, ou au moins un élément `failed` pendant un nettoyage. |
| `2` | Erreur d'utilisation : commande ou option inconnue, valeur invalide (`--min-size`, `--older-than`, `--risk` y compris `never`, `--category` y compris un nom d'outil comme `cursor`, `--sort`, `--top`, `--limit`, un type inconnu pour `artifacts --target`), `clean --yes` sans `--smart`, `-c` ni `-k` (ou avec seulement un `-k` qui nomme un scanner couvrant plusieurs catégories), `clean --json` sans `--yes`, `clean` sans terminal et sans `--yes`, un `--root` qui n'est pas un dossier ou qui vaut `/`, catégorie désactivée dans la configuration. |
| `130` | Interrompu par <kbd>Ctrl</kbd>+<kbd>C</kbd>. |
| `143` | Interrompu par `SIGTERM`. |

Un scanner qui échoue ne change pas non plus le code de sortie : l'analyse continue, l'erreur est affichée sur stderr et listée dans `errors`.

## Stabilité

- Les documents n'ont pas de numéro de version de schéma propre. `version` vous indique quel lu-cleaner les a produits ; figez-la en CI si vous dépendez des détails.
- Attendez-vous à des changements **additifs** à chaque version : nouveaux champs, nouveaux types d'éléments, nouvelles clés `meta`, nouvelles catégories. Écrivez des consommateurs qui ignorent ce qu'ils ne connaissent pas.
- Les valeurs de `kind`, `category`, `risk`, `method` et `status` sont des identifiants destinés aux scripts. `name`, `note`, `warn`, `message` et toutes les valeurs de `meta` sont du texte destiné aux humains, dont la formulation peut changer.
- Les clés de `meta` dépendent du scanner et de ce qu'il a pu observer ; testez leur présence au lieu de supposer qu'une clé existe.
- La sortie texte (tableaux, couleurs, colonne `★`) est réservée aux humains. Elle est brute et sans emoji quand stdout n'est pas un terminal, mais sa mise en forme n'est pas un contrat. Ni le texte ni le JSON ne transportent jamais un caractère de contrôle brut issu d'un nom de fichier : le texte affiche un échappement visible (`\x1b`), le JSON l'échappe sous la forme `\u001b`.

Voir [Automatisation et scripts](/lu-cleaner/fr/guides/automation/) pour des scripts complets.
