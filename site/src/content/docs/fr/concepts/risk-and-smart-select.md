---
title: Niveaux de risque et sélection intelligente
description: Les quatre niveaux de risque, les règles exactes que la sélection intelligente applique pour recommander des éléments (optionnelle, avec la touche a ou --smart), comment choisir vous-même ce qu'il faut supprimer, et les cinq méthodes de nettoyage.
sidebar:
  order: 2
---

Chaque élément trouvé par lu-cleaner porte un **risque** (ce que vous perdez s'il est supprimé) et une **méthode** (la façon dont il est nettoyé). La sélection intelligente (smart select) combine le risque, l'âge et le jugement propre du scanner pour recommander ce qu'un développeur prudent supprimerait sans hésiter. C'est quelque chose que vous demandez, jamais quelque chose qui se produit tout seul : rien n'est sélectionné quand le sélecteur s'ouvre.

## Les quatre niveaux de risque

| Risque | Signification | Exemples |
|---|---|---|
| <span class="risk safe">safe</span> | Cache pur. Régénéré automatiquement, il ne contient aucune donnée utilisateur. Au pire, le prochain build ou la prochaine installation est plus lent. | Cache npm (`~/.npm/_cacache`), cache Metro, DerivedData de Xcode, journaux du démon Gradle, dossier `build/` Android d'un projet, enregistrements d'écran XCTest, un simulateur indisponible dont le runtime a disparu pour de bon |
| <span class="risk moderate">moderate</span> | Régénérable, mais le récupérer coûte du temps ou de la bande passante. | `node_modules`, `ios/Pods`, un worktree git propre (sa branche est conservée), runtimes de simulateurs (environ 8 Go à retélécharger), versions de node inutilisées, distributions du wrapper Gradle, NDK |
| <span class="risk caution">caution</span> | Peut contenir des données ou un état auxquels vous tenez. | Un worktree avec des modifications non commitées ou des fichiers secrets ignorés par git (`.env`, keystores…), un dossier de worktree orphelin, un simulateur avec des applications installées et leurs données, des sessions Claude Code ou Codex de plus de 30 jours, des sauvegardes de bases de conversations, des archives Xcode, des émulateurs Android (AVD), le cache de Yarn Berry quand des projets Plug'n'Play s'exécutent à partir de lui, un virtualenv sans fichier de dépendances, de gros dossiers ignorés par git qu'aucune règle ne connaît, d'anciens installateurs dans `~/Downloads` |
| <span class="risk never">never</span> | Protégé ou hors de portée. Affiché à titre d'information, jamais proposé à la suppression. | Snapshots locaux de Time Machine, fichiers de swap, mises à jour macOS en attente, bases de conversations des outils d'IA (état global de Cursor, Conductor, Warp), un dossier `build/` qui s'est révélé contenir du code source |

Un scanner peut relever le risque d'un élément selon ce qu'il observe. Par exemple, un worktree est <span class="risk moderate">moderate</span> par défaut et devient <span class="risk caution">caution</span> dès qu'il contient des modifications non commitées, qu'il est verrouillé, qu'il contient des fichiers secrets ignorés par git qui n'existent nulle part ailleurs (`.env`, `.npmrc`, keystores, clés de signature…), qu'il contient un autre dépôt, qu'il est ouvert dans un éditeur, qu'un processus travaille à l'intérieur ou qu'une session d'agent y est active. Une version de node devient <span class="risk caution">caution</span> quand c'est votre version par défaut ou le `node` de votre `PATH`.

### Ce que change chaque niveau

| | Sélecteur | Sélection intelligente | `clean --yes` |
|---|---|---|---|
| <span class="risk safe">safe</span> | sélectionnable | oui, à partir de 1 Mio | inclus |
| <span class="risk moderate">moderate</span> | sélectionnable | oui, une fois inutilisé depuis `stale_after` | inclus |
| <span class="risk caution">caution</span> | sélectionnable, à confirmer en tapant `yes` | jamais | seulement avec `--risk caution` |
| <span class="risk never">never</span> | non sélectionnable | jamais | jamais |

`clean --yes` traite les éléments qui portent un avertissement comme des éléments caution, quel que soit leur risque : sans `--risk caution`, ils sont laissés de côté et listés sous **Held back**. `--risk never` est refusé, puisque les éléments <span class="risk never">never</span> sont en rapport seul.

Deux détails du sélecteur protègent davantage les éléments caution : appuyer sur <kbd>space</kbd> sur une catégorie sélectionne tous ses éléments **sauf** les éléments caution (ouvrez la catégorie pour les choisir un par un), et la boîte de confirmation vous demande de taper `yes` dès que la sélection contient au moins un élément caution, même situé dans un élément sélectionné, ou un élément qui exige `--force`.

## Sélection intelligente

La sélection intelligente est **optionnelle**. Quand le sélecteur s'ouvre, rien n'est sélectionné, et cela reste ainsi tant que vous n'avez pas choisi : <kbd>space</kbd> sélectionne un élément, <kbd>a</kbd> lance la sélection intelligente sur la vue actuelle, et `--smart` la lance pour vous dès la fin de l'analyse. La raison est simple : supprimer doit toujours être le résultat d'un choix explicite. Si les éléments recommandés étaient déjà sélectionnés, un simple <kbd>d</kbd> suivi de <kbd>enter</kbd> supprimerait tout ce que la sélection intelligente a trouvé, que vous l'ayez regardé ou non. En la demandant vous-même, vous savez ce qui a été choisi, et le pied de page ainsi que la fenêtre de confirmation affichent le nombre d'éléments et le total avant que quoi que ce soit ne soit supprimé.

La sélection intelligente est une seule fonction, appliquée à chaque élément, dans cet ordre :

1. **Jamais** quand l'élément ne peut pas être nettoyé (rapport seul, non sélectionnable ou risque <span class="risk never">never</span>), qu'il est encore en cours de mesure, qu'il **porte un avertissement**, qu'il est <span class="risk caution">caution</span> ou que **son scanner y oppose un veto** (voir ci-dessous).
2. **Jamais** pour les artefacts (`node_modules`, `Pods`, builds…) d'un projet actif dans les dernières 24 heures : vous travaillez dessus.
3. **Oui** quand le scanner lui-même recommande l'élément (voir ci-dessous).
4. **Oui** pour les éléments <span class="risk safe">safe</span> d'au moins 1 Mio.
5. **Oui** pour les éléments <span class="risk moderate">moderate</span> dont la dernière utilisation est connue et plus ancienne que `stale_after` (14 jours par défaut).
6. Sinon, non.

L'ordre compte. Un avertissement ou un veto bloque tout, y compris la recommandation du scanner lui-même. C'est voulu : les avertissements sont la façon dont les scanners disent « regardez d'abord ceci ». Quelques exemples :

- `Xcode is running — quit it before cleaning` exclut DerivedData de la sélection intelligente jusqu'à ce que vous quittiez Xcode et relanciez l'analyse.
- `2 unpushed commits (branch feature/login is kept)` exclut un worktree, même si le supprimer ne ferait pas perdre les commits.
- `pinned by 1 project file(s): …` exclut une version de node que demande le `.nvmrc` d'un projet.
- `newest iOS symbols — kept for on-device debugging` exclut le dossier DeviceSupport le plus récent.

### Vetos des scanners

Certains éléments ne sont jamais recommandés, quels que soient leur risque, leur taille et leur âge, parce que leur suppression est irréversible ou que leur origine est incertaine. Vous pouvez toujours les sélectionner vous-même :

- vider la Corbeille ;
- les données orphelines dont le propriétaire peut revenir : dossiers de worktrees orphelins (qui exigent aussi `--force`), données Claude Code d'un dossier supprimé écrites au cours des 30 derniers jours ou dont le dossier peut être restauré (elles sont alors <span class="risk caution">caution</span>), données Cursor de workspaces supprimés qui sont récentes ou contiennent des conversations ;
- le profil de navigateur d'Antigravity d'une application désinstallée (identifiants enregistrés, cookies) ;
- un simulateur indisponible qui peut revenir (runtime encore sur le disque, autre Xcode sélectionné) ou qui contient des applications avec des données ;
- le cache global de Yarn Berry quand des projets Plug'n'Play s'exécutent à partir de lui, un store pnpm dont le store virtuel global est lié par des projets, `pnpm store prune` quand un projet enregistré est inaccessible ;
- les `keep_latest` versions de node les plus récentes (par gestionnaire de versions) et runtimes de simulateurs les plus récents (par plateforme), et toutes les versions de node quand la liste des processus ne peut pas être lue ;
- les anciens réglages d'Android Studio tant qu'une version nouvellement installée ne les a pas encore importés ;
- un dossier de build hors de git dont on n'a que de faibles indices qu'il est généré.

Les sauvegardes de bases de conversations (sauvegardes de la base d'état de Cursor, sauvegardes de réparation par Codex des bases de mémoires et d'objectifs) sont <span class="risk caution">caution</span>, et donc jamais recommandées non plus. Dans le sélecteur, un élément qui exige `--force` n'est jamais choisi par la sélection intelligente, même quand vous avez lancé lu-cleaner avec `--force`.

### Recommandations des scanners

Les règles 4 et 5 sont génériques. Les scanners en savent plus : ils peuvent recommander un élément que les règles génériques manqueraient (un élément moderate plus récent que `stale_after`, un élément safe de moins de 1 Mio), et ils excluent un élément en lui attachant un avertissement ou en relevant son risque. Voici quelques recommandations représentatives des scanners, qui s'ajoutent aux règles génériques :

| Scanner | Recommandé quand |
|---|---|
| worktrees | Propre, poussé et inactif depuis 7 jours, ou fusionné dans la branche par défaut et inactif depuis 1 jour, sans avertissement. Les métadonnées de worktrees périmées (`git worktree prune`) sont toujours recommandées. |
| artifacts | Une sortie safe (dossier de build, cache) d'un projet inactif depuis 24 heures. Un cache de projet Yarn Berry que `.yarnrc.yml` n'utilise plus. |
| apple | DerivedData d'un workspace qui n'existe plus. Simulateurs indisponibles dont le runtime a disparu pour de bon et qui ne contiennent aucune application. Enregistrements XCTest de simulateurs éteints. Un runtime qu'aucun simulateur n'utilise quand un runtime plus récent de la même plateforme est installé. |
| android | Émulateurs et images système x86 sur Apple Silicon. Images système qu'aucun AVD n'utilise. Paquets du SDK (NDK, build-tools, platforms) qu'aucun projet analysé n'utilise ; la version installée la plus récente n'est jamais recommandée. Téléchargements Gradle interrompus. |
| js | Versions de node que rien ne référence (ni version par défaut, ni dans le `PATH`, ni épinglées, ni en cours d'exécution, sans paquet global installé uniquement là), au-delà des `keep_latest` plus récentes. Paquets npx inutilisés depuis 14 jours. Builds Expo Go remplacés. Navigateurs Playwright dont aucun Playwright installé n'a besoin. Surveillances watchman sur des dossiers supprimés (les surveillances actives de Metro ou de Jest ne sont jamais touchées). |
| ai | Builds de CLI remplacés (Claude Code, cursor-agent, Copilot CLI…). Données Claude Code de dossiers qui n'existent plus, intactes depuis 30 jours. Données Cursor de workspaces supprimés, intactes depuis 30 jours et sans conversations. |
| system | `brew cleanup`. Mises à jour d'applications téléchargées. Cache des anciens builds de VS Code et versions d'extensions remplacées. Caches JetBrains de versions d'IDE que vous n'avez plus. |

La liste complète, type d'élément par type d'élément, se trouve dans la [référence des scanners](/lu-cleaner/fr/reference/scanners/). Les entrées du catalogue peuvent aussi porter ce drapeau : `Flipper leftovers` est recommandé quelle que soit sa taille, puisque React Native 0.74+ n'utilise plus Flipper.

### Où s'applique la sélection intelligente

| Commande | Comportement |
|---|---|
| `lu-cleaner`, `lu-cleaner clean`, `lu-cleaner artifacts`, `worktrees`, `devices` | Ouvre le sélecteur sans rien de sélectionné. Appuyez sur <kbd>a</kbd> pour lancer la sélection intelligente sur la vue actuelle. |
| Les mêmes commandes avec `--smart` | Ouvre le sélecteur ; à la fin de l'analyse, les éléments recommandés sont sélectionnés, sauf ceux que vous avez déjà cochés ou décochés pendant l'analyse. |
| `lu-cleaner scan` | Les éléments recommandés reçoivent une ★ dans la colonne `RECO` ; l'en-tête de chaque catégorie indique le volume recommandé. `scan --smart` ne liste qu'eux. |
| `lu-cleaner clean --yes --smart` | Nettoie exactement les éléments recommandés (dans la limite des autres filtres). |

```bash
lu-cleaner scan --smart                        # ce que choisirait la sélection intelligente, rien n'est supprimé
lu-cleaner clean --yes --smart --dry-run       # la même chose, sous forme de plan de nettoyage avec revérifications
lu-cleaner clean --yes --smart -c artifacts    # nettoie uniquement les artefacts de projet recommandés
```

`stale_after` est un réglage, pas un filtre. `--older-than 30d` masque tout ce qui a été utilisé dans les 30 derniers jours (et tout ce dont l'âge est inconnu), quel que soit le risque ; `stale_after = "30d"` change seulement le moment où les éléments moderate deviennent recommandés. Voir [Configuration](/lu-cleaner/fr/reference/configuration/).

## Choisir ce qu'il faut supprimer

La sélection intelligente est un point de départ que vous choisissez, jamais une obligation.

- **Toujours partir d'une sélection vide.** `lu-cleaner` et `lu-cleaner clean` ouvrent le sélecteur sans rien de sélectionné. Choisissez avec <kbd>space</kbd> (sur une catégorie : tous les éléments sauf les éléments caution), ou appuyez sur <kbd>a</kbd> pour sélectionner les éléments recommandés et les passer en revue.
- **Partir des éléments recommandés.** `lu-cleaner clean --smart` ouvre le sélecteur avec eux déjà sélectionnés. C'est la version explicite d'un appui sur <kbd>a</kbd> juste après l'analyse.
- **Ajuster une vue.** Dans le sélecteur, <kbd>n</kbd> désélectionne tous les éléments de la vue actuelle (la catégorie ouverte, ou la liste filtrée). <kbd>A</kbd> les sélectionne tous, <kbd>i</kbd> inverse la sélection, <kbd>space</kbd> bascule un seul élément, et <kbd>a</kbd> applique la sélection intelligente à la vue actuelle : éléments recommandés sélectionnés, tous les autres désélectionnés.
- **Répéter à blanc.** `--dry-run` (`-n`) montre ce qui serait nettoyé et ne supprime rien.
- **Scripter sans sélection intelligente.** `clean --yes` accepte n'importe quel filtre restrictif à la place de `--smart` :

```bash
lu-cleaner clean --yes -c artifacts --older-than 30d --dry-run   # tous les artefacts inactifs, recommandés ou non
lu-cleaner clean --yes -k node_modules --min-size 200MB --dry-run
lu-cleaner clean --yes -c worktrees --risk caution --dry-run     # les éléments caution et ceux qui portent un avertissement exigent un --risk explicite
```

Toutes les touches sont listées dans [Raccourcis clavier](/lu-cleaner/fr/reference/keyboard-shortcuts/).

## Méthodes de nettoyage

La méthode est choisie par le scanner pour chaque élément. Vous la voyez dans le panneau de détails du sélecteur, dans `scan --json` et dans la boîte de confirmation.

| Méthode | Ce qui se passe | Libère l'espace |
|---|---|---|
| `delete` | Le chemin est supprimé définitivement. Les liens symboliques sont supprimés, jamais suivis. Les dossiers en lecture seule (cache des modules Go, certains SDK) et les attributs de fichiers (flags) sont levés lors d'une seconde tentative. | Immédiatement |
| `trash` | Le chemin est déplacé dans `~/.Trash`, et renommé (`name 2`, `name 3`…) si une entrée du même nom s'y trouve déjà ; une entrée existante n'est jamais remplacée. Ne fonctionne que sur le même volume que votre dossier personnel. | Seulement quand vous videz la Corbeille |
| `command` | lu-cleaner exécute la commande de nettoyage propre à l'outil : `xcrun simctl delete <udid>`, `xcrun simctl runtime delete …`, `pnpm store prune`, `docker builder prune -a -f`, `brew cleanup --prune=all`, `go clean -cache`, `git worktree prune`… | À la fin de la commande |
| `worktree` | `git worktree remove` depuis le dépôt principal, puis suppression du dossier de tâche vide de l'outil. La branche est conservée. Refusé quand le worktree est verrouillé, contient des modifications non commitées ou des fichiers non suivis, porte des commits sur aucune branche (HEAD détachée) ou contient un autre dépôt, sauf avec `--force`. | Immédiatement |
| `report` | Rien. L'élément est purement informatif. | Non |

**Pourquoi des commandes ?** Certaines données appartiennent à un outil qui tient sa propre comptabilité. Supprimer un simulateur avec `xcrun simctl delete` garde cohérents les registres de CoreSimulator, ce que supprimer son dossier dans son dos ne garantit pas. `pnpm store prune` ne retire que les paquets qu'aucun projet ne référence, ce qu'un simple `rm` ne peut pas savoir. Quand un outil propose un nettoyage correct, lu-cleaner l'utilise.

### Pourquoi `delete` est la méthode par défaut

Le but du nettoyage est de récupérer de l'espace. Déplacer 40 Go dans la Corbeille ne libère rien : les fichiers restent sur le même disque jusqu'à ce que la Corbeille soit vidée, et un disque plein reste plein. Cela mélange aussi ce qu'a retiré lu-cleaner avec les fichiers que vous avez vous-même mis à la Corbeille : le jour où vous la videz, vous supprimez les deux d'un coup sans avoir revu ni l'un ni l'autre.

La suppression définitive est raisonnable ici en raison de ce que propose lu-cleaner : des caches et des sorties qui reviennent d'eux-mêmes, des données que vous avez confirmées élément par élément, et un pipeline qui revérifie chaque chemin juste avant de le supprimer (voir [Modèle de sécurité](/lu-cleaner/fr/concepts/safety/)).

Si vous préférez malgré tout un filet de sécurité, vous pouvez déplacer les éléments dans la Corbeille :

- `--trash` en ligne de commande, pour une exécution ;
- `use_trash = true` dans la configuration, pour toutes les exécutions ;
- <kbd>t</kbd> dans le sélecteur, pour activer ou désactiver le mode Corbeille pour le prochain nettoyage.

Le mode Corbeille ne supprime jamais rien définitivement. Il ne fait que déplacer des fichiers et des dossiers : les éléments de type worktree et commande sont ignorés (`not possible in Trash mode (it would delete permanently)`), tout comme les éléments qui se trouvent déjà dans `~/.Trash` (vider la Corbeille compris). `--trash=false` le désactive pour une exécution quand `use_trash` est activé. Les archives Xcode passent par la Corbeille même sans le mode Corbeille, car une archive ne peut pas être reconstruite à l'identique.

:::caution
Les déplacements dans la Corbeille ne sont jamais comptés comme libérés : les récapitulatifs et l'historique les totalisent à part et vous rappellent que l'espace n'est libéré qu'une fois la Corbeille vidée. Si `doctor` affiche une Corbeille volumineuse après un nettoyage, c'est là qu'est passé votre espace.
:::
