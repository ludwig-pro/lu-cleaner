---
title: Configuration
description: Toutes les clés de config.toml avec leur valeur par défaut et des exemples, la façon dont les racines de projets et de worktrees sont trouvées, et les variables d'environnement lues par lu-cleaner.
sidebar:
  order: 3
---

lu-cleaner fonctionne sans aucune configuration. Le fichier de configuration est facultatif, tout comme chacune de ses clés : une clé absente garde sa valeur par défaut.

## Le fichier de configuration

Le fichier est au format TOML. lu-cleaner le cherche au premier de ces emplacements :

| Condition | Chemin |
| --- | --- |
| `LU_CLEANER_CONFIG` est définie | Sa valeur, utilisée comme chemin complet du fichier. |
| `XDG_CONFIG_HOME` est définie | `$XDG_CONFIG_HOME/lu-cleaner/config.toml` |
| Sinon | `~/.config/lu-cleaner/config.toml` |

Quatre commandes permettent de le gérer :

```bash
lu-cleaner config init   # écrit un exemple commenté (n'écrase jamais un fichier existant)
lu-cleaner config edit   # l'ouvre dans $VISUAL ou $EDITOR (par défaut : open -t), en le créant si besoin
lu-cleaner config show   # valeurs effectives, racines résolues et fichier d'historique
lu-cleaner config path   # affiche le chemin du fichier
```

`lu-cleaner config` seul équivaut à `config show`, et `config show --json` affiche les mêmes informations en JSON. Consultez la [référence de la commande `config`](/lu-cleaner/reference/commands/config/).

### Validation

Le fichier est validé à chaque chargement. lu-cleaner refuse d'analyser ou de nettoyer tant que le fichier n'est pas corrigé s'il contient :

- une clé inconnue, par exemple une faute de frappe comme `stale_afer` ;
- une taille ou une durée illisible ;
- une `$VARIABLE` non définie dans `exclude` ou `protect`.

Cette rigueur est volontaire : un `protect` ou un `exclude` mal orthographié et ignoré sans prévenir, ou développé en un autre chemin, laisserait sans protection les chemins mêmes que vous vouliez protéger. `config edit` vérifie le fichier à la fermeture de l'éditeur et signale les erreurs de syntaxe TOML et les clés inconnues ; les autres erreurs sont signalées par la commande suivante qui le charge. Un nom inconnu dans `disabled_categories` ne provoque qu'un avertissement, puisque cette clé ne peut que masquer des éléments.

Dans toutes les clés, les chemins acceptent `~` pour votre dossier personnel. Les chemins relatifs sont relatifs à votre dossier personnel. `exclude` et `protect` développent aussi les variables d'environnement (`$HOME`, `${TMPDIR}`, toute autre variable ; `$$` est un `$` littéral).

## Exemple

```toml
# ~/.config/lu-cleaner/config.toml

# Chercher les projets uniquement dans ces dossiers (au lieu de les détecter automatiquement).
roots = ["~/dev", "~/work/clients"]

# Un agent maison crée ses worktrees ici.
worktree_roots = ["~/agents/worktrees"]

# Ne jamais regarder dedans, ne jamais nettoyer.
exclude = ["~/dev/legacy-monorepo"]

# Ne jamais nettoyer ces chemins, ni rien de ce qui les contient.
protect = ["~/.android/avd/Pixel_8_API_35.avd", "~/Library/Developer/Xcode/Archives"]

# Masquer les petits éléments, et ne considérer comme inactif que ce qui a un mois.
min_size = "50MB"
stale_after = "30d"

# Garder deux versions des CLI d'outils d'IA versionnées.
keep_latest = 2

# Catégories qui ne vous intéressent pas.
disabled_categories = ["containers", "langs"]

# Un dossier de sortie de build propre à vos projets.
extra_artifacts = ["tmp-build"]
```

## Clés

| Clé | Type | Défaut | Résumé |
| --- | --- | --- | --- |
| [`roots`](#roots) | liste de chemins | détection automatique | Dossiers dans lesquels chercher les projets. |
| [`worktree_roots`](#worktree_roots) | liste de chemins | `[]` | Dossiers supplémentaires où des outils créent des worktrees git. |
| [`exclude`](#exclude) | liste de chemins | `[]` | Jamais analysés, jamais nettoyés. |
| [`protect`](#protect) | liste de chemins | `[]` | Jamais nettoyés, en plus de la liste intégrée. |
| [`max_depth`](#max_depth) | entier | `8` | Profondeur de la recherche de projets sous chaque racine. |
| [`min_size`](#min_size) | taille | `"1MB"` | Masquer les éléments plus petits dans les listes. |
| [`stale_after`](#stale_after) | durée | `"14d"` | Délai après lequel un élément inutilisé devient inactif. |
| [`keep_latest`](#keep_latest) | entier | `1` | Versions les plus récentes des installations versionnées tenues à l'écart de la sélection intelligente. |
| [`disabled_categories`](#disabled_categories) | liste de catégories | `[]` | Catégories entièrement ignorées. |
| [`use_trash`](#use_trash) | booléen | `false` | Déplacer vers la Corbeille au lieu de supprimer. |
| [`extra_artifacts`](#extra_artifacts) | liste de noms | `[]` | Noms de dossiers d'artefacts de projet supplémentaires. |

Les options de la ligne de commande l'emportent sur le fichier : `--root` remplace `roots` pour l'analyse des artefacts de projet, `--min-size` remplace `min_size`, `--trash` active le mode Corbeille même avec `use_trash = false`, et `--trash=false` le désactive quand `use_trash = true`.

### `roots`

Dossiers dans lesquels lu-cleaner cherche les artefacts de projet (`node_modules`, `ios/Pods`, `ios/build`, `android/app/build`, `.expo`…). lu-cleaner y cherche aussi les dépôts git, pour lister leurs worktrees, ainsi que les fichiers de projet qui fixent une version de Node.js.

```toml
roots = ["~/dev", "~/work/clients"]
```

- Les dossiers qui n'existent pas sont ignorés.
- Un dossier imbriqué dans une autre racine est écarté : la racine englobante le couvre déjà.
- Chaque racine est protégée en tant que telle : lu-cleaner nettoie à l'intérieur, jamais la racine elle-même ni un dossier qui la contient.
- `lu-cleaner --root <dir>` (répétable) et les arguments positionnels de `lu-cleaner artifacts <dir>…` remplacent `roots` le temps d'une exécution, **pour l'analyse des artefacts de projet uniquement** : exactement ces dossiers sont analysés, sans les racines de worktrees ni les dossiers supplémentaires intégrés. Les autres scanners gardent vos racines configurées, puisqu'ils les lisent pour savoir ce qu'utilisent vos projets (versions de Node, de Ruby et du SDK Android, dépôts principaux des worktrees). Ces dossiers doivent exister, les chemins relatifs sont résolus depuis le dossier courant, et `/` est refusé.

#### Détection automatique

Quand `roots` est vide (le cas par défaut), lu-cleaner utilise tous les dossiers de cette liste qui existent :

`~/local_sources`, `~/dev`, `~/Dev`, `~/Developer`, `~/Projects`, `~/projects`, `~/code`, `~/Code`, `~/src`, `~/workspace`, `~/Workspace`, `~/repos`, `~/git`, `~/github`, `~/GitHub`, `~/Documents/GitHub`, `~/Documents/Projects`, `~/Documents/dev`, `~/Sites`, `~/conductor/repos`.

Un candidat n'est retenu que si son nom correspond exactement, casse comprise. Sur un volume APFS insensible à la casse, `~/Dev` et `~/dev` désignent le même dossier, et exiger le nom exact évite de l'analyser deux fois. Les dossiers exclus sont ignorés.

Lancez `lu-cleaner config show` pour voir le résultat : les racines sont listées sous **Project roots**, avec leur provenance (`auto-detected`, `config`, `--root` ou `arguments`).

:::tip
Si vos projets se trouvent ailleurs, par exemple dans `~/work`, définissez `roots` explicitement. La détection automatique ne connaît que les noms de dossiers ci-dessus.
:::

### `worktree_roots`

Dossiers supplémentaires où des outils créent des worktrees git. Ils s'ajoutent à la liste intégrée, sans jamais la remplacer :

| Racine de worktrees intégrée | Créée par |
| --- | --- |
| `~/.codex/worktrees` | Codex |
| `~/.cursor/worktrees` | Cursor |
| `~/conductor/workspaces` | Conductor |
| `~/.claude/worktrees`, `~/.claude-worktrees` | Claude Code |
| `~/Library/Application Support/Claude/worktrees` | Application de bureau Claude |
| `~/.superset/worktrees` | Superset |
| `~/.worktrees` | Création manuelle ou autres outils |

Seuls les dossiers existants sont utilisés. Dans chacun, lu-cleaner cherche les worktrees liés (dossiers dont `.git` est un fichier) jusqu'à quatre niveaux de profondeur, et y cherche les artefacts de projet comme dans une racine, sauf si vous passez des racines en ligne de commande.

Vous aurez rarement besoin de cette clé. Les worktrees sont aussi trouvés grâce à `git worktree list` sur chaque dépôt trouvé sous vos racines, et dans les dossiers `.claude/worktrees`, `.worktrees` et `worktrees` de chaque dépôt (ainsi que dans un dossier `<repo>-worktrees` placé à côté). Ajoutez un dossier ici quand un outil crée des worktrees à un emplacement personnalisé **et** que les dépôts principaux sont en dehors de vos racines.

```toml
worktree_roots = ["~/agents/worktrees"]
```

Voir [Nettoyer les worktrees des agents IA](/lu-cleaner/fr/guides/ai-worktrees/).

### `exclude`

Chemins dans lesquels lu-cleaner ne regarde jamais et qu'il ne nettoie jamais. Utilisez cette clé pour des dossiers lents à analyser qui ne contiennent rien à nettoyer (une énorme archive, un jeu de données monté), ou que vous voulez écarter complètement.

```toml
exclude = ["~/dev/legacy-monorepo", "~/datasets"]
```

La correspondance se fait par composants de chemin entiers : `~/dev/legacy` exclut `~/dev/legacy/app` mais pas `~/dev/legacy-app`. Comme sur APFS, elle ne tient compte ni de la casse ni de la normalisation Unicode. Les entrées sont des chemins littéraux, jamais des motifs (`~/dev/[wip] shop` désigne ce dossier-là), et une entrée qui passe par un lien symbolique couvre aussi son emplacement réel. Les chemins exclus sont aussi transmis au garde-fou de sécurité : rien de ce qu'ils contiennent, et rien de ce qui les contient, ne peut être supprimé, et la sortie de chaque scanner est filtrée en fonction d'eux.

`lu-cleaner config show` liste les entrées `exclude` et `protect` qui n'existent pas : c'est sans danger, mais c'est souvent une faute de frappe.

### `protect`

Chemins qui ne sont jamais nettoyés, pas plus que ce qu'ils contiennent ni ce qui les contient. Ils s'ajoutent à la liste intégrée des chemins protégés : clés SSH et GPG, identifiants cloud, profils de shell, Trousseau, Mail, Messages, iCloud Drive, réglages, mémoires et index de sessions des outils d'IA, keystore de débogage Android, sauvegardes d'appareils faites par le Finder, et d'autres encore. Voir [Modèle de sécurité](/lu-cleaner/fr/concepts/safety/).

```toml
# Garder cet émulateur et toutes les archives Xcode, quoi qu'en dise l'analyse.
protect = ["~/.android/avd/Pixel_8_API_35.avd", "~/Library/Developer/Xcode/Archives"]
```

Les entrées suivent les mêmes règles qu'`exclude` : chemins littéraux, insensibles à la casse et à la normalisation Unicode, `~` et `$VARS` développés, liens symboliques résolus. Les scanners ne proposent pas les chemins protégés, et le garde-fou de sécurité les refuse au moment de la suppression s'ils apparaissent malgré tout.

**`exclude` ou `protect` ?** Les deux empêchent la suppression. `exclude` empêche en plus lu-cleaner de lire le chemin, ce qui accélère les analyses. Utilisez `protect` pour un élément précis dans un dossier qui doit par ailleurs être analysé normalement, par exemple un émulateur parmi d'autres.

### `max_depth`

Nombre de niveaux que la recherche de projets descend sous chaque racine. La valeur par défaut, `8`, couvre des arborescences comme `~/dev/clients/acme/apps/mobile/node_modules`. Le scanner de worktrees applique la même limite quand il cherche des dépôts sous vos racines.

```toml
max_depth = 10
```

Une valeur de `0` ou moins équivaut à la valeur par défaut. Une recherche plus profonde trouve davantage de projets imbriqués, mais prend plus de temps.

### `min_size`

Les éléments plus petits que cette taille sont masqués dans le tableau de bord, les sélecteurs, `scan` et `doctor`.

```toml
min_size = "50MB"
```

Les tailles acceptent par défaut les unités décimales (`500MB`, `1.5GB`, `1.5G`) et les unités binaires avec un `i` (`2GiB`). Un nombre seul est exprimé en octets ; `"0"` affiche tout. Les éléments en cours de mesure et ceux qui exécutent une commande ne sont jamais masqués.

`--min-size` remplace cette valeur pour une commande. `clean --yes` ignore `min_size` et nettoie les éléments de toutes tailles, sauf si vous passez `--min-size` explicitement. Ainsi, un nettoyage scripté comme `lu-cleaner clean --yes -k node_modules` ne laisse pas de côté les petits éléments sans le dire.

### `stale_after`

Un élément inutilisé depuis plus longtemps que cette durée devient **inactif** (*stale*). Le sélecteur affiche alors son âge en jaune avec l'étiquette `stale`, et les éléments <span class="risk moderate">moderate</span> deviennent éligibles à la sélection intelligente une fois inactifs. Quelques éléments moderate sont recommandés par leur scanner quel que soit leur âge, comme les versions remplacées d'un outil. [Niveaux de risque et sélection intelligente](/lu-cleaner/fr/concepts/risk-and-smart-select/) détaille les règles.

```toml
stale_after = "30d"
```

Les durées acceptent `h` (heures), `d` (jours), `w` (semaines), `m` (mois de 30 jours) et `y` (années) ; un nombre seul est exprimé en jours. Exemples : `"12h"`, `"14d"`, `"2w"`, `"6m"`, `"1y"`. `--older-than` utilise le même format.

### `keep_latest`

Nombre de versions les plus récentes à conserver pour les installations versionnées, en plus de celles qui sont actives, épinglées ou en cours d'exécution. Les versions conservées ne sont jamais recommandées par la sélection intelligente (certaines ne sont pas listées du tout). Par défaut `1`, minimum `1`.

```toml
keep_latest = 2
```

`keep_latest` s'applique :

- aux versions des outils d'IA : Claude Code (autonome, ainsi que les copies embarquées par l'application de bureau Claude et par Conductor), cursor-agent, la CLI `origin` de Cursor, les agents embarqués par Conductor, GitHub Copilot CLI, vibe-kanban ;
- aux paquets du SDK Android (NDK, build-tools, platforms, CMake, sources), aux IDE JetBrains et à Android Studio ;
- aux entrées du catalogue qui conservent leurs copies les plus récentes (navigateurs Puppeteer et Cypress, Kotlin/Native, Skiko) : il augmente leur propre nombre ;
- aux versions de Node.js, par gestionnaire de versions ;
- aux runtimes de simulateurs iOS, par plateforme.

Gradle n'est pas versionné de cette façon. D'autres règles s'appliquent en plus : par exemple, une version de Node.js n'est jamais recommandée tant qu'elle est la version par défaut, fixée par un projet, présente dans votre `PATH` ou en cours d'exécution. Voir [Ce qui est analysé](/lu-cleaner/fr/reference/scanners/).

### `disabled_categories`

Catégories entièrement ignorées : leurs scanners ne s'exécutent pas, sauf si une autre catégorie activée en a besoin, et leurs éléments ne sont jamais listés ni nettoyés.

```toml
disabled_categories = ["containers", "langs", "system"]
```

Les noms valides sont `worktrees`, `artifacts`, `simulators`, `xcode`, `android`, `ai`, `js`, `ide`, `containers`, `langs` et `system`. Un nom d'outil (comme `docker`, `cursor` ou `node_modules`) désactive toute la catégorie à laquelle il appartient. Un nom inconnu affiche un avertissement et est ignoré. Voir [Catégories](/lu-cleaner/fr/concepts/categories/).

Une catégorie désactivée passée à `-c` est ignorée. Si toutes les catégories que vous passez sont désactivées, par exemple `-c containers` avec la configuration ci-dessus, la commande échoue avec une erreur qui renvoie à cette clé.

### `use_trash`

Déplacer les éléments dans `~/.Trash` au lieu de les supprimer :

```toml
use_trash = true
```

Cela revient à passer `--trash` à chaque commande, et le sélecteur démarre en mode Corbeille (appuyez sur `t` pour repasser en mode suppression le temps de la session) ; `--trash=false` le désactive pour une commande. Seuls les fichiers et dossiers sont déplacés dans la Corbeille : les suppressions de worktrees et les commandes comme `xcrun simctl delete <UDID>` supprimeraient définitivement, elles sont donc ignorées, tout comme les éléments qui se trouvent déjà dans la Corbeille.

:::caution
Déplacer vers la Corbeille ne libère **rien** tant que vous ne l'avez pas vidée : c'est pour cette raison que la valeur par défaut est `false`. Les récapitulatifs et l'historique comptent ce qui a été déplacé dans la Corbeille à part de ce qui a été libéré. La sécurité de lu-cleaner ne repose pas sur la Corbeille : utilisez `--dry-run` et la fenêtre de confirmation pour vérifier ce qui sera supprimé.
:::

### `extra_artifacts`

Noms de dossiers supplémentaires à traiter comme des artefacts de projet, en plus des noms intégrés (`node_modules`, `Pods`, `build`, `.gradle`, `.expo`, `.next`…).

```toml
extra_artifacts = ["tmp-build", ".cache-loader"]
```

- Des noms uniquement : une valeur contenant `/` est ignorée, tout comme les noms trop génériques pour être sûrs : `src`, `source`, `app`, `apps`, `lib`, `packages`, `ios`, `android`, `.git`.
- Les correspondances sont listées dans **Project artifacts** avec le type `extra-artifact` et le risque <span class="risk moderate">moderate</span>.
- Dans un dépôt git, un dossier ne correspond que si git l'ignore : un dossier suivi qui porte le même nom par hasard n'est donc jamais proposé. En dehors de git, tout dossier portant ce nom correspond.

Pour ne nettoyer que ceux-ci : `lu-cleaner clean --yes -k extra-artifact --dry-run`.

## Variables d'environnement

### lu-cleaner

| Variable | Effet |
| --- | --- |
| `LU_CLEANER_CONFIG` | Chemin complet du fichier de configuration. Prioritaire sur `XDG_CONFIG_HOME`. |
| `XDG_CONFIG_HOME` | Le fichier de configuration est `$XDG_CONFIG_HOME/lu-cleaner/config.toml`. Par défaut : `~/.config`. |
| `XDG_STATE_HOME` | L'historique est `$XDG_STATE_HOME/lu-cleaner/history.jsonl`. Par défaut : `~/.local/state`. |
| `NO_COLOR` | Toute valeur non vide désactive les couleurs, dans la sortie des commandes et dans les écrans interactifs (comme `--no-color`). `TERM=dumb` les désactive aussi dans la sortie des commandes. |
| `VISUAL`, `EDITOR` | Éditeur utilisé par `lu-cleaner config edit`, `VISUAL` en priorité. Sans l'un ni l'autre, le fichier s'ouvre avec `open -t`. |
| `TMPDIR` | Votre dossier temporaire utilisateur. Les caches qu'il contient (Metro, Jest, outils Xcode…) sont analysés, et le garde-fou de sécurité n'autorise les suppressions dans votre zone temporaire utilisateur que si `TMPDIR` vous est privé : jamais dans un dossier partagé comme `/tmp`. |
| `LU_WALKERS` | Nombre maximal de dossiers lus en parallèle pendant la mesure des tailles. Par défaut : 3 × le nombre de cœurs du processeur, au moins 8. Réduisez-le pour alléger la charge sur le disque pendant les analyses. |
| `LU_NO_BULK` | Toute valeur non vide mesure les tailles avec un appel `lstat` par entrée au lieu de l'appel groupé `getattrlistbulk` de macOS. Plus lent : utile uniquement pour diagnostiquer un écart de taille. |

### Emplacements des outils

Les scanners suivent les variables standard des outils qu'ils inspectent, ce qui permet de retrouver les installations déplacées :

| Écosystème | Variables |
| --- | --- |
| Android | `ANDROID_HOME`, `ANDROID_SDK_ROOT`, `ANDROID_AVD_HOME`, `ANDROID_USER_HOME`, `ANDROID_EMULATOR_HOME`, `ANDROID_PREFS_ROOT`, `GRADLE_USER_HOME`, `JAVA_HOME` |
| Versions de Node.js | `NVM_DIR`, `FNM_DIR`, `FNM_MULTISHELL_PATH`, `VOLTA_HOME`, `ASDF_DATA_DIR`, `MISE_DATA_DIR`, `MISE_CONFIG_DIR`, `MISE_GLOBAL_CONFIG_FILE` |
| Gestionnaires de paquets | `NPM_CONFIG_CACHE`, `NPM_CONFIG_PREFIX` (et leurs formes en minuscules `npm_config_*`), `PNPM_HOME`, `YARN_GLOBAL_FOLDER`, `PLAYWRIGHT_BROWSERS_PATH` |
| Modèles d'IA | `OLLAMA_MODELS`, `HF_HOME`, `HF_HUB_CACHE` |
| Autres chaînes d'outils | `RUSTUP_HOME`, `RBENV_ROOT` |
| XDG | `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_RUNTIME_DIR`, `XDG_CONFIG_HOME` |

Les applications graphiques et les agents d'IA s'exécutent souvent sans l'environnement de votre shell. Pour les variables Android, lu-cleaner lit donc aussi les lignes `export` simples de `~/.zshenv`, `~/.zprofile`, `~/.zshrc`, `~/.bash_profile`, `~/.bashrc` et `~/.profile`.

## Voir aussi

- [`lu-cleaner config`](/lu-cleaner/reference/commands/config/) et ses sous-commandes.
- [Fonctionnement](/lu-cleaner/fr/concepts/how-it-works/) : comment les racines alimentent les scanners.
- [Modèle de sécurité](/lu-cleaner/fr/concepts/safety/) : les chemins protégés intégrés.
