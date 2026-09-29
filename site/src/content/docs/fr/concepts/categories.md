---
title: Catégories
description: Les onze catégories dans lesquelles lu-cleaner regroupe les éléments, ce que chacune contient et l'espace qu'elle occupe généralement.
sidebar:
  order: 4
---

Les éléments sont regroupés en onze catégories. Elles organisent le premier écran du sélecteur et le rapport de `scan`, et ce sont elles que sélectionnent `--category` (`-c`) et le réglage `disabled_categories`.

```bash
lu-cleaner scan --summary              # une ligne par catégorie
lu-cleaner scan -c simulators,xcode    # seulement ces deux-là
lu-cleaner clean -c worktrees          # sélecteur limité aux worktrees
```

## Vue d'ensemble

| Identifiant | Titre affiché | Contenu | Aussi accepté par `-c` |
|---|---|---|---|
| `worktrees` | Worktrees | Worktrees git créés par les agents d'IA ou à la main | `wt`, `worktree` |
| `artifacts` | Project artifacts | `node_modules`, `ios/Pods`, builds iOS et Android, `.expo`, `dist`… dans vos projets | `artifact` |
| `simulators` | iOS Simulators | Appareils de simulateur, runtimes, enregistrements, journaux et caches | `sim`, `simulator` |
| `xcode` | Xcode | DerivedData, archives, DeviceSupport, CocoaPods, SwiftPM, installations supplémentaires de Xcode | |
| `android` | Android | Émulateurs (AVD), paquets du SDK, Gradle, Android Studio, JDK | |
| `ai` | AI tools | Données et caches de Claude Code, Claude desktop, Codex, Cursor agent, ChatGPT, Conductor… | |
| `js` | JS toolchain | Caches npm, Yarn, pnpm et bun, versions de node, Metro, Expo, navigateurs de test | |
| `ide` | IDEs | Caches et état de VS Code, Cursor (partie éditeur), Zed, JetBrains, Sublime | |
| `containers` | Containers | Docker, colima, Lima, OrbStack, `container` d'Apple | `container` |
| `langs` | Other toolchains | Caches Homebrew, Go, Rust, Python, Ruby, .NET, JVM | `lang` |
| `system` | System | Corbeille, caches de navigateurs et d'applications, journaux, `~/Downloads`, snapshots et swap de macOS | |

Les identifiants de catégorie ne sont pas sensibles à la casse. Vous pouvez en combiner plusieurs avec des virgules ou en répétant l'option : `-c js,ai` ou `-c js -c ai`.

Les noms d'outils ne sont pas des catégories : `-c cursor`, `-c docker` ou `-c node_modules` est refusé, avec une indication qui nomme la catégorie à laquelle l'outil appartient, parce que cette catégorie couvre aussi d'autres outils et qu'un `clean --yes -c cursor` nettoierait tous les outils d'IA. Pour vous limiter à un seul outil ou à un seul type d'élément, utilisez `--kind`/`-k` avec un type d'élément ou un identifiant de scanner (`lu-cleaner scan --json` affiche le `kind` de chaque élément).

## Tailles typiques

Les fourchettes ci-dessous sont indicatives, pour un Mac utilisé pour du développement React Native, iOS et Android avec des agents de code IA. Vos chiffres seront différents ; `lu-cleaner scan --summary` ou `lu-cleaner doctor` vous donne les vôtres.

| Catégorie | Total typique | Ce qui la fait grossir |
|---|---|---|
| Worktrees | 5 – 50 Go | Chaque tâche d'agent extrait à nouveau le dépôt, avec ses propres dépendances et builds natifs (1 à 3 Go par worktree React Native). |
| Artefacts de projet | 10 – 60 Go | Un `node_modules` + `Pods` + build Android par projet, et par paquet dans un monorepo. |
| Simulateurs iOS | 10 – 70 Go | Runtimes (environ 8 Go chacun), appareils avec des applications, enregistrements d'écran des tests d'interface que rien ne supprime jamais. |
| Xcode | 5 – 40 Go | Un dossier DerivedData par chemin de workspace, donc un par worktree ; 2 à 6 Go de DeviceSupport par version d'iOS. |
| Android | 10 – 40 Go | Images système (1,5 à 7 Go chacune), émulateurs, caches Gradle par version de Gradle, NDK. |
| Outils d'IA | 2 – 40 Go | Transcripts de sessions avec captures d'écran, anciens builds de CLI, modèles locaux, images de VM des applications de bureau. |
| Chaîne d'outils JS | 5 – 30 Go | Plusieurs caches de gestionnaires de paquets côte à côte, une installation de node par version, caches Metro et Jest. |
| IDE | 1 – 10 Go | Caches d'anciens builds d'éditeurs, versions d'extensions remplacées, index de serveurs de langage. |
| Conteneurs | 0 – 100 Go | Images Docker et cache de build dans un disque de VM qui ne rétrécit jamais tout seul. |
| Autres chaînes d'outils | 1 – 20 Go | Téléchargements et anciennes versions de Homebrew, caches Go et Cargo. |
| Système | 2 – 30 Go | Caches de navigateurs et leurs modèles d'IA embarqués, journaux d'applications, anciens installateurs dans `~/Downloads`, la Corbeille. |

## Worktrees

Tous les worktrees git liés trouvés dans vos racines de worktrees (`~/.codex/worktrees`, `~/.cursor/worktrees`, `~/conductor/workspaces`, `~/.claude/worktrees`…), dans vos racines de projets et par `git worktree list` dans chaque dépôt. Chaque élément indique l'outil qui l'a créé, la branche et un statut : `clean`, `dirty`, `unpushed`, `merged`, `locked`, `orphan` ou `unknown`. Les métadonnées de worktrees périmées (dossiers supprimés sans que git le sache) apparaissent sous la forme d'un élément `git worktree prune`.

Un worktree est supprimé en entier avec `git worktree remove`, qui conserve sa branche. Un `orphan` (dont les métadonnées git ont disparu, de façon vérifiée) est supprimé comme un simple dossier et exige `--force` ; un worktree dont le dépôt principal ne peut pas être lu, ou a été déplacé, est en rapport seul. Voir [Nettoyer les worktrees des agents IA](/lu-cleaner/fr/guides/ai-worktrees/).

## Artefacts de projet

Les sorties de build et les dépendances trouvées en parcourant vos racines de projets, à la manière de npkill, un élément par dossier : `node_modules`, `ios/Pods`, `ios/build`, `android/app/build`, `android/.gradle`, `.cxx`, `.expo`, `.next`, `.turbo`, `dist`, `coverage`, `target` de Rust, `.venv` de Python, `.yarn/cache` de Yarn Berry et des dizaines d'autres. Un dossier ne correspond que si un fichier marqueur se trouve à côté (`package.json` à côté de `node_modules`), les noms génériques comme `build` doivent être ignorés par git ou contenir des sorties de build, et un dossier qui contient une entrée `.git` n'est jamais proposé. Avec `--root` ou `lu-cleaner artifacts <dossier>`, seuls ces dossiers sont analysés.

Les gros dossiers ignorés par git qu'aucune règle ne reconnaît (200 Mo ou plus, sans compter les artefacts connus qu'ils contiennent) sont listés en <span class="risk caution">caution</span> pour que vous décidiez. Voir [Nettoyer les projets React Native](/lu-cleaner/fr/guides/react-native-projects/).

:::note
Les artefacts situés dans un worktree sont listés deux fois : dans la taille du worktree et comme artefacts à part entière, ce qui vous permet de garder le worktree et de ne supprimer que son `node_modules`. Si vous sélectionnez les deux, seul le worktree est traité, et les totaux ne comptent jamais deux fois les mêmes octets.
:::

## Simulateurs iOS

Appareils de simulateur (un élément par appareil, supprimé avec `xcrun simctl delete <UDID>`), appareils indisponibles (un élément par appareil là aussi, <span class="risk caution">caution</span> quand ils peuvent revenir avec une autre version de Xcode ou contiennent des applications avec des données), dossiers d'appareils orphelins que `simctl` ne connaît plus, runtimes (`xcrun simctl runtime delete`), ensembles d'appareils alternatifs (aperçus SwiftUI, clones des tests parallèles), enregistrements d'écran XCTest laissés dans les simulateurs, journaux unifiés et caches système des simulateurs éteints, caches CoreSimulator. Voir [Simulateurs iOS et Xcode](/lu-cleaner/fr/guides/ios-simulators-xcode/).

## Xcode

DerivedData, un élément par dossier (Xcode en crée un par chemin de workspace, donc un par worktree), signalé quand son workspace n'existe plus ; archives, symboles DeviceSupport par version d'iOS, installations supplémentaires de Xcode (rapport seul), chaîne d'outils Metal, et caches fixes de Xcode, CocoaPods, Swift Package Manager, Carthage et fastlane.

## Android

Émulateurs (AVD) et leurs snapshots Quick Boot, images système du SDK, NDK, build-tools, platforms, CMake et sources, restes du SDK Manager, Gradle user home (distributions du wrapper, caches par version, cache des dépendances, journaux du démon, JDK des toolchains), caches, journaux et anciens réglages d'Android Studio, JDK installés, caches Kotlin/Native et Maven. Un dossier de SDK ou d'AVD situé sur un disque externe est signalé, jamais touché. Voir [SDK Android, émulateurs et Gradle](/lu-cleaner/fr/guides/android/).

## Outils d'IA

Les données laissées par les outils de code IA : sessions Claude Code de plus de 30 jours et données de dossiers de projets qui n'existent plus, anciens builds de Claude Code, cursor-agent et Copilot CLI, sessions et bases de journaux de Codex, transcripts d'agent et données de workspace de Cursor, contextes archivés de Conductor, modèles locaux (Ollama, LM Studio, Hugging Face, Whisper), caches et images de VM des applications de bureau. La configuration, les identifiants, les mémoires et les bases de conversations sont protégés, et les sauvegardes des bases de conversations sont <span class="risk caution">caution</span>. Voir [Nettoyer les données des outils d'IA](/lu-cleaner/fr/guides/ai-tools-data/).

## Chaîne d'outils JS

Caches des gestionnaires de paquets (npm, Yarn classic et Berry, pnpm, bun, corepack), versions de node installées par nvm, fnm, mise, asdf ou volta (en gardant celles qui servent), installations npx, restes d'installations globales npm, caches Metro, Haste, Jest et Vitest, builds Expo Go pour simulateur, navigateurs Playwright, Puppeteer et Cypress, liens de shell périmés de fnm et surveillances (watches) watchman sur des dossiers supprimés (les surveillances actives de Metro et de Jest sont laissées tranquilles). Le cache global de Yarn Berry est <span class="risk caution">caution</span> quand des projets Plug'n'Play s'exécutent à partir de lui, tout comme un store pnpm dont le store virtuel global est lié par vos projets. Voir [Caches de la chaîne d'outils JavaScript et versions de Node](/lu-cleaner/fr/guides/js-toolchain/).

## IDE

Caches et état des éditeurs : données en cache des anciens builds de VS Code (et d'Insiders, de VSCodium), versions d'extensions remplacées, stockage de workspace de dossiers supprimés, index de serveurs de langage et Local History ; partie éditeur de Cursor et de Windsurf ; Zed ; IDE JetBrains (caches et réglages d'anciennes versions, journaux) ; Sublime Text et Sublime Merge.

## Conteneurs

Cache de build Docker, images inutilisées et conteneurs arrêtés (nettoyés avec les commandes `prune` de Docker), volumes inutilisés (rapport seul), images disque de Docker Desktop et d'OrbStack (listées uniquement avec l'accès complet au disque, puisqu'elles se trouvent dans des conteneurs d'applications), VM colima et Lima, données de `container` d'Apple, et caches de téléchargement des outils de VM. Les disques de VM sont signalés avec la commande qui récupère leur espace : supprimer une VM fait perdre tout ce qu'elle contient.

## Autres chaînes d'outils

Cache de téléchargement et anciennes versions de Homebrew (`brew cleanup`), caches de build et de modules Go (`go clean`), registre Cargo et téléchargements rustup, toolchains Rust et Rubies rbenv inutilisées (rapport seul), caches pip, uv, Poetry, pipenv et conda, RubyGems, NuGet, Ivy/sbt, Zig et Bazel.

## Système

La Corbeille, les caches et caches web hors ligne des applications de bureau (Slack, Discord, Notion, Figma, Postman…), les caches des navigateurs et leurs modèles d'IA embarqués, les journaux et rapports de plantage des applications, les téléchargements des outils de mise à jour des applications, les anciens installateurs, archives et builds d'applications dans `~/Downloads` (<span class="risk caution">caution</span>, jamais présélectionnés), les sauvegardes locales d'iPhone (rapport seul), et les éléments de macOS qui expliquent un disque plein : snapshots locaux de Time Machine, fichiers de swap et mises à jour macOS en attente, tous en rapport seul. Voir [Pourquoi l'espace libéré n'apparaît pas](/lu-cleaner/fr/guides/disk-space-not-freed/).

## Désactiver des catégories

Pour ignorer complètement une catégorie, listez-la dans le fichier de configuration :

```toml
disabled_categories = ["containers", "system"]
```

Les éléments des catégories désactivées ne sont jamais listés ni nettoyés, et les scanners qui ne produisent que des catégories désactivées ne sont pas démarrés du tout, ce qui accélère aussi les analyses. Voir [Configuration](/lu-cleaner/fr/reference/configuration/).
