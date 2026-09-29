---
title: Pourquoi l'espace libéré n'apparaît pas
description: Pourquoi macOS peut afficher moins d'espace libre que ce que lu-cleaner a libéré (Corbeille, instantanés locaux APFS, liens physiques et clones, fichiers ouverts, espace purgeable, caches recréés, swap) et comment vérifier et corriger chaque cause.
sidebar:
  order: 7
---

Vous avez nettoyé 20 Go, mais le Finder ou `df` en affiche beaucoup moins. En général, rien ne s'est mal passé. Sur macOS, supprimer un fichier et récupérer ses blocs sont deux événements distincts, et plusieurs choses peuvent s'intercaler entre les deux.

Le moyen le plus rapide de savoir laquelle s'applique :

```bash
lu-cleaner doctor
```

## Estimé ou mesuré

Après un nettoyage, lu-cleaner affiche deux chiffres :

```text frame="terminal" title="lu-cleaner clean --yes --smart"
✓ 42 items cleaned · 18.4 GB freed (estimated)
  Disk free: 21.3 GB → 27.4 GB (6.10 GB measured)
  Less space than expected? Run 'lu-cleaner doctor' (snapshots, Trash, open files…).
```

| Chiffre | Ce qu'il représente |
|---|---|
| **Estimé** (`estimated`) | La somme, sur les éléments supprimés définitivement, de ce que la suppression de chacun devrait libérer : sa taille allouée sur le disque, moins les données qu'il partage avec des fichiers situés en dehors de lui par des liens physiques ou des clones APFS (voir [plus bas](#liens-physiques-et-clones-apfs)). Les déplacements dans la Corbeille n'en font pas partie. |
| **Mesuré** (`measured`) | L'augmentation de l'espace libre du volume entre le début et la fin de l'exécution, telle que la rapporte le système de fichiers (`statfs`, le même chiffre que celui de `df`). |

Les deux correspondent rarement à l'octet près. Le chiffre mesuré inclut aussi tout ce qui s'est passé sur le disque pendant l'exécution : un build qui écrit des fichiers, un téléchargement, le swap qui grossit. Il peut même être négatif. Lorsque l'estimation dépasse 1 Go et que le gain mesuré en représente moins de la moitié, lu-cleaner suggère de lancer `doctor`. Le sélecteur interactif affiche les deux mêmes lignes (« Estimated freed » et « Measured freed ») dans son récapitulatif.

Avec `--json`, le récapitulatif contient `estimated_freed`, `trashed` (octets déplacés dans la Corbeille), `measured_freed`, `disk_before` et `disk_after` (en octets). Voir [Sortie JSON](/lu-cleaner/fr/reference/json-output/). Une simulation (`--dry-run`) ne supprime rien, elle ne mesure donc rien. `lu-cleaner history` conserve l'estimation de chaque élément passé dans sa colonne `FREED`.

## `lu-cleaner doctor`

`doctor` vérifie toutes les causes courantes en une seule passe et ne supprime jamais rien :

```text frame="terminal" title="lu-cleaner doctor"
Disk
  █████████████████████████░░░░░ 84% used · 160 GB free of 995 GB

APFS local snapshots (2)
  com.apple.TimeMachine.2026-09-27-101010.local
  com.apple.TimeMachine.2026-09-28-111512.local
  They pin the blocks of deleted files: that space comes back only when they
  expire (about 24h) or are thinned. To reclaim it now (lu-cleaner never runs these):
    tmutil thinlocalsnapshots / 999999999999 4
    sudo tmutil deletelocalsnapshots 2026-09-27-101010

Trash
  3.20 GB in 1 204 files — empty it to free the space

Running apps that block cleaning
  Simulator  booted simulators and runtimes cannot be deleted
  Codex      an agent may be working in a worktree

Space by category
  CATEGORY            SIZE  ITEMS  RECOMMENDED
  …
```

| Section | Ce qu'elle vous indique |
|---|---|
| **Disk** | Le pourcentage utilisé et l'espace réellement libre du volume qui contient votre dossier personnel. |
| **APFS local snapshots** | Les instantanés locaux Time Machine qui maintiennent en vie des blocs supprimés, avec les commandes pour les retirer. |
| **Trash** | Le volume occupé par `~/.Trash`. Sa lecture nécessite l'accès complet au disque (voir plus bas). |
| **Running apps that block cleaning** | Xcode, Simulator, émulateur Android, Android Studio, Cursor, VS Code, Docker, Codex, Claude : ce que chacun garde occupé. |
| **Space by category** | Un court résumé de scan, avec ce que la sélection intelligente (smart select) libérerait. Désactivez-le avec `--no-scan`. |
| **Why isn't my space freed?** | Un rappel des causes décrites ci-dessous. |

`lu-cleaner doctor --json` renvoie le même rapport pour les scripts. Voir la [référence de `doctor`](/lu-cleaner/reference/commands/doctor/).

La suite de cette page passe en revue chaque cause, de la plus fréquente à la plus rare.

## La Corbeille

Les fichiers placés dans la Corbeille occupent toujours de l'espace disque tant qu'elle n'est pas vidée. Cela vaut pour tout ce que vous faites glisser dans la Corbeille depuis le Finder, pour le mode Corbeille de lu-cleaner (`--trash`, `use_trash = true` ou la touche `t` dans le sélecteur), et pour les archives Xcode, que lu-cleaner déplace toujours dans la Corbeille. lu-cleaner compte ces déplacements à part de ce qu'il a libéré :

```text frame="terminal" title="lu-cleaner clean --yes --smart --trash"
✓ 12 items cleaned · 4.20 GB moved to the Trash (estimated)
  Disk free: 21.3 GB → 21.3 GB (0 B measured)
  Space is only freed once the Trash is emptied.
```

Le mode Corbeille ne supprime jamais rien définitivement : les suppressions de worktrees et les commandes (`simctl`, `docker`, `brew`…) sont ignorées, tout comme les éléments qui se trouvent déjà dans la Corbeille.

Pour vider la Corbeille, utilisez le Finder, ou laissez lu-cleaner s'en charger. La Corbeille est un élément de la catégorie `system`. La vider est irréversible, donc la sélection intelligente ne la recommande jamais :

```bash
lu-cleaner clean -c system          # sélectionnez "Trash (N items)"
lu-cleaner clean --yes -k trash --dry-run
```

:::note[Accès complet au disque]
macOS protège `~/.Trash`. Sans l'accès complet au disque pour votre terminal, lu-cleaner ne peut ni en lister le contenu ni le mesurer. Il affiche alors « Trash (size unknown — needs Full Disk Access) », et le nettoyage de cet élément demande au Finder de vider la Corbeille (le Finder peut vous demander l'autorisation Automatisation). Pour accorder l'accès, ouvrez **Réglages Système › Confidentialité et sécurité › Accès complet au disque**, activez votre application de terminal, puis redémarrez-la.

Sans l'accès complet au disque, lu-cleaner ne lit jamais non plus l'intérieur des conteneurs des autres applications (`~/Library/Containers/<app>`, `~/Library/Group Containers/<group>`) : macOS bloquerait le scan sur une demande d'autorisation. Les éléments qui s'y trouvent sont signalés comme nécessitant l'accès complet au disque, et les images disque de Docker Desktop et d'OrbStack ne sont pas listées.
:::

## Instantanés locaux APFS

Lorsque Time Machine est activé, macOS prend des instantanés locaux du disque de démarrage, environ toutes les heures. Un instantané conserve chaque bloc qui existait au moment où il a été pris. Si vous supprimez un fichier ensuite, ses blocs restent alloués jusqu'à la disparition de tous les instantanés qui y font référence. C'est la principale raison pour laquelle le gain mesuré peut être proche de zéro juste après un gros nettoyage.

Les instantanés expirent d'eux-mêmes au bout d'environ 24 heures, et macOS les allège lorsqu'il manque d'espace. Pour récupérer l'espace tout de suite :

```bash
# les lister
tmutil listlocalsnapshots /

# demander à macOS de les alléger autant que possible (sans sudo)
tmutil thinlocalsnapshots / 999999999999 4

# ou en supprimer un par sa date (la partie après com.apple.TimeMachine.)
sudo tmutil deletelocalsnapshots 2026-09-27-101010
```

lu-cleaner liste les instantanés dans `doctor` et sous la forme d'un élément « Time Machine local snapshots », signalé uniquement, dans la catégorie `system`. Il n'exécute jamais ces commandes. Supprimer un instantané supprime un point de restauration local, et `deletelocalsnapshots` nécessite les droits d'administrateur : cette décision vous revient donc. Les sauvegardes présentes sur votre disque Time Machine ne sont pas concernées.

## Liens physiques et clones APFS

Plusieurs outils partagent le contenu des fichiers entre dossiers au lieu de le copier :

| Outil | Mécanisme |
|---|---|
| **pnpm** | Lie les paquets de son store (`~/Library/pnpm/store`) dans chaque `node_modules`, sous forme de liens physiques (hardlinks) ou de clones APFS. |
| **bun** | Clone les fichiers de son cache d'installation dans `node_modules` sur APFS. |
| **Yarn Berry** avec `nmMode: hardlinks-global` | Crée des liens physiques vers les fichiers de son store global. |
| **Finder, `cp -c`, simulateurs copiés** | Clones APFS (copie à l'écriture). |

Supprimer l'un des côtés ne libère que les blocs que personne d'autre n'utilise. Si vous supprimez le `node_modules` d'un projet pnpm, les fichiers restent dans le store. Si vous purgez le store, les fichiers restent dans les projets qui les utilisent.

lu-cleaner mesure les deux mécanismes et calcule ce que la suppression d'un élément libère réellement :

- un fichier avec des **liens physiques** n'est compté qu'une fois, et ne compte pour rien quand un autre lien se trouve en dehors de l'élément ;
- un **clone APFS** ne compte que ses blocs privés, sauf si tous les fichiers qui partagent ses données se trouvent dans l'élément.

Lorsque le résultat est inférieur à la taille, l'élément l'indique (« Shared with other files (hardlinks or APFS clones of the pnpm store): only 120 MB is really freed »), la taille affiche un `*`, la sortie JSON comporte un champ `reclaim`, et les totaux comme l'estimation utilisent le chiffre réel.

Cela reste une estimation. Les données partagées entre deux éléments mesurés séparément, comme le cache de bun et un `node_modules` installé à partir de lui, ne sont libérées que lorsque les deux sont supprimés. Pour récupérer réellement l'espace, supprimez les deux côtés : les dossiers `node_modules` des anciens projets, puis les paquets non référencés du store (`pnpm store prune`, que lu-cleaner propose comme élément de commande <span class="risk safe">safe</span>). Voir [Outils JavaScript](/lu-cleaner/fr/guides/js-toolchain/).

## Fichiers encore ouverts

Un processus qui garde un fichier ouvert maintient ses blocs alloués après la suppression du fichier, jusqu'à ce qu'il le ferme ou se termine. Sur une machine de développement, les coupables habituels sont le Simulator, Xcode, le démon Gradle, Docker, un serveur de développement et un agent IA qui tourne encore dans un worktree.

```bash
lsof +L1                  # fichiers supprimés encore ouverts, avec le processus qui les détient
./gradlew --stop          # arrêter les démons Gradle (dans un projet Android)
xcrun simctl shutdown all # éteindre les simulateurs démarrés
```

Quitter l'application, ou redémarrer, libère l'espace. lu-cleaner évite la plupart de ces cas en amont. Il ignore un dossier dans lequel un processus travaille ou depuis lequel il exécute un programme, ainsi qu'une base SQLite qu'un processus garde ouverte. Les éléments qui appartiennent à une application en cours d'exécution attendent que vous la quittiez. `doctor` liste les applications en cours d'exécution qui bloquent un nettoyage.

## Espace purgeable : Finder ou `df`

Le chiffre « Disponible » du Finder inclut l'espace **purgeable** : instantanés locaux, fichiers iCloud qui peuvent être téléchargés de nouveau, certains caches. macOS ne libère l'espace purgeable que lorsque quelque chose en a besoin. Le Finder peut donc afficher bien plus de place que vous ne pouvez réellement en utiliser, et son chiffre peut varier sans aucune suppression.

lu-cleaner, comme `df`, indique l'espace réellement libre (`f_bavail`, les blocs disponibles pour votre utilisateur) :

```bash
df -h ~
```

Comparez les chiffres de lu-cleaner à ceux de `df`, pas à ceux du Finder. Il n'y a rien à « nettoyer » dans l'espace purgeable : vous pouvez seulement accélérer les choses en allégeant les instantanés (voir plus haut).

## Les applications recréent leurs caches

Un élément <span class="risk safe">safe</span> est un cache que son outil reconstruit automatiquement. Cela signifie aussi que l'espace est de nouveau occupé dès que l'outil se relance : Xcode reconstruit DerivedData au build suivant, Metro son cache au bundle suivant, Cursor et les autres applications Electron leurs caches au lancement suivant.

C'est normal. Supprimer un cache que vous utilisez tous les jours ne vous apporte qu'un prochain démarrage plus lent. C'est pourquoi la sélection intelligente se concentre sur les éléments inactifs : les éléments <span class="risk moderate">moderate</span> ne sont recommandés qu'après `stale_after` (14 jours par défaut) sans utilisation. Pour ne cibler que ce que vous n'avez pas touché depuis un moment :

```bash
lu-cleaner clean --yes --smart --older-than 30d --dry-run
```

## Swap et mises à jour en attente

Les volumes APFS partagent l'espace libre de leur conteneur. Les fichiers de swap du volume VM (`/System/Volumes/VM`) grossissent sous la pression mémoire (simulateurs, émulateurs Android, applications Electron, agents IA), et ils prennent aussi de l'espace libre à votre volume de données.

```bash
sysctl vm.swapusage
```

lu-cleaner signale le swap au-delà de 1 Go sous le nom « Swap files (VM volume) », avec le risque <span class="risk never">never</span> : ne supprimez jamais ces fichiers. Quittez les applications gourmandes en mémoire ou redémarrez pour réduire le swap. Les mises à jour de macOS téléchargées mais pas encore installées sont aussi signalées (« Staged macOS updates »). Installez ou ignorez la mise à jour pour récupérer cet espace.

## L'image disque de Docker

Docker Desktop stocke les images, les conteneurs et les volumes dans une seule image disque (`Docker.raw`) qui ne rétrécit pas d'elle-même. Supprimer des fichiers dans un conteneur ne rend pas d'espace à macOS. Faites le ménage depuis Docker, puis redémarrez-le :

```bash
docker system prune
docker builder prune
```

## Éléments sur un volume externe

Certains éléments se trouvent sur un autre disque : un SDK Android ou un stockage de modèles déplacé sur un SSD externe avec un lien symbolique de retour, ou un projet sur `/Volumes/…`. Les supprimer ne libérerait aucun espace sur votre disque interne, donc lu-cleaner les signale comme « on external volume — no internal gain » et ne les nettoie jamais.

## Aide-mémoire

| Symptôme | Cause probable | Que faire |
|---|---|---|
| Gain mesuré proche de zéro juste après un gros nettoyage | Instantanés locaux APFS | Attendre environ 24 h, ou `tmutil thinlocalsnapshots / 999999999999 4` |
| lu-cleaner a indiqué « moved to the Trash » | Mode Corbeille, ou archives Xcode | Vider la Corbeille |
| `node_modules` supprimé mais peu d'espace libéré | Partage par pnpm / bun / Yarn Berry | Purger aussi le store (`pnpm store prune`) |
| L'espace revient après avoir quitté une application | Fichiers ouverts | Quitter l'application, `./gradlew --stop`, éteindre les simulateurs |
| Le Finder affiche plus d'espace libre que lu-cleaner | Espace purgeable | Se fier à `df -h ~` |
| L'espace libre diminue de nouveau le lendemain | Caches recréés, swap | Ne nettoyer que les éléments inactifs (`--older-than`), redémarrer pour réinitialiser le swap |
| Docker occupe toujours beaucoup de place | `Docker.raw` ne rétrécit jamais | `docker system prune`, redémarrer Docker |

## Voir aussi

- [Modèle de sécurité](/lu-cleaner/fr/concepts/safety/)
- [Fonctionnement](/lu-cleaner/fr/concepts/how-it-works/)
- [Référence de `lu-cleaner doctor`](/lu-cleaner/reference/commands/doctor/)
- [FAQ](/lu-cleaner/fr/about/faq/)
