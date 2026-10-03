---
title: FAQ
description: Réponses aux questions fréquentes sur lu-cleaner, notamment la sécurité, pourquoi rien n'est sélectionné par défaut, les worktrees et les branches, les éléments ignorés ou non recommandés, le mode Corbeille, l'accès complet au disque, sudo, les disques externes, la protection de dossiers et l'historique des nettoyages.
sidebar:
  order: 2
---

## Sécurité

### Est-ce sans danger ?

Le scan est en lecture seule : `lu-cleaner`, `scan`, `doctor` et les sélecteurs ne suppriment jamais rien d'eux-mêmes. Rien n'est présélectionné, et toute suppression exige un choix explicite de votre part : soit une sélection que vous avez faite et confirmée dans le sélecteur, soit `clean --yes` avec un filtre restrictif. Plusieurs règles ajoutent une marge de sécurité :

- Chaque élément a un [niveau de risque](/lu-cleaner/fr/concepts/risk-and-smart-select/). Les éléments <span class="risk caution">caution</span> (sessions, modèles, worktrees modifiés…) ne sont jamais choisis par la sélection intelligente, sont ignorés quand vous appuyez sur `space` sur une catégorie entière, exigent de taper `yes` dans le sélecteur et sont écartés de `clean --yes`, sauf si vous passez `--risk caution`, comme tout élément qui porte un avertissement.
- Juste avant la suppression, chaque chemin passe par un [garde-fou de sécurité](/lu-cleaner/fr/concepts/safety/). Il refuse tout ce qui se trouve hors de votre dossier personnel et de votre dossier temporaire utilisateur, les dossiers système et les dossiers de premier niveau du dossier personnel, les identifiants et configurations d'outils, les dépôts git et les racines de vos projets elles-mêmes.
- lu-cleaner revérifie aussi l'état de chaque élément à ce moment-là : le fichier est toujours celui qu'il a scanné, l'application propriétaire est fermée, aucun processus ne travaille dedans, aucune base de données n'est ouverte.

Commencez par une simulation pour voir exactement ce qui se passerait :

```bash
lu-cleaner clean --yes --smart --dry-run
```

### Puis-je annuler un nettoyage ?

Non. La méthode par défaut supprime définitivement, comme `rm -rf`, car c'est le seul moyen de libérer l'espace immédiatement. Gardez en tête ce que « supprimé » signifie pour chaque niveau de risque :

- Les éléments <span class="risk safe">safe</span> et <span class="risk moderate">moderate</span> sont régénérés par leur outil : un cache se remplit de nouveau, `pod install` ou `pnpm install` reconstruit, un mécanisme de mise à jour retélécharge une version.
- Supprimer un worktree conserve sa branche (voir la question suivante).
- Si vous voulez un filet de sécurité, utilisez le [mode Corbeille](#que-fait-le-mode-corbeille-). L'espace n'est alors libéré que lorsque vous videz la Corbeille.

### Vais-je perdre mes branches quand un worktree est supprimé ?

Non. lu-cleaner supprime les worktrees avec `git worktree remove`, qui efface la copie de travail et conserve la branche et ses commits dans le dépôt principal. Les commits que vous n'avez pas poussés restent sur cette branche.

lu-cleaner refuse de supprimer un worktree, sauf si vous passez `--force`, lorsque :

- il contient des modifications non commitées, y compris des fichiers non suivis (quoi que masque votre configuration git) ;
- il est sur une HEAD détachée avec des commits qu'aucune branche, aucun tag ni aucun remote ne contient (ils deviendraient inaccessibles) ;
- il est verrouillé (`git worktree lock`) ;
- il contient un autre worktree ou dépôt, que git supprimerait avec lui ;
- lu-cleaner ne parvient pas à lire son statut git.

`--dry-run` effectue les mêmes vérifications. Git refuse lui-même les worktrees modifiés ou verrouillés, en seconde ligne de défense. Les fichiers ignorés par git, comme `node_modules`, `Pods` ou `.env`, ne bloquent pas la suppression. Lorsqu'un worktree contient des fichiers secrets ou personnels ignorés qui n'existent nulle part ailleurs (`.env*`, `.npmrc`, keystores, clés de signature…), lu-cleaner le marque <span class="risk caution">caution</span> avec un avertissement, car ils disparaissent avec la copie de travail.

Un worktree que git ne suit plus (ses métadonnées ou son dépôt principal ont disparu, de façon vérifiée) est un orphelin : il est supprimé comme un simple dossier, et uniquement avec `--force`. Un worktree dont le dépôt ne peut pas être lu est seulement signalé. Voir [Nettoyer les worktrees des agents IA](/lu-cleaner/fr/guides/ai-worktrees/).

### Peut-il supprimer mon code source ?

Le garde-fou refuse de supprimer tout répertoire qui est un dépôt git (qui contient un dossier, un fichier ou un lien symbolique `.git`, ou un dépôt nu), les racines de vos projets et tout dossier qui les contient. Les artefacts de projet ne sont proposés que si leur fichier marqueur se trouve à côté (par exemple `package.json` à côté de `node_modules`), et lu-cleaner revérifie ce marqueur juste avant la suppression. Un dossier nommé `build` qui contient du code source suivi par git n'est pas proposé. Voir [Projets React Native](/lu-cleaner/fr/guides/react-native-projects/).

### Que contourne `--force` ?

`--force` saute trois types de vérifications : les garde-fous « l'application est en cours d'exécution », la vérification « un processus travaille dans ce dossier » et les vérifications des worktrees (modifications non commitées, commits sur aucune branche, verrou, dépôts imbriqués). Il accepte aussi les éléments qui l'exigent, comme les dossiers de worktrees orphelins. Il ne contourne **pas** le garde-fou de sécurité, la vérification des bases de données ouvertes, la vérification que le chemin n'a pas changé depuis le scan, celle du fichier marqueur, ni la règle selon laquelle `clean --yes` exige `--risk caution` pour les éléments caution et ceux qui portent un avertissement. Utilisez-le rarement, et après un `--dry-run`.

## Comprendre les résultats

### Pourquoi un élément a-t-il été ignoré ?

Juste avant le nettoyage, chaque élément est revérifié. Si quelque chose a changé ou semble risqué, lu-cleaner ignore l'élément, nettoie les autres et indique la raison sous **Skipped** (ou `skipped: …` dans le sélecteur) :

| Message | Pourquoi | Que faire |
|---|---|---|
| `Xcode is running — quit it first (or use --force)` | L'élément appartient à une application ouverte. | Quittez l'application et relancez. |
| `cannot verify that … is closed` | La liste des processus, ou `lsof`, n'a pas pu être lue. Dans le doute, lu-cleaner refuse. | Relancez. |
| `… is open by process 1234 — quit the app first` | Une base SQLite (ou son fichier `-wal`/`-shm`) est ouverte. `--force` n'y change rien. | Quittez l'application qui l'utilise. |
| `in use: process 1234 is working inside …` | Un shell, un serveur de développement ou un agent a ce dossier pour répertoire de travail. | Arrêtez-le, ou faites `cd` ailleurs. |
| `in use: process 1234 runs a program or library from …` | Quelque chose s'exécute depuis ce dossier, par exemple une version de node ou un outil de SDK. | Arrêtez le processus. |
| `current directory is inside …` | Vous avez lancé lu-cleaner depuis l'intérieur du dossier. | `cd ~` et relancez. |
| `… changed since the scan (different inode) — rescan` | Le chemin a été remplacé depuis le scan (nouveau clone, lien symbolique…). | Relancez un scan. |
| `marker [package.json] not found next to … anymore` | Le fichier marqueur de l'artefact a disparu. | Relancez un scan. |
| `blocked by safety guard: …` | Le chemin est protégé, hors des zones autorisées, est un dépôt git ou une racine de projet. | Rien : c'est voulu. Vérifiez votre liste `protect` si cela vous surprend. |
| `… contains the mount point … — eject it first` | Un volume ou une image disque montés se trouvent dans le dossier. | Éjectez-les. |
| `parent directory … is read-only …` | Le dossier ne peut pas être retiré de son parent ; rien n'a été touché. | Corrigez les permissions, ou supprimez-le vous-même. |
| `simulator … is booted now — shut it down first` | Le simulateur a été démarré après le scan. | `xcrun simctl shutdown all`. |
| `… exists again` / `a live Claude Code session runs in …` | Des données proposées comme orphelines appartiennent à un dossier revenu ou en cours d'utilisation. | Relancez un scan. |
| `N uncommitted change(s) — commit them or use --force` | Worktree avec des modifications locales. | Commitez-les, mettez-les de côté (stash) ou abandonnez-les. |
| `contains the git repository … (git would delete it along)` / `contains another worktree, …` | Un dépôt ou un worktree se trouve dans le worktree. | Déplacez-le ou supprimez-le d'abord. |
| `needs --force: orphaned …` | Un dossier de worktree orphelin : git ne voit plus son travail non commité. | Vérifiez-le, puis nettoyez-le avec `--force`, ou déplacez-le dans la Corbeille. |
| `main repository … no longer exists …` | Le dépôt a été supprimé ou déplacé depuis le scan. | Relancez un scan : le worktree est alors listé comme orphelin. |
| `detached HEAD with N commit(s) on no branch — they would be lost…` | Les commits du worktree deviendraient inaccessibles. | `git branch <name>` dans le worktree. |
| `worktree is locked (git worktree lock) — use --force` | Le worktree est verrouillé. | `git worktree unlock <path>` si vous êtes sûr de vous. |
| `git refused to remove it (…)` | Les vérifications propres à git ont échoué. | Lisez le message de git. |
| `already gone` | Le chemin n'existe plus. | Rien. |
| `not possible in Trash mode (it would delete permanently)` / `already in the Trash` | Le mode Corbeille ne fait que déplacer des fichiers et des dossiers. | Nettoyez sans le mode Corbeille si vous voulez les supprimer. |
| `not cleanable (…)` | L'élément est signalé uniquement. | Rien. |
| `cancelled` | Vous avez appuyé sur `ctrl+c`. | Relancez. |

### Pourquoi rien n'est-il sélectionné quand j'ouvre lu-cleaner ?

Parce que supprimer doit être votre décision. `lu-cleaner` et `lu-cleaner clean` s'ouvrent avec une sélection vide : le pied de page indique « nothing selected », la colonne SELECTED est vide, et `d` n'a rien à nettoyer tant que vous n'avez pas choisi. Les versions précédentes démarraient avec les éléments recommandés déjà sélectionnés, si bien qu'un simple `d` suivi de `enter` pouvait supprimer bien plus que ce que vous aviez regardé. Ce n'est plus le cas.

Pour choisir :

- `space` sélectionne l'élément sous le curseur. Sur une catégorie, il sélectionne tout ce qu'elle contient sauf les éléments <span class="risk caution">caution</span>, que vous choisissez un par un.
- `a` est la [sélection intelligente](/lu-cleaner/fr/concepts/risk-and-smart-select/) : elle sélectionne les éléments recommandés de la vue actuelle (toutes les catégories sur la vue d'ensemble, ou la catégorie ouverte) et désélectionne le reste de cette vue. C'est une action explicite, et vous pouvez regarder ce qu'elle a choisi avant de nettoyer.
- `A` sélectionne tout ce qui est nettoyable dans la vue, `n` ne sélectionne rien, `i` inverse.

Appuyez ensuite sur `d`. La fenêtre de confirmation montre ce qui sera supprimé et le total, et demande de taper `yes` quand un élément caution est inclus. Pour répéter à blanc, ajoutez `--dry-run` (`-n`).

Si vous voulez que les éléments recommandés soient sélectionnés dès le départ, demandez-le : `lu-cleaner clean --smart` (ou `lu-cleaner --smart`) ouvre le sélecteur avec eux sélectionnés, et `clean --yes --smart` les nettoie sans sélecteur. Les [raccourcis clavier](/lu-cleaner/fr/reference/keyboard-shortcuts/) listent toutes les touches.

### Pourquoi un élément n'est-il pas recommandé ?

La [sélection intelligente](/lu-cleaner/fr/concepts/risk-and-smart-select/) (smart select : la colonne `★`, la touche `a`, `--smart`) ne choisit que ce que vous nettoieriez sans hésiter. Un élément n'est **pas** recommandé lorsque :

- son risque est <span class="risk caution">caution</span> ou <span class="risk never">never</span> ;
- il porte un avertissement : application en cours d'exécution, modifications non commitées, commits non poussés, ouvert dans un éditeur, sur un volume externe… ;
- son scanner y oppose un veto parce que sa suppression est irréversible ou que son origine est incertaine : vider la Corbeille, des données orphelines qui peuvent encore être voulues, un profil de navigateur avec des identifiants enregistrés… (voir [Vetos des scanners](/lu-cleaner/fr/concepts/risk-and-smart-select/#vetos-des-scanners)) ;
- il est encore en cours de mesure ;
- c'est un artefact de build d'un projet sur lequel vous avez travaillé au cours des dernières 24 heures ;
- c'est un élément <span class="risk safe">safe</span> de moins de 1 Mo ;
- c'est un élément <span class="risk moderate">moderate</span> utilisé plus récemment que `stale_after` (14 jours par défaut), sauf si son scanner sait qu'il s'agit d'un poids mort (versions remplacées, données de dossiers supprimés).

Les worktrees suivent leur propre règle : ils sont recommandés lorsqu'ils sont propres, poussés et inactifs depuis 7 jours, ou fusionnés dans la branche par défaut et inactifs depuis 1 jour. Pour modifier le seuil d'inactivité :

```toml
# ~/.config/lu-cleaner/config.toml
stale_after = "30d"
```

Vous pouvez toujours sélectionner vous-même un élément non recommandé.

### Pourquoi les tailles diffèrent-elles du Finder ou de `du` ?

lu-cleaner indique l'espace alloué sur le disque (en blocs), ne compte qu'une fois un fichier qui a plusieurs liens physiques et, lorsque des données sont partagées avec d'autres dossiers par des liens physiques ou des clones APFS, montre ce que la suppression libérerait réellement. Le Finder affiche la taille logique des fichiers. Pour les fichiers creux (sparse) comme les images disque de VM, et pour les dossiers remplis de petits fichiers, les deux peuvent beaucoup différer. Les chiffres de lu-cleaner sont proches de ceux de `du -sh`.

### J'ai fait le ménage, mais mon disque est toujours plein

Lancez `lu-cleaner doctor`. Les causes habituelles sont les instantanés locaux APFS, la Corbeille, les fichiers partagés avec un store de gestionnaire de paquets et les applications qui gardent ouverts des fichiers supprimés. Voir [Pourquoi l'espace libéré n'apparaît pas](/lu-cleaner/fr/guides/disk-space-not-freed/).

## Configuration et historique

### Comment protéger un dossier ?

Ajoutez-le à votre fichier de configuration. Ouvrez le fichier avec `lu-cleaner config edit`, ou créez un exemple commenté avec `lu-cleaner config init`.

```toml
# ~/.config/lu-cleaner/config.toml

# Jamais proposés ni nettoyés, pas plus que les dossiers qui les contiennent.
protect = ["~/dev/shop-app/ios/Pods", "~/.ollama/models"]

# Jamais scannés et jamais nettoyés (correspondance par préfixe).
exclude = ["~/dev/archive"]

# Ignorer des catégories entières.
disabled_categories = ["containers"]
```

`protect` s'ajoute à la liste intégrée (clés SSH, identifiants, configurations des outils d'IA, keystores…). `exclude` accélère aussi les scans. Les chemins acceptent `~`, et les chemins relatifs sont résolus depuis votre dossier personnel. Vérifiez le résultat avec `lu-cleaner config show`. Voir [Configuration](/lu-cleaner/fr/reference/configuration/).

### Comment voir ce qui a été supprimé ?

```bash
lu-cleaner history              # les 30 dernières entrées, les plus récentes d'abord
lu-cleaner history --limit 0    # tout
lu-cleaner history --json
```

Chaque nettoyage ajoute une ligne par élément à `~/.local/state/lu-cleaner/history.jsonl` (ou sous `$XDG_STATE_HOME`) : heure, type d'élément, catégorie, nom, chemin ou commande (l'emplacement et les premiers chemins d'un élément groupé), méthode, statut, taille, octets réellement libérés, et erreur ou message. Les simulations ne sont pas enregistrées. L'historique vous dit ce qui a été supprimé, pas comment le récupérer.

### Comment ne nettoyer qu'un seul type de chose ?

Filtrez par [catégorie](/lu-cleaner/fr/concepts/categories/) avec `-c` ou par type d'élément avec `-k`, ou utilisez une commande dédiée :

```bash
lu-cleaner clean -c simulators            # sélecteur, simulateurs uniquement
lu-cleaner clean --yes -k node_modules --older-than 30d --dry-run
lu-cleaner worktrees                      # uniquement les worktrees git
lu-cleaner artifacts ~/dev/my-app         # uniquement les artefacts de ce projet
```

`lu-cleaner scan --json` affiche le `kind` de chaque élément.

### Pourquoi `clean --yes` refuse-t-il de s'exécuter ?

`clean --yes` refuse de « tout nettoyer ». Il exige un filtre restrictif : `--smart`, `--category/-c` ou `--kind/-k`. Un `-k` qui nomme un scanner couvrant plusieurs catégories (`-k catalog`) ne suffit pas à lui seul, et un nom d'outil n'est pas une catégorie (`-c cursor` est refusé : utilisez `-c ai` ou un type avec `-k`). Il écarte aussi les éléments <span class="risk caution">caution</span> et les éléments qui portent un avertissement, sauf si vous passez `--risk caution`, et liste ces derniers sous **Held back**. Ces règles existent pour qu'un script, une tâche cron ou un agent de code ne puisse pas effacer par accident vos sessions ou vos worktrees modifiés. Voir [Automatisation](/lu-cleaner/fr/guides/automation/).

## Corbeille, autorisations et macOS

### Que fait le mode Corbeille ?

Au lieu de supprimer, lu-cleaner déplace chaque chemin dans `~/.Trash`, en le nommant `name 2`, `name 3`… si le nom y est déjà pris (rien de ce qui se trouve déjà dans la Corbeille n'est jamais remplacé). Activez-le avec `--trash`, avec `use_trash = true` dans la configuration, ou avec la touche `t` dans le sélecteur ; `--trash=false` le désactive pour une exécution.

- **L'espace n'est pas libéré tant que vous n'avez pas vidé la Corbeille.** Les récapitulatifs et l'historique comptent les déplacements dans la Corbeille à part, jamais comme libérés, et vous le rappellent.
- **Rien n'est supprimé définitivement.** Seuls les fichiers et dossiers sont déplacés. Les commandes (comme `xcrun simctl delete`) et les suppressions de worktrees ne peuvent pas être annulées : elles sont donc ignorées, tout comme les éléments qui se trouvent déjà dans la Corbeille.
- Un chemin situé sur un autre volume ne peut pas être déplacé dans votre Corbeille, et cet élément échoue.

Utilisez-le lorsque vous voulez vérifier avant de vous engager, puis videz la Corbeille dans le Finder, ou avec l'élément « Trash » de lu-cleaner dans la catégorie `system` (en mode suppression).

### Faut-il l'accès complet au disque ?

Non, lu-cleaner fonctionne sans. macOS protège toutefois quelques dossiers : sans l'accès complet au disque, lu-cleaner ne peut pas mesurer `~/.Trash` ni vos sauvegardes d'iPhone/iPad, et `analyze` signale des entrées illisibles. Dans ce cas, l'élément Corbeille demande au Finder de vider la Corbeille.

Il ne touche jamais non plus aux données des autres applications (`~/Library/Containers/<app>`, `~/Library/Group Containers/<group>`) sans l'accès complet au disque : sur macOS 14 et versions ultérieures, le premier accès ouvrirait une demande d'autorisation qui bloquerait l'analyse. Ce qui s'y trouve, comme les journaux de Docker Desktop, est listé en rapport seul avec l'avertissement `needs Full Disk Access`, et l'analyseur affiche ces dossiers verrouillés.

Pour l'accorder, ouvrez **Réglages Système › Confidentialité et sécurité › Accès complet au disque**, activez votre application de terminal (Terminal, iTerm2, Ghostty…), puis redémarrez le terminal.

### Faut-il `sudo` ?

Non, et ne le lancez pas avec `sudo`. lu-cleaner travaille sur votre compte utilisateur : il ne supprime que dans votre dossier personnel et votre dossier temporaire utilisateur, et refuse les emplacements système. Les seules commandes qui nécessiteraient les droits d'administrateur, comme la suppression des instantanés locaux APFS, sont affichées par `doctor` pour que vous les lanciez vous-même.

### Fonctionne-t-il sous Linux ou Windows ?

Non. lu-cleaner est réservé à macOS. Il s'appuie sur APFS, les appels système de macOS, `~/Library`, `xcrun simctl`, `tmutil` et la façon dont les applications macOS stockent leurs données. Les binaires publiés sont universels (Apple silicon et Intel).

### Et avec un SSD externe ?

lu-cleaner ne supprime jamais rien en dehors de votre dossier personnel et de votre dossier temporaire, donc rien sur `/Volumes/…` n'est jamais supprimé. Les éléments dont l'emplacement réel se trouve sur un autre disque (un SDK Android, un stockage de modèles ou un projet déplacé sur un SSD externe avec un lien symbolique de retour) sont signalés uniquement, avec la mention « on external volume — no internal gain ». Les worktrees dont le dépôt principal se trouve sur un volume non monté reçoivent le statut « unknown » et ne sont pas touchés. Pour nettoyer un disque externe, utilisez le Finder ou un analyseur de disque.

### Faut-il quitter mes applications d'abord ?

Cela aide. Les éléments qui appartiennent à une application (Xcode, Simulator, Cursor, Codex, l'application ChatGPT, Claude desktop, émulateurs Android…) affichent un avertissement comme « Cursor is running — quit it before cleaning » tant que cette application tourne. La sélection intelligente ne les choisit jamais, `clean --yes` les retient, et ils sont ignorés pendant le nettoyage. Tout le reste est nettoyé normalement. `lu-cleaner doctor` liste les applications en cours d'exécution qui bloquent le nettoyage.

### lu-cleaner envoie-t-il des données quelque part ?

Les [rapports d'erreurs techniques](../../guides/diagnostics/) sont facultatifs et désactivés sans consentement explicite. Ils contiennent des codes fixes et la pile du code de lu-cleaner, sans chemins personnels, contenu de fichiers, logs bruts ni identifiant permanent d'utilisateur ou de machine. Les commandes `diagnostics status`, `enable`, `disable` et `export` permettent de les contrôler. Sans accord, aucune requête de diagnostic. Les outils locaux et leurs commandes de nettoyage peuvent avoir leur propre comportement réseau.

## Autres outils

### En quoi est-il différent de mole, CleanMyMac ou npkill ?

- **CleanMyMac** et **mole** entretiennent l'ensemble du Mac : fichiers inutiles du système, désinstallation d'applications, analyseur. Ils couvrent aussi certains caches de développement.
- **npkill** trouve les `node_modules` (ou d'autres noms de dossiers) dans n'importe quel projet, sur n'importe quel OS.
- **lu-cleaner** est spécialisé pour les machines macOS de développeurs qui travaillent sur des projets React Native, iOS et Android avec des agents IA. Il connaît le statut des worktrees, le fonctionnement interne des simulateurs, les paquets du SDK Android, les stores des gestionnaires de paquets et les données des outils d'IA, et revérifie chaque chemin avant de le supprimer. Il ne désinstalle pas d'applications et ne nettoie pas les dossiers système.

Ils se complètent bien. Voir la [comparaison](/lu-cleaner/fr/about/comparison/).
