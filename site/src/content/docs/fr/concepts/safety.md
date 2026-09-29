---
title: Modèle de sécurité
description: Toutes les vérifications que lu-cleaner effectue avant de supprimer quoi que ce soit, dans l'ordre, la raison d'être de chacune, ce que --force contourne ou non, et ce que lu-cleaner ne fait jamais.
sidebar:
  order: 3
---

lu-cleaner supprime des gigaoctets à la fois sur la machine sur laquelle vous travaillez. Son modèle de sécurité repose sur quatre principes :

- **Les scanners proposent, un seul pipeline décide.** Les scanners ne suppriment jamais rien. Chaque suppression passe par le même exécuteur et le même garde-fou de sécurité, quelle que soit l'origine de l'élément. Une entrée erronée du catalogue ou un bug de scanner peut au pire *proposer* quelque chose, et cette proposition doit encore passer les règles codées en dur du garde-fou.
- **Vérifier au dernier moment.** Votre machine change entre l'analyse et le nettoyage : vous ouvrez Xcode, un agent commence à travailler dans un worktree, un dossier est remplacé par un clone tout neuf. Chacune des vérifications ci-dessous est refaite juste avant le traitement de chaque élément.
- **Refuser en cas de doute** (fail closed). Quand lu-cleaner ne peut pas vérifier quelque chose (la liste des processus est illisible, `lsof` échoue, `simctl` ne répond pas, `git status` renvoie une erreur), l'élément est ignoré.
- **Tout ou rien, élément par élément.** Tous les chemins d'un élément sont validés avant que quoi que ce soit ne soit supprimé : une vérification qui échoue fait ignorer l'élément entier, et le récapitulatif indique pourquoi.

## Ce que lu-cleaner ne fait jamais

- **Il n'utilise jamais `sudo` sur votre Mac** et ne demande jamais de droits administrateur. Les restes qui appartiennent à root (caches dyld périmés de simulateurs, JDK système) sont signalés avec la commande à lancer vous-même. Le seul `sudo` que lu-cleaner exécute est `fstrim` *à l'intérieur* d'une VM Linux colima, juste après un prune Docker, pour que le disque de la VM puisse rétrécir.
- **Il ne supprime jamais de fichiers en dehors de votre dossier personnel et de votre dossier temporaire utilisateur.** L'espace occupé ailleurs (runtimes de simulateurs dans le stockage système, Cellar de Homebrew, disque de la VM Docker) n'est récupéré que par la commande propre à l'outil concerné, qui vous est montrée avant que vous ne confirmiez.
- **Il ne supprime jamais de code source.** Un répertoire qui contient une entrée `.git` de quelque nature que ce soit, ou organisé comme un dépôt nu (bare), est refusé, vos racines de projets ne sont jamais supprimées, et les noms génériques comme `build/` ou `dist/` ne sont proposés que si git les ignore ou si leur contenu prouve qu'il s'agit de sorties de build.
- **Il ne suit jamais les liens symboliques.** Supprimer un lien symbolique supprime le lien, pas sa cible.
- **Il ne touche jamais aux identifiants, aux clés ni à la configuration des outils** (voir [chemins protégés](#3-chemins-protégés)).
- **Il n'entre jamais dans les conteneurs des autres applications sans accès complet au disque.** `~/Library/Containers/<app>` et `~/Library/Group Containers/<group>` ne sont ni lus, ni mesurés, ni nettoyés : sur macOS 14 et versions ultérieures, le premier accès resterait bloqué sur une demande d'autorisation système. Ce qui s'y trouve est signalé comme nécessitant l'accès complet au disque.
- **Il ne supprime jamais rien définitivement en mode Corbeille.** Les suppressions de worktrees et les commandes, qui ne peuvent pas passer par la Corbeille, sont ignorées à la place.
- **Il ne supprime jamais rien sans sélection explicite de votre part**, dans le sélecteur ou avec `clean --yes` et un filtre restrictif.
- **Il ne modifie jamais rien pendant l'analyse.** Les scanners se contentent de lister des répertoires, de lire de petits fichiers et d'exécuter des commandes en lecture seule (`git status` avec les verrous optionnels désactivés, `xcrun simctl list`, `lsof`, `sqlite3` en mode lecture seule immuable, `watchman --no-spawn watch-list`).
- **Il n'affiche jamais de caractères de contrôle bruts.** Les noms, chemins et messages lus sur le disque passent par un filtre (les séquences d'échappement et les caractères de forçage bidirectionnel sont affichés sous forme d'échappements visibles comme `\x1b`), si bien qu'un nom de dossier piégé ne peut pas réécrire votre terminal. La sortie JSON les échappe sous la forme `\uXXXX`.

## Vérifications sur chaque élément, dans l'ordre

Pour chaque élément sélectionné, l'exécuteur effectue ces vérifications. La première qui échoue fait ignorer l'élément, avec sa raison.

1. **Nettoyable.** Les éléments en rapport seul, les éléments non sélectionnables et les éléments de risque <span class="risk never">never</span> sont ignorés (`not cleanable`).
2. **Mode Corbeille.** Avec `--trash`, `use_trash` ou <kbd>t</kbd> dans le sélecteur, les éléments de type worktree et commande sont ignorés (`not possible in Trash mode (it would delete permanently)`), tout comme les éléments qui se trouvent déjà dans `~/.Trash` (`already in the Trash`).
3. **Exige `--force`.** Certains éléments sont refusés tant que vous ne passez pas `--force` : `needs --force: <reason>`. Aujourd'hui, ce sont les dossiers de worktrees orphelins, dont git ne voit plus le travail non commité. Le mode Corbeille peut tout de même les déplacer, puisqu'un déplacement dans la Corbeille peut être annulé.
4. **Applications en cours d'exécution.** Les éléments déclarent les applications qui doivent être fermées : Xcode et `xcodebuild` pour DerivedData, Simulator pour les caches CoreSimulator, le moteur de l'émulateur pour les AVD Android, Codex et ChatGPT pour la base de journaux de Codex, Android Studio pour ses caches. Si l'une d'elles tourne, l'élément est ignoré (`Xcode is running — quit it first (or use --force)`). Si la liste des processus ne peut pas être lue, l'élément est également ignoré, car « ne tourne pas » ne peut pas être prouvé.
5. **Revérification propre à l'élément.** Certains éléments embarquent leur propre test de dernière minute. Un simulateur, ses enregistrements, ses journaux et ses caches ne sont supprimés que si `simctl` indique toujours que le simulateur est éteint ; s'il a été démarré depuis l'analyse, ou si `simctl` ne peut pas être interrogé, l'élément est ignoré. Un simulateur indisponible n'est supprimé que s'il est toujours indisponible, sur le même runtime. Les données Claude Code d'un dossier supprimé ne sont supprimées que si le dossier est toujours absent et qu'aucune session n'y a écrit dans la dernière heure. Un dossier de worktree orphelin n'est supprimé que si ses métadonnées git ont toujours disparu, de façon vérifiée, et qu'il ne contient aucun autre dépôt.
6. **Vérifications par chemin.** Pour chaque chemin de l'élément (les éléments commande n'en ont pas) :
   1. Un chemin qui n'existe plus est laissé de côté. Si tous ont disparu, l'élément est signalé comme `already gone`.
   2. **Identité d'inode.** L'analyse a enregistré l'identité (inode) de chaque chemin. Si elle a changé, le chemin a été remplacé depuis l'analyse (par un lien symbolique, un nouveau clone, une sauvegarde restaurée) et l'élément est ignoré : `changed since the scan (different inode) — rescan`.
   3. **Bases de données ouvertes.** Les fichiers SQLite (`.sqlite`, `.sqlite3`, `.db`, `-wal`, `-shm`, `-journal`, `.vscdb`) sont vérifiés avec `lsof`. Supprimer une base qu'une application a ouverte la corrompt : un fichier ouvert, ou un échec de `lsof`, fait donc ignorer l'élément.
   4. **Le garde-fou de sécurité** valide le chemin. Voir [ci-dessous](#le-garde-fou-de-sécurité).
   5. **Fichiers marqueurs.** Les artefacts de projet ne sont proposés que si un marqueur se trouve à côté : `package.json` à côté de `node_modules`, `Podfile` ou `Podfile.lock` à côté de `Pods`, `build.gradle` à côté d'un `build/` Android. Le marqueur doit toujours être là, sinon le dossier est peut-être devenu autre chose.
   6. **Répertoire courant.** Le répertoire depuis lequel votre shell lance lu-cleaner, ou tout dossier qui le contient, n'est jamais supprimé.
   7. **Supprimable.** Un chemin dont le dossier parent est en lecture seule est ignoré avant que quoi que ce soit ne soit touché, pour qu'il ne reste jamais à moitié supprimé. Un point de montage, un dossier qui contient un volume ou une image disque montés (`contains the mount point … — eject it first`), et le fichier d'un autre utilisateur dans un dossier sticky sont ignorés aussi.
   8. **Processus à l'intérieur.** Un processus dont le répertoire de travail se trouve dans le dossier (un serveur de développement, un shell, un agent), ou qui exécute un programme ou une bibliothèque chargée depuis ce dossier (un binaire node d'un dossier de version de node, un addon natif `.node` de `node_modules`), fait ignorer l'élément : `in use: process 4242 is working inside …`.
7. **Règles des worktrees**, pour les worktrees git. Voir [ci-dessous](#règles-des-worktrees).

Avec `--dry-run` (`-n`), toutes ces vérifications s'exécutent réellement, règles des worktrees comprises, et rien n'est exécuté ensuite : le plan que vous voyez inclut donc les éléments qui seraient ignorés. Seuls les refus de git lui-même au moment de la suppression ne peuvent pas être prévus.

## Le garde-fou de sécurité

Le garde-fou est la dernière ligne de défense. Ses règles sont codées en dur et indépendantes du catalogue et des scanners. Les scanners le consultent aussi, si bien que les chemins protégés n'apparaissent même pas dans la liste. Comme sur APFS, les chemins sont comparés sans tenir compte de la casse ni de la normalisation Unicode : `~/Dev` et `~/dev`, ou un nom tapé avec un `é` composé et le même nom enregistré sous forme décomposée par le Finder, sont le même chemin.

### 1. Chemin bien formé

Le chemin doit être absolu et propre : pas de `..`, pas de `.`, pas de barres obliques en double. Les chemins relatifs ou ambigus sont refusés avant toute autre chose.

### 2. Zones autorisées et dossiers interdits

Les suppressions n'ont lieu que **strictement à l'intérieur** de votre dossier personnel ou de votre zone temporaire utilisateur (le parent de `$TMPDIR`, comme `/var/folders/xx/yyyy/`, qui contient `T/` et `C/`). Cette zone n'est ouverte que si `$TMPDIR` se résout vers le dossier utilisateur que macOS vous attribue et qu'il vous appartient. Tout autre `$TMPDIR` n'autorise que ce qui se trouve strictement à l'intérieur, et seulement s'il vous est privé (il vous appartient et n'est pas accessible en écriture aux autres) : un `$TMPDIR` non défini ou un dossier partagé comme `/tmp` ou `/var/tmp` ne devient jamais une zone autorisée. Tout le reste est refusé : `outside the allowed areas (home, per-user temp)`.

Certains dossiers peuvent voir leur contenu nettoyé, mais ne peuvent **jamais être supprimés eux-mêmes** :

- les dossiers système : `/`, `/System`, `/Library`, `/Applications`, `/Users`, `/usr`, `/bin`, `/etc`, `/var`, `/private`, `/opt`, `/opt/homebrew`, `/Volumes`, `/tmp`… ;
- votre dossier personnel et ses dossiers standard : `~`, `~/Library`, `~/Documents`, `~/Desktop`, `~/Downloads`, `~/Pictures`, `~/Movies`, `~/Music`, `~/.Trash`… ;
- les grands conteneurs de caches et d'outils : `~/Library/Caches`, `~/Library/Application Support`, `~/Library/Developer`, `~/Library/Developer/CoreSimulator/Devices`, `~/Library/Android/sdk`, `~/.config`, `~/.cache`, `~/.local`, `~/.npm`, `~/.gradle`, `~/.android`, `~/.claude`, `~/.codex`, `~/.cursor`, `~/go`… ;
- `$TMPDIR` lui-même et ses dossiers voisins.

`~/Library/Caches/CocoaPods` peut être nettoyé ; `~/Library/Caches` ne le peut pas.

### 3. Chemins protégés

Un chemin protégé n'est jamais supprimé, **ni rien de ce qu'il contient, ni rien de ce qui le contient**. La liste intégrée couvre notamment :

- les clés et identifiants : `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.azure`, `~/.kube`, `~/.netrc`, `~/.npmrc`, `~/.yarnrc.yml`, `~/.gitconfig`, `~/.git-credentials`, `~/.config/gh`, `~/.password-store`, `~/.docker/config.json`, les trousseaux (Keychains) ;
- les profils de shell : `~/.zshrc`, `~/.zprofile`, `~/.bashrc`… ;
- les données personnelles : Preferences, iCloud Drive et les dossiers de stockage cloud, Mail, Messages, Calendriers, Safari, les cookies, les sauvegardes d'appareils iOS (`MobileSync`) ;
- l'identité de développement : les profils de provisionnement, les snippets, raccourcis clavier et thèmes de Xcode, le `debug.keystore` et les clés adb d'Android, `~/.gradle/gradle.properties` et les scripts d'initialisation Gradle, `~/.expo/state.json` d'Expo ;
- la configuration et la mémoire des outils d'IA : `~/.claude.json`, les réglages de Claude Code, `CLAUDE.md`, les skills, agents, commandes, hooks, règles, l'historique et les sessions, le dossier `memory/` de chaque projet ; `auth.json`, `config.toml`, les mémoires, skills, règles, `AGENTS.md` et les bases d'état de Codex ; `mcp.json`, les règles et les skills de Cursor ; les réglages utilisateur, raccourcis clavier, snippets et bases d'état de VS Code et de Cursor ; les identifiants et réglages de Gemini CLI ; la configuration de Claude desktop.

Certaines entrées sont des **motifs**. `~/.claude/projects/*/memory` protège la mémoire automatique de chaque projet Claude Code, et `~/.codex/state_*.sqlite*` chaque base d'état de Codex. Un dossier qui *contient* une correspondance est également refusé : supprimer un dossier `~/.claude/projects/<project>` entier qui contient un dossier `memory/` est refusé. C'est pourquoi le scanner Claude Code supprime un à un les transcripts d'un projet supprimé et conserve `memory/`.

« Contient » compte autant que « se trouve dans » : supprimer `~/Library/Developer/Xcode/UserData` supprimerait vos snippets et raccourcis clavier Xcode, c'est donc refusé même si un scanner le proposait.

Vous pouvez étendre la liste avec la clé `protect` du fichier de configuration, et chaque chemin `exclude` est également protégé. Ces entrées sont des chemins littéraux, jamais des motifs (un dossier nommé `[wip] app` est pris tel quel) ; `~` et les `$VARS` sont développés, et une entrée atteinte à travers un lien symbolique protège aussi son emplacement réel. Voir [Configuration](/lu-cleaner/fr/reference/configuration/).

### 4. Racines d'analyse

Vos racines de projets et vos racines de worktrees peuvent être nettoyées **à l'intérieur**, mais une racine elle-même, ou tout dossier qui contient une racine, n'est jamais supprimé. Avec `~/dev` comme racine, `~/dev/my-app/node_modules` peut partir ; `~/dev` ne le peut pas. Quand vous passez des racines en ligne de commande, vos racines configurées restent protégées elles aussi.

### 5. Les dossiers parents qui sont des liens symboliques sont résolus

Si un dossier parent du chemin est un lien symbolique, l'emplacement **réel** doit passer les mêmes règles. Supposons que `~/Library/Android/sdk` soit un lien symbolique vers un SSD externe : une image système atteinte à travers ce lien se résout en `/Volumes/…`, hors des zones autorisées, et elle est donc refusée. Le chemin lui-même n'est jamais suivi : supprimer un lien symbolique supprime le lien.

### 6. Les dépôts git sont du code source

Un répertoire qui contient une entrée `.git` de **n'importe quelle** nature est un dépôt, et il est refusé : un répertoire `.git` (un clone), un fichier `.git` (un worktree lié, un sous-module, un répertoire git séparé) ou un lien symbolique `.git`. C'est aussi le cas d'un dépôt nu (un fichier `HEAD` avec des dossiers `objects/` et `refs/`). Un `.git` illisible compte comme présent : seul un « n'existe pas » net prouve qu'il n'y en a pas. Le refus s'affiche sous la forme `is a git repository (…)`.

Seuls quelques éléments sont autorisés à être des dépôts : le clone du dépôt de specs CocoaPods, les éléments déjà dans votre Corbeille, les dossiers de worktrees orphelins vérifiés, et les worktrees liés, que git supprime lui-même après les vérifications propres à lu-cleaner (voir ci-dessous).

## Règles des worktrees

Supprimer un worktree git est l'opération la plus sensible que réalise lu-cleaner, car le dossier peut contenir du travail qui n'existe nulle part ailleurs.

- Seuls les **worktrees liés** sont supprimés de cette façon : un checkout dont le fichier `.git` pointe vers l'enregistrement que git en tient, qui pointe lui-même vers ce même dossier. Un checkout copié ou déplacé est refusé (`git records this worktree at …`). La commande est `git worktree remove`, lancée depuis le dépôt principal. **La branche est toujours conservée** : les commits d'une branche, poussés ou non, restent dans le dépôt principal.
- Sauf si vous passez `--force`, la suppression est ignorée quand :
  - le worktree est **verrouillé** (`git worktree lock`) ;
  - il contient des **modifications non commitées**, sur des fichiers suivis ou non suivis. Le décompte utilise des options git explicites qui ignorent votre configuration : `status.showUntrackedFiles=no` ne peut donc pas masquer les fichiers non suivis, et les sous-modules modifiés comptent ;
  - sa HEAD est **détachée** avec des commits qu'aucune branche, aucun tag ni aucun remote ne contient : ils deviendraient inaccessibles. Créez d'abord une branche (`git branch <name>`) ;
  - il **contient un autre worktree ou dépôt**, enregistré ou non, même dans un dossier ignoré : git le supprimerait avec lui ;
  - son dépôt principal n'existe plus, ou git ne peut pas répondre (`git status` échoue), car un état inconnu n'est pas un état propre.
- `--dry-run` effectue les mêmes vérifications, `git status` compris, et signale les mêmes refus.
- Git refuse lui-même, sans `--force`, de supprimer les worktrees modifiés, verrouillés ou contenant des sous-modules. C'est une seconde ligne de défense après les vérifications de lu-cleaner.
- **Les commits non poussés sur une branche nommée ne bloquent pas la suppression** : la branche survit, et ses commits aussi. Le scanner affiche tout de même un avertissement (`2 unpushed commits (branch feature/login is kept)`), qui exclut le worktree de la sélection intelligente.
- **Les fichiers ignorés sont perdus.** `node_modules`, `Pods` et les builds natifs ne bloquent jamais `git worktree remove`, ce qui est généralement ce que vous voulez. Les fichiers ignorés qui ressemblent à des secrets ou à des réglages personnels (`.env*`, `.npmrc`, keystores, clés de signature, configurations Firebase, surcharges `*.local.*`, réglages locaux de Claude…) et qui n'existent nulle part ailleurs rendent le worktree <span class="risk caution">caution</span>, avec l'avertissement `ignored secret/local files would be lost: .env.local`. Un fichier identique au même chemin dans l'arbre de travail principal n'est pas compté.
- Un worktree que git ne suit plus n'est un **orphelin** que si c'est vérifié : ses métadonnées git (ou son dépôt principal) n'existent pas, sous un dossier qui peut être lu. Git ne peut pas aider : le dossier est supprimé comme un simple répertoire, après la vérification du garde-fou et une revérification qu'il est toujours orphelin et ne contient aucun autre dépôt. Les orphelins sont <span class="risk caution">caution</span>, jamais présélectionnés, et **exigent `--force`**, puisque git ne voit plus leur travail non commité. Le mode Corbeille peut les déplacer sans `--force`.
- Un worktree dont le dépôt principal ou les métadonnées git **ne peuvent pas être lus** (permissions, protection de la vie privée de macOS, volume non monté) n'est pas un orphelin : il est en rapport seul, jamais supprimé, et ses métadonnées ne sont jamais élaguées (prune). Un worktree dont le dépôt principal a été déplacé ou renommé est lui aussi en rapport seul, avec la commande `git worktree repair` à lancer.
- Les worktrees hors des zones autorisées (par exemple dans `/tmp`) sont en rapport seul. Leur avertissement donne la commande exacte `git -C <repo> worktree remove <path>` à lancer vous-même.
- Les worktrees ne sont jamais déplacés dans la Corbeille : en mode Corbeille, ils sont ignorés.

Au moment de l'analyse, le scanner worktrees marque aussi comme <span class="risk caution">caution</span> tout worktree ouvert dans Cursor ou VS Code, dans lequel un processus travaille, ou qui a un thread Codex, une session Claude desktop ou un workspace Conductor actif. Le worktree dans lequel vous vous trouvez ne peut pas être sélectionné du tout.

## Ce que `--force` contourne

`--force` existe pour le cas où vous avez vérifié un élément vous-même et qu'un garde-fou vous bloque (Xcode est ouvert pour un autre projet, vous voulez vraiment abandonner un worktree modifié). Il assouplit un petit ensemble fixe de vérifications :

| Vérification | Contournée par `--force` |
|---|---|
| Applications en cours d'exécution | Oui |
| Processus qui travaillent dans un dossier ou s'exécutent depuis celui-ci | Oui |
| Worktree verrouillé, modifications non commitées, commits sur aucune branche, worktrees ou dépôts imbriqués (git est appelé avec `--force --force`) | Oui |
| Éléments qui exigent `--force` (dossiers de worktrees orphelins) | Oui |
| Revérifications propres à l'élément (simulateur toujours éteint, dossier orphelin toujours absent) | Non |
| Identité d'inode depuis l'analyse | Non |
| Bases SQLite ouvertes | Non |
| Fichiers marqueurs | Non |
| Répertoire courant, parent en lecture seule, volumes montés à l'intérieur | Non |
| Le garde-fou de sécurité : zones autorisées, dossiers interdits, chemins protégés, racines d'analyse, dépôts git, parents qui sont des liens symboliques | Non |
| Éléments en rapport seul et risque <span class="risk never">never</span> | Non |
| Mode Corbeille qui ignore les worktrees et les commandes | Non |
| Exclusion des éléments <span class="risk caution">caution</span> et des éléments qui portent un avertissement par `clean --yes`, sauf avec `--risk caution` | Non |

:::danger
`--force` s'applique à tous les éléments de l'exécution. Combiné à `--yes`, il s'applique à tout le plan sans rien demander. Utilisez-le sur une sélection restreinte (un worktree, un type d'élément), après un `--dry-run`.
:::

## Nettoyage non interactif

`clean --yes` (ainsi que `artifacts --yes`, `worktrees --yes` et `devices --yes`) ne pose jamais de question ; ses règles par défaut sont donc plus strictes que celles du sélecteur :

- **Un filtre restrictif est obligatoire** : `--smart`, `--category`/`-c` ou `--kind`/`-k` (`artifacts`, `worktrees` et `devices` sont déjà restreints à leurs propres catégories). Un simple `lu-cleaner clean --yes` est refusé avec le code de sortie 2 : un script dont une variable est vide ne peut pas se transformer en « tout nettoyer ». Un `--kind` qui nomme un scanner couvrant plusieurs catégories (`-k catalog`, `-k system`) ne restreint pas assez à lui seul, et `artifacts --target` rejette un type qu'il ne connaît pas au lieu de ne rien chercher.
- **Les éléments caution sont exclus**, sauf si vous passez explicitement `--risk caution`. C'est aussi le cas des éléments que l'analyse a signalés par un avertissement (utilisés par un processus, fichiers secrets ignorés par git qui seraient perdus, runtime utilisé par des simulateurs démarrés…) : ils sont listés sous **Held back** à la place.
- Les éléments encore en cours de mesure ne sont jamais inclus.

```bash
lu-cleaner clean --yes --smart --dry-run   # commencez toujours par ceci
lu-cleaner clean --yes --smart
```

Voir [Automatisation et scripts](/lu-cleaner/fr/guides/automation/) pour les scripts et les nettoyages planifiés.

## L'analyseur

`lu-cleaner analyze` vous permet de supprimer des dossiers arbitraires depuis une vue à la ncdu. Ces suppressions passent par le même exécuteur et le même garde-fou : chaque entrée est traitée comme <span class="risk caution">caution</span> et demande de taper `yes`, et un worktree lié vérifié est supprimé avec `git worktree remove`, comme dans le sélecteur. Les dépôts git, les sous-modules, les données git, les checkouts que git ne peut pas supprimer sans risque et les dossiers qui contiennent un dépôt ne sont jamais supprimés depuis l'analyseur, et, sans accès complet au disque, les conteneurs des autres applications sont affichés verrouillés et jamais ouverts.
