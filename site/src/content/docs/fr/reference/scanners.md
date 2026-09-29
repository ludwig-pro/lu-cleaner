---
title: Ce qui est analysé
description: Chaque type d'élément produit par les huit scanners de lu-cleaner, avec ce qu'il est, son risque, la façon dont il est nettoyé et quand la sélection intelligente le recommande.
sidebar:
  order: 2
---

lu-cleaner exécute huit scanners. Sept d'entre eux contiennent de la logique (état git, versions utilisées, état des simulateurs…) ; le huitième, `catalog`, évalue une liste de chemins fixes. Cette page liste tous les **types** d'éléments qu'ils produisent. Utilisez un type, ou un identifiant de scanner, avec `--kind`/`-k` :

```bash
lu-cleaner scan -k node_modules,ios-pods      # deux types d'artefacts
lu-cleaner scan -k apple                      # tout ce qu'a trouvé le scanner apple
lu-cleaner clean -y -k xcode-derived-data -n  # simulation sur tous les dossiers DerivedData
```

Avec `clean --yes`, un identifiant de scanner qui couvre plusieurs catégories (`apple`, `ai`, `system`, `catalog`) ne constitue pas à lui seul un filtre restrictif : ajoutez `-c` ou `--smart`, ou utilisez des types d'éléments.

## Lire les tableaux

- **Risque** : <span class="risk safe">safe</span>, <span class="risk moderate">moderate</span>, <span class="risk caution">caution</span> ou <span class="risk never">never</span>. Plusieurs types changent de risque selon ce qu'observe le scanner ; la condition est indiquée.
- **Méthode** : `delete`, `trash`, `command` (la commande est indiquée), `worktree` ou `report`. Voir [méthodes de nettoyage](/lu-cleaner/fr/concepts/risk-and-smart-select/#méthodes-de-nettoyage).
- **Recommandé** décrit le comportement effectif de la sélection intelligente :
  - **Oui** : dès que l'élément ne porte aucun avertissement (pour les éléments safe, à partir de 1 Mio) ;
  - **Si inactif** : une fois inutilisé depuis `stale_after` (14 jours par défaut) ;
  - **Non** : jamais recommandé, soit parce que l'élément est <span class="risk caution">caution</span>, soit parce que le scanner y met son veto (voir [vetos des scanners](/lu-cleaner/fr/concepts/risk-and-smart-select/#vetos-des-scanners)) ;
  - une condition : la règle propre au scanner.

Deux règles s'appliquent partout et ne sont pas répétées : un élément qui porte un **avertissement** n'est jamais recommandé (application en cours d'exécution, processus à l'intérieur, version la plus récente conservée…), et les artefacts d'un projet actif dans les dernières 24 heures ne sont jamais recommandés. Les détails sont dans [Niveaux de risque et sélection intelligente](/lu-cleaner/fr/concepts/risk-and-smart-select/#sélection-intelligente). `clean --yes` retient aussi tout élément qui porte un avertissement, sauf si vous passez `--risk caution`.

« Version la plus récente conservée » suit le réglage `keep_latest` (1 par défaut) : il fixe combien des versions les plus récentes sont tenues à l'écart de la sélection intelligente pour les builds des outils d'IA, les paquets du SDK Android et les versions d'Android Studio, les IDE JetBrains, les versions de Node (par gestionnaire de versions), les runtimes de simulateur (par plateforme) et les entrées du catalogue qui conservent leurs correspondances les plus récentes.

Tout ce dont l'emplacement réel se trouve sur un autre volume devient un élément en rapport seul, avec l'avertissement `on external volume — no internal gain`, quel que soit son type. Les totaux et les plans comptent ce que la suppression libère réellement : des données liées physiquement depuis l'extérieur de l'élément ne libèrent rien, et un clone APFS ne libère que ses blocs privés, sauf si toutes les copies se trouvent dans l'élément. Quand c'est moins que la taille, la taille affiche un `*` et le JSON contient `reclaim`.

## worktrees

Trouve les **worktrees liés** git, les checkouts que les agents d'IA créent par dizaines, et supprime des worktrees entiers avec `git worktree remove`.

**Découverte**, combinée et dédoublonnée par inode :

1. les répertoires contenant un fichier `.git` sous les racines de worktrees (`~/.codex/worktrees`, `~/.cursor/worktrees`, `~/conductor/workspaces`, `~/.claude/worktrees`, `~/.claude-worktrees`, `~/.worktrees`, `~/.superset/worktrees`, `~/Library/Application Support/Claude/worktrees`, plus `worktree_roots`), sur 4 niveaux de profondeur ;
2. les dépôts principaux et les worktrees isolés sous vos racines de projets, jusqu'à `max_depth` ;
3. `git worktree list --porcelain` dans chaque dépôt principal trouvé, ce qui attrape les worktrees situés n'importe où sur le disque ;
4. les dossiers conventionnels `<repo>/.claude/worktrees`, `<repo>/.worktrees`, `<repo>/worktrees` et `<repo>-worktrees`.

**L'outil créateur** est reconnu d'après l'emplacement, le `codex-thread.json` de Codex et le nommage des branches (`claude/…` pour Claude desktop, `worktree-…` pour Claude Code). Le type d'élément est `<tool>-worktree`.

**La dernière activité** provient de git (date du commit HEAD, index, reflog, fichiers modifiés) et des outils eux-mêmes (threads Codex, sessions Claude desktop), jamais de la date de modification du dossier.

Les sous-modules et leurs worktrees sont laissés de côté. Un worktree n'est **orphelin** que si ses données git n'existent plus, de façon vérifiée (le dossier parent est lisible) ; quand elles ne peuvent pas être lues (permissions, protection de la vie privée de macOS, volume non monté), il est signalé comme illisible, jamais supprimé.

| Type | Ce que c'est | Risque | Méthode | Recommandé |
|---|---|---|---|---|
| `codex-worktree`, `cursor-worktree`, `conductor-worktree`, `claude-worktree`, `claude-desktop-worktree`, `superset-worktree`, `claude-squad-worktree`, `vibe-kanban-worktree`, `multica-worktree`, `manual-worktree` | Un worktree lié, avec sa taille décomposée en checkout et artefacts (`node_modules`, `ios/Pods`, builds natifs…) | moderate ; caution s'il est modifié, verrouillé, orphelin, déplacé, détaché avec des commits non poussés, illisible pour git, avec des fichiers secrets ignorés introuvables ailleurs, contenant un autre dépôt ou worktree, ouvert dans Cursor ou VS Code, utilisé par un processus, ou lié à un thread Codex, une session Claude desktop ou un workspace Conductor actif | `worktree` ; `delete` pour les orphelins, uniquement avec `--force` ; `report` hors de votre dossier personnel et de votre dossier temporaire utilisateur (par exemple `/tmp`), sur un volume externe, illisible, quand son dépôt principal est sur un volume non monté ou a été déplacé, ou pour un orphelin qui contient un autre dépôt | Propre, poussé et inactif depuis 7 jours, ou fusionné dans la branche par défaut et inactif depuis 1 jour ; jamais un orphelin |
| `worktree-prune` | Métadonnées git de worktrees dont le dossier n'existe plus, de façon vérifiée | safe | `command` : `git -C <repo> worktree prune` (ou suppression des seules entrées vérifiées quand d'autres appartiennent à un checkout non monté, illisible ou déplacé) | Oui |

**Statut** (colonne `STATUS` de `lu-cleaner worktrees --list`, `meta.status` en JSON) :

| Statut | Signification |
|---|---|
| `clean` | Aucune modification non commitée, rien de non poussé |
| `merged` | Propre, et tous ses commits sont dans la branche par défaut |
| `unpushed` | Des commits n'existent sur aucun remote (en avance sur l'upstream, ou jamais poussés), ou git n'a pas pu le déterminer. La branche et ses commits sont conservés lors de la suppression |
| `dirty` | Modifications non commitées, sur des fichiers suivis ou non suivis |
| `locked` | `git worktree lock` a été utilisé |
| `orphan` | Git ne suit plus le dossier : c'est une simple copie de fichiers |
| `unknown` | Git n'a pas pu être interrogé, les données git ou le dépôt principal sont illisibles, ou le dépôt principal est sur un volume non monté ou a été déplacé |

Les avertissements signalent aussi les fichiers secrets ou personnels ignorés qui seraient perdus (`.env*`, `.npmrc`, keystores, clés de signature, surcharges `*.local.*`…), les dépôts imbriqués, et un worktree que git enregistre à un autre emplacement (`moved: … run git -C <repo> worktree repair`). Le worktree qui contient votre répertoire courant ne peut pas être sélectionné. La suppression conserve la branche ; sans `--force`, elle est refusée pour un worktree verrouillé, des modifications non commitées ou non suivies, des commits présents sur aucune branche, ou un worktree ou dépôt imbriqué, et `--dry-run` signale les mêmes refus. Voir [Nettoyer les worktrees des agents IA](/lu-cleaner/fr/guides/ai-worktrees/).

## artifacts

Le scanner à la npkill, qui connaît React Native. Il parcourt vos racines de projets, les racines de worktrees et quelques dossiers où des outils conservent des copies de projets (`~/conductor/archived-contexts`, `~/Documents/Codex`, `~/.gemini/antigravity/scratch`, auxquels seules les règles de dépendances et de build s'appliquent), jusqu'à `max_depth` niveaux. Les racines indiquées en ligne de commande (`lu-cleaner artifacts <root>`, `--root`) sont alors les seuls dossiers parcourus : ni racines de worktrees, ni dossiers supplémentaires, et `/` est refusé. Il ne descend jamais dans les liens symboliques ni dans d'autres volumes, et ne propose jamais le checkout lui-même.

**Règles de correspondance :**

- Un dossier ne correspond que si un **marqueur** existe à côté (`package.json` à côté de `node_modules`). Les marqueurs sont revérifiés juste avant la suppression.
- Un dossier qui contient une **entrée `.git`** de n'importe quel type (dépôt, worktree lié, sous-module) ou un dépôt bare est un checkout, jamais un artefact, pas plus qu'un dossier qui contient un worktree enregistré. Une sortie générique qui contient un checkout est en rapport seul. Juste avant la suppression, une cible devenue un checkout depuis l'analyse est laissée intacte.
- **Les noms génériques** (`build`, `dist`, `out`, `target`, `coverage`, `vendor/bundle`, `.yarn/*`…) peuvent être du code source. Dans un arbre de travail git, ils doivent être ignorés par git, ou être non suivis avec un contenu de build sans ambiguïté (produits de build Xcode, `CMakeCache.txt`, `CACHEDIR.TAG`…). Hors de git, ils doivent avoir ce contenu. Une sortie JS (`build`, `dist`, `out`, `web-build`) hors de git dont le contenu n'est qu'un indice faible (simples fichiers `.js`, `index.html`, ressources), sans rien de propre à un générateur (source maps, noms de bundles hachés, `asset-manifest.json`, `_next`…), est <span class="risk caution">caution</span> et jamais recommandée, avec l'avertissement `not under git: nothing proves it is build output — check before deleting`.
- Dans un arbre de travail git, un dossier qui contient des **fichiers suivis** n'est jamais proposé.
- Un candidat qui échoue à la vérification est affiché en rapport seul avec `not proposed: <reason>`.

**La dernière utilisation** est l'activité du *projet*, car les builds et les installations touchent l'artefact lui-même en permanence : les fichiers de premier niveau du projet et du paquet (les dates des dossiers sont ignorées), l'index git, `HEAD` et le reflog, et les fichiers non commités ou non suivis n'importe où dans l'arbre de travail. Hors de git, c'est le fichier source le plus récent d'un parcours limité qui compte.

**Recommandé** : les artefacts safe une fois leur projet inactif depuis 24 heures ; les artefacts moderate une fois le projet inactif depuis `stale_after`. Jamais tant qu'un processus tourne dans le projet (`in use: process … runs inside the project (dev server, agent, shell?)`), tant que l'outil qui le protège tourne (`xcodebuild is running — quit it before cleaning`), ni quand le dossier contient des fichiers `.apk`, `.aab` ou `.ipa` générés.

### JavaScript et React Native

| Type | Dossier | Requis à côté | Risque |
|---|---|---|---|
| `node_modules` | `node_modules` | `package.json` | moderate |
| `ios-pods` | `Pods` | `Podfile` ou `Podfile.lock` | moderate |
| `ios-build` (`xcode-build` hors de `ios/` et `macos/`) | `build` | `*.xcodeproj`, `*.xcworkspace` ou `Podfile`, plus un contenu de build Xcode | safe ; moderate quand `Podfile.lock` référence le codegen React Native dans `build/generated/ios` |
| `android-build` (`gradle-build` hors de `android/`) | `build` | `build.gradle(.kts)`, plus un contenu de sortie Gradle | safe |
| `android-gradle` (`gradle-cache`) | `.gradle` | Fichiers de projet Gradle | safe |
| `android-kotlin` (`gradle-kotlin`) | `.kotlin` | Fichiers de projet Gradle | safe |
| `android-cxx` | `.cxx`, `.externalNativeBuild` | `CMakeLists.txt` ou `build.gradle(.kts)` | safe |
| `expo` | `.expo` | `package.json`, `app.json` ou `app.config.*` | safe |
| `metro-cache` | `.metro-cache`, `.metro` | `package.json` ou `metro.config.*` | safe |
| `js-build`, `dist`, `web-build`, `out` | `build`, `dist`, `web-build`, `out` | `package.json` (ou configuration Next.js / electron-vite pour `out`), plus un contenu de sortie de build | safe ; `js-build`, `dist` et `out` deviennent moderate quand `package.json` (`main`, `exports`, `types`) pointe dedans ; caution hors de git avec seulement des indices faibles |
| `next`, `open-next`, `nuxt`, `nitro-output`, `svelte-kit`, `astro`, `docusaurus`, `react-router`, `vinxi`, `tanstack` | `.next`, `.open-next`, `.nuxt`, `.output`, `.svelte-kit`, `.astro`, `.docusaurus`, `.react-router`, `.vinxi`, `.tanstack` | `package.json` ou la configuration du framework | safe |
| `turbo`, `parcel-cache`, `vite-cache`, `swc-cache`, `wireit`, `rollup-cache`, `nx-cache`, `angular-cache`, `js-cache` | `.turbo`, `.parcel-cache`, `.vite`, `.swc`, `.wireit`, `.rollup.cache` ou `.rpt2_cache`, `.nx/cache`, `.angular/cache`, `.cache` | `package.json` ou la configuration de l'outil | safe |
| `vercel-output`, `netlify-cache`, `netlify-functions-serve`, `wrangler-tmp` | `.vercel/output`, `.netlify/cache`, `.netlify/functions-serve`, `.wrangler/tmp` (les liens de site et les données locales sont conservés) | `package.json` ou la configuration de l'outil | safe |
| `storybook-static`, `coverage`, `nyc-output` | `storybook-static`, `coverage` (avec un rapport de couverture à l'intérieur), `.nyc_output` | `package.json` (ou un fichier de projet Python pour `coverage`) | safe |
| `playwright-report` | `playwright-report`, `test-results`, `blob-report` | `playwright.config.*` ou `package.json` | moderate |
| `pnpm-store` | `.pnpm-store` (un `store-dir` à l'intérieur du dépôt) | Fichiers de projet pnpm ou npm | moderate |
| `yarn-cache` | `.yarn/cache` | `.yarnrc.yml` | moderate ; recommandé immédiatement quand `enableGlobalCache: true` en fait un reste inutile |
| `yarn-unplugged` | `.yarn/unplugged` | `.yarnrc.yml` | moderate |

### Apple

| Type | Dossier | Requis à côté | Risque |
|---|---|---|---|
| `project-derived-data` | `DerivedData`, `DerivedData-*`, `.derived-data*`, `derived-data`, `derivedData` | Projet Xcode, `Podfile` ou `Package.swift`, plus un contenu DerivedData | safe |
| `swiftpm-build` | `.build` | `Package.swift` | safe |
| `carthage-build` | `Carthage/Build` | `Cartfile` ou `Cartfile.resolved` | safe |

`ios-pods`, `ios-build` et `project-derived-data` sont refusés tant que `xcodebuild` tourne.

### Autres langages

| Type | Dossier | Requis à côté | Risque |
|---|---|---|---|
| `cmake-build` | `build`, `build-*`, `build_*`, `cmake-build-*`, `out` avec `CMakeCache.txt` à l'intérieur | `CMakeLists.txt` | moderate |
| `flutter-build`, `dart-tool` | `build`, `.dart_tool` | `pubspec.yaml` | safe |
| `rust-target` | `target` | `Cargo.toml` | safe |
| `maven-target` | `target` | `pom.xml` ou `build.sbt` | safe |
| `python-venv` | `.venv`, `venv` avec `pyvenv.cfg` | un fichier de projet Python (`pyproject.toml`, `requirements*.txt`, `uv.lock`…) | moderate |
| `python-venv` | `.venv`, `venv`, `env`, `.env` ou tout dossier contenant `pyvenv.cfg`, sans fichier de dépendances | aucun | caution |
| `python-tox` | `.tox`, `.nox` | un fichier de projet Python | safe |
| `python-cache` | `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.pytype`, `.pyre`, regroupés par projet | aucun | safe |
| `htmlcov` | `htmlcov` avec un rapport à l'intérieur | un fichier de projet Python | safe |
| `ruby-vendor-bundle` | `vendor/bundle` | `Gemfile` | moderate |
| `elixir-build`, `elixir-deps` | `_build`, `deps` | `mix.exs` | safe, moderate |
| `zig-cache`, `haskell-stack-work`, `cabal-dist`, `dotnet-build` | `zig-cache`, `.zig-cache`, `zig-out`, `.stack-work`, `dist-newstyle`, `bin` et `obj` | `build.zig`, `stack.yaml`, fichiers Cabal, `*.csproj` | safe |

### Générique

| Type | Ce que c'est | Risque | Méthode |
|---|---|---|---|
| `cachedir-tag` | Tout dossier contenant un `CACHEDIR.TAG` valide (écrit par cargo, uv, ccache…) | safe | `delete` |
| `extra-artifact` | Un nom de dossier listé dans `extra_artifacts` | moderate | `delete` |
| `ignored-dir` | Un dossier ignoré par git de 200 Mo ou plus (sans compter les artefacts connus qu'il contient) qu'aucune règle ne reconnaît : caches de QA d'agents, copies de préproduction, enregistrements | caution | `delete` ; `report` quand il contient un checkout git ou un worktree |

`lu-cleaner artifacts -t <kind>` n'accepte que ces types (et les alias `pods`, `pod`, `cocoapods`, `node-modules`) ; un type inconnu est une erreur d'utilisation.

## apple

Xcode et les simulateurs iOS. Les caches Xcode fixes (CocoaPods, SwiftPM, Carthage, journaux CoreSimulator…) sont des entrées du [catalogue](#catalog).

| Type | Ce que c'est | Risque | Méthode | Recommandé |
|---|---|---|---|---|
| `xcode-derived-data` | Un dossier DerivedData (Xcode en crée un par chemin de workspace, donc un par worktree), y compris dans un emplacement DerivedData personnalisé | safe | `delete`, refusé tant que Xcode ou `xcodebuild` tourne | Oui ; le nom indique `(workspace gone)` quand son workspace n'existe plus |
| `xcode-module-cache` | Caches de modules partagés d'un emplacement DerivedData personnalisé | safe | `delete` | Oui |
| `xcode-archive` | Un `.xcarchive` (binaire et dSYM) ; la plus récente de chaque application porte un avertissement | caution | `trash` | Non |
| `xcode-device-support` | Symboles de débogage copiés depuis un appareil, par version d'OS | moderate | `delete` | Si inactif ; jamais les symboles les plus récents d'une plateforme |
| `xcode-app` | Un Xcode installé autre que celui qui est sélectionné | caution | `report` | Non |
| `xcode-metal-toolchain` | Le compilateur de shaders Metal que Xcode télécharge séparément | moderate | `command` : `xcodebuild -deleteComponent metalToolchain` | Non |
| `ios-simulator` | Un appareil de simulateur | moderate ; caution quand des applications y sont installées | `command` : `xcrun simctl delete <udid>` ; non sélectionnable quand il est démarré | Éteint, sans application et inactif ; jamais un appareil qui n'a jamais été démarré |
| `ios-simulators-unavailable` | Un simulateur que `simctl` signale comme indisponible | safe quand son runtime a disparu de façon avérée et qu'il ne contient aucune application ; caution quand il contient des applications, ou peut revenir (runtime encore installé ou présent sur le disque, autre Xcode sélectionné, image du runtime non montée) | `command` : `xcrun simctl delete <udid>` pour ce seul appareil | Quand safe |
| `ios-simulator-orphan` | Un dossier d'appareil que `simctl` ne connaît plus (`device.plist` absent ou cassé) | moderate | `delete` | Inutilisé depuis 7 jours |
| `ios-simulator-runtime` | Une image disque de runtime de simulateur | moderate ; rapport seul quand il est fourni avec Xcode | `command` : `xcrun simctl runtime delete <id>` | Utilisé par aucun simulateur, pas parmi les `keep_latest` plus récents de sa plateforme, et un runtime plus récent est installé |
| `ios-simulator-device-set` | Ensembles d'appareils des aperçus SwiftUI, d'Interface Builder, des tests parallèles et des Playgrounds | safe | `command` : `xcrun simctl --set <dir> delete all` | Oui |
| `ios-simulator-attachments` | Enregistrements d'écran XCTest laissés dans un simulateur par l'automatisation des tests d'interface | safe | `delete` | Quand le simulateur est éteint |
| `ios-simulator-logs` | Journaux unifiés des simulateurs éteints | safe | `delete` | Oui |
| `ios-simulator-caches` | Caches système des simulateurs inutilisés depuis plus de 2 semaines | moderate | `delete` | Si inactif |
| `ios-simulator-dyld-cache` | Caches dyld appartenant à root, pour un autre build de macOS ou un runtime supprimé | safe | `report` (la note donne la commande `sudo rm`) | Non |
| `coresimulator-caches` | Caches et fichiers temporaires CoreSimulator de l'utilisateur | safe | `delete`, refusé tant que Simulator tourne | Oui |

Les appareils de simulateur, ainsi que les enregistrements, journaux et caches pris à l'intérieur, sont revérifiés avec `simctl` juste avant le nettoyage : si un simulateur a été démarré depuis l'analyse, l'élément est ignoré. Un simulateur indisponible est aussi ignoré s'il est redevenu disponible ou a changé de runtime. lu-cleaner ne lance jamais `xcrun simctl delete unavailable`, qui supprimerait aussi des appareils qui ne sont inutilisables que pour le moment.

## android

La chaîne d'outils Android d'un développeur React Native ou Expo. Les SDK et les dossiers d'AVD qui se trouvent en réalité sur un autre volume (souvent derrière des liens symboliques) sont signalés, jamais supprimés, et les liens symboliques cassés ne sont jamais touchés. Les paquets versionnés du SDK sont comparés à ce qu'utilisent vos projets analysés (versions du wrapper Gradle, `compileSdk`, `ndkVersion`, versions de build-tools et de CMake), lu dans vos racines configurées même quand `--root` restreint le scan des artefacts : sans racines de projets, l'utilisation est inconnue et aucun paquet versionné n'est recommandé.

| Type | Ce que c'est | Risque | Méthode | Recommandé |
|---|---|---|---|---|
| `android-avd` | Un émulateur : dossier et `.ini` | caution ; moderate pour les AVD x86 sur Apple Silicon | `delete`, refusé tant que l'émulateur tourne | Seulement les AVD x86 sur Apple Silicon (ils ne peuvent pas démarrer) |
| `android-avd-snapshots` | Snapshots Quick Boot d'un AVD | moderate ; caution s'il y a des snapshots que vous avez enregistrés | `delete` | Si inactif |
| `android-avd-orphan-ini` | Un `.ini` d'AVD dont le dossier a été supprimé | safe | `delete` | Oui |
| `android-avd-home` | Un dossier d'AVD hors du volume interne | caution | `report` | Non |
| `android-system-image` | Une image système d'émulateur | moderate ; non sélectionnable tant qu'un AVD l'utilise | `delete` | Quand aucun AVD ne l'utilise, ou qu'elle est x86 sur Apple Silicon |
| `android-ndk`, `android-build-tools`, `android-platform`, `android-cmake` | Paquets versionnés du SDK | moderate ; safe pour les restes d'une installation ratée (build-tools, platforms, CMake) | `delete` | Quand aucun projet analysé ne l'utilise ; jamais les `keep_latest` versions les plus récentes ; CMake 3.22.1 (version par défaut d'AGP 8) est conservé |
| `android-ndk-bundle` | Le dossier obsolète `ndk-bundle` | moderate | `delete` | Sauf si un projet épingle `ndk.dir` |
| `android-sources` | Sources du SDK pour un niveau d'API | safe ; moderate quand elles sont utilisées ou les plus récentes | `delete` | Oui (celles qui sont safe) |
| `android-sdk-temp` | Fichiers temporaires et téléchargements partiels du SDK Manager | safe | `delete` | Oui |
| `android-sdk-legacy-tools`, `android-cmdline-tools-old`, `android-haxm` | SDK Tools obsolètes, cmdline-tools remplacés, Intel HAXM sur Apple Silicon | moderate | `delete` | Oui |
| `android-sdk` | Un SDK hors du volume interne | moderate | `report` | Non |
| `android-gradle-dist` | Une distribution du wrapper Gradle | moderate ; safe pour un téléchargement interrompu | `delete` | Quand aucun projet analysé n'utilise cette version, ou que le téléchargement a été interrompu |
| `android-gradle-version-cache` | Caches Gradle par version | moderate | `delete` | Quand ni un projet analysé ni un Gradle installé n'utilise cette version ; sinon si inactif |
| `android-gradle-transforms`, `android-gradle-jars`, `android-gradle-build-cache` | Caches de transformations, de jars et de build locaux | moderate ; safe pour les anciens formats de cache et le cache de build | `delete` | Anciens formats et cache de build : oui ; transformations actuelles : si inactif |
| `android-gradle-modules` | Le cache des dépendances (`modules-2`) | moderate | `delete` | Si inactif |
| `android-gradle-caches-other` | Autres caches Gradle partagés | moderate | `delete` | Si inactif |
| `android-gradle-daemon-logs` | Journaux du démon d'une version de Gradle | safe | `delete` | Oui |
| `android-gradle-tmp` | Fichiers temporaires et téléchargements interrompus de plus d'un jour | safe | `delete` | Oui |
| `android-gradle-jdk` | Un JDK provisionné automatiquement par les toolchains Gradle | moderate | `delete` | Non |
| `android-gradle-misc` | Assistants natifs, rapports de build Kotlin, état des workers | safe | `delete` | Oui |
| `android-gradle-home` | Un Gradle user home hors du volume interne | moderate | `report` | Non |
| `android-studio-cache` | Caches d'une version d'Android Studio ; son dossier `LocalHistory` (votre historique de modifications) est toujours conservé | safe pour les versions qui ne sont plus installées ; moderate pour la version actuelle | `delete`, refusé tant qu'Android Studio tourne | Oui pour les anciennes versions ; si inactif pour la version actuelle |
| `android-studio-logs` | `idea.log` et rapports de plantage | safe | `delete` | Oui |
| `android-studio-config` | Réglages d'un ancien Android Studio (ceux de la version actuelle ne sont jamais listés) | moderate | `delete` | Quand la version actuelle a ses propres réglages ; jamais tant qu'une version installée n'a pas encore été lancée (elle les importe à son premier lancement) |
| `android-user-cache` | Cache du dépôt `~/.android`, ancien cache de build, dumps de plantage (les clés et les réglages sont conservés) | safe | `delete` | Oui |
| `android-jdk` | Un JDK dans votre dossier personnel, installé par un IDE, SDKMAN, mise ou asdf | moderate ; non sélectionnable quand il est utilisé | `delete` | Non |
| `android-jdk-system` | Un JDK dans `/Library/Java/JavaVirtualMachines` | moderate | `report` | Non |

Les éléments Gradle reçoivent un avertissement tant qu'un démon Gradle tourne (`./gradlew --stop` règle le problème), ce qui les exclut aussi de la sélection intelligente.

## ai

Les données laissées par les outils de code IA. Les emplacements fixes (journaux de débogage de Claude Code, caches de Codex, caches des applications de bureau…) sont des entrées du [catalogue](#catalog) ; le scanner traite ce qui demande de la logique. La plupart des éléments sont refusés tant que l'application concernée tourne.

| Type | Ce que c'est | Catégorie | Risque | Méthode | Recommandé |
|---|---|---|---|---|---|
| `claude-code-orphan-project` | Transcripts et sorties d'outils des sessions Claude Code lancées dans un dossier qui n'existe plus, de façon vérifiée (son `memory/` est conservé) | ai | moderate quand le dossier a été lu dans les sessions elles-mêmes, n'est pas un workspace Conductor, et que rien n'a été écrit depuis 30 jours ; caution sinon | `delete`, revérifié juste avant | Seulement quand moderate |
| `claude-code-old-sessions` | Sessions d'un projet non touché depuis 30 jours ou plus | ai | caution | `delete` | Non |
| `claude-code-config-backups` | Sauvegardes horodatées de `~/.claude.json` de plus de 7 jours (la plus récente est conservée) | ai | moderate | `delete` | Si inactif |
| `claude-code-old-version` | Builds natifs de Claude Code remplacés | ai | moderate | `delete` | Oui |
| `claude-desktop-old-claude-code`, `claude-desktop-old-claude-code-vm` | Anciennes copies de Claude Code téléchargées par l'application Claude desktop | ai | safe | `delete` | Oui |
| `cursor-agent-old-version`, `cursor-agent-bundled-old-version`, `cursor-origin-old-version`, `copilot-cli-old-version`, `vibe-kanban-old-version`, `conductor-old-agent-binary` | Builds de CLI et d'agents remplacés (le build actif est conservé) | ai | safe | `delete` | Oui |
| `codex-old-sessions` | Rollouts Codex non touchés depuis 30 jours ou plus, par mois (threads épinglés conservés) | ai | caution | `delete` | Non |
| `codex-archived-sessions` | Rollouts des threads que vous avez archivés, par mois | ai | caution | `delete` | Non |
| `codex-logs-db` | Base de journaux de traçage de Codex (avec ses `-wal`/`-shm`) | ai | safe | `delete` | Oui |
| `codex-stale-logs-db` | Une ancienne copie de la base de journaux | ai | safe | `delete` | Oui |
| `codex-visualizations` | Captures d'écran et enregistrements d'agent de plus de 30 jours | ai | caution | `delete` | Non |
| `multica-task-codex-homes` | Copies du dossier personnel de Codex pour des tâches Multica terminées | ai | moderate | `delete` | Oui |
| `cursor-agent-orphan-projects` | Données de l'agent Cursor pour des dossiers supprimés | ai | moderate quand Cursor a enregistré le dossier et que rien n'a été écrit depuis 30 jours ; caution (« to review ») quand elles sont récentes, déduites du nom du répertoire, ou d'un workspace Conductor | `delete`, revérifié juste avant | Seulement quand moderate |
| `cursor-agent-mcp-caches` | Caches de descripteurs MCP par projet | ai | safe | `delete` | Oui |
| `cursor-agent-old-transcripts` | Transcripts d'agent non touchés depuis 30 jours ou plus | ai | caution | `delete` | Non |
| `cursor-cached-data-old-builds` | Caches de code des builds précédents de Cursor | ide | safe | `delete` | Oui |
| `cursor-workspace-storage-orphans` | État de workspace de dossiers supprimés | ide | moderate quand il n'a pas été touché depuis 30 jours et ne contient pas de données de conversation ; caution (« to review ») quand il est récent, contient des données de conversation, ou appartient à un workspace Conductor | `delete`, revérifié juste avant | Seulement quand moderate |
| `cursor-workspace-dead-extension-data` | Données de workspace d'extensions désinstallées | ide | safe | `delete` | Oui |
| `cursor-extension-old-versions` | Versions d'extensions remplacées | ide | moderate | `delete` | Oui |
| `conductor-archived-contexts` | Notes et pièces jointes de workspaces Conductor archivés il y a plus de 30 jours | ai | caution | `delete` | Non |
| `chatgpt-atlas-leftover` | Données de ChatGPT Atlas quand l'application n'est pas installée | ai | caution | `delete` | Non |
| `antigravity-browser-profile` | Profil de l'agent navigateur d'Antigravity (mots de passe enregistrés, cookies, historique) quand l'application n'est pas installée | ai | caution | `delete` | Non |
| `antigravity-extensions` | Extensions de l'IDE Antigravity quand l'application n'est pas installée | ai | moderate | `delete` | Si inactif |
| `antigravity-browser-recordings` | Enregistrements de l'agent navigateur de plus de 90 jours | ai | caution | `delete` | Non |
| `antigravity-conversations`, `antigravity-brain` | Transcripts et mémoire d'agent d'Antigravity | ai | caution | `report` | Non |
| `ollama-model`, `lmstudio-model`, `huggingface-model`, `huggingface-dataset`, `huggingface-space`, `whisper-model`, `voiceink-whisper-model` | Poids de modèles locaux | ai | caution | `delete` | Non |

La configuration, les identifiants, les mémoires, l'historique et les bases de conversations de ces outils sont [protégés](/lu-cleaner/fr/concepts/safety/#3-chemins-protégés). Les sauvegardes des bases de conversations (sauvegardes de `state.vscdb` de Cursor, sauvegardes de réparation par Codex des bases de mémoires et d'objectifs) sont des entrées caution du catalogue, jamais recommandées. Voir [Nettoyer les données des outils d'IA](/lu-cleaner/fr/guides/ai-tools-data/).

## js

La chaîne d'outils JavaScript et React Native, là où il faut de la logique. Les caches de gestionnaires de paquets à chemin fixe (npm, Yarn classic, bun, Metro, Jest…) sont des entrées du [catalogue](#catalog).

| Type | Ce que c'est | Risque | Méthode | Recommandé |
|---|---|---|---|---|
| `node-version` | Une version de Node.js installée par nvm, fnm, mise, asdf ou volta | moderate ; caution quand c'est votre version par défaut ou le `node` de votre `PATH` ; non sélectionnable tant qu'un processus l'exécute | `delete` | Quand rien ne la référence : ni version par défaut, ni dans le `PATH`, ni épinglée par un projet (`.nvmrc`, `.node-version`, `.tool-versions`), ni alias, ni en cours d'exécution, et aucun paquet global installé uniquement là. Jamais les `keep_latest` versions non référencées les plus récentes de chaque gestionnaire, et jamais quand la liste des processus ne peut pas être lue |
| `npm-global-leftover` | Un dossier `.name-XXXXXXXX` laissé par un `npm install -g` interrompu, de plus d'un jour | safe | `delete` | Oui |
| `npx-package` | Un paquet installé à la volée par `npx` (souvent un serveur MCP) | moderate | `delete` | Inutilisé depuis 14 jours et non utilisé par un processus en cours |
| `pnpm-store` | Un store pnpm adressé par contenu (un par version de store) | moderate ; caution quand il contient un store virtuel global vers lequel pointent des projets | `delete` | Si inactif ; jamais avec un store virtuel global |
| `pnpm-store-prune` | Paquets non référencés du store actif (taille inconnue avant l'exécution : affichée comme `?`) | safe | `command` : `pnpm store prune` | Oui, sauf si un projet enregistré dans le store est inaccessible (volume non monté) |
| `yarn-berry-cache` | Cache zip global de Yarn Berry | safe ; caution quand des projets Plug'n'Play s'exécutent à partir de lui ; moderate quand la recherche des projets Plug'n'Play n'a pas pu regarder partout | `delete` | Oui ; jamais avec des projets PnP ; si inactif quand moderate |
| `yarn-berry-metadata` | Métadonnées de registre de Yarn Berry | safe | `delete` | Oui |
| `yarn-berry-store` | Store `hardlinks-global` de Yarn Berry (les fichiers encore liés dans des `node_modules` survivent) | moderate | `delete` | Si inactif |
| `expo-go-ios` | Builds Expo Go mis en cache pour le simulateur iOS | safe ; moderate pour le build le plus récent d'une version de SDK | `delete` | Builds remplacés : oui ; le plus récent : si inactif, sauf si un projet utilise ce SDK |
| `playwright-browser` | Une révision de navigateur Playwright | moderate | `delete` | Quand aucun Playwright installé n'en a besoin, ou qu'une révision plus récente du même navigateur existe |
| `rn-codegen-tmp`, `vitest-tmp` | Dossiers temporaires du codegen React Native et de Vitest dans `$TMPDIR`, de plus de 2 heures | safe | `delete` | Oui |
| `fnm-multishells` | Liens symboliques par shell de fnm pour des shells qui n'existent plus (uniquement les liens, jamais Node lui-même) | safe | `delete` | Oui |
| `watchman-stale-watches` | Surveillances watchman sur des dossiers supprimés (libère de la mémoire, pas du disque) | safe | `command` : `watchman watch-del <root>` pour chaque surveillance périmée uniquement ; les surveillances actives (Metro, Jest) ne sont pas touchées | Oui |

Voir [Caches de la chaîne d'outils JavaScript et versions de Node](/lu-cleaner/fr/guides/js-toolchain/).

## system

Tout ce qui se trouve hors des projets et des chaînes d'outils mobiles ou d'IA : Corbeille, conteneurs, Homebrew et chaînes d'outils de langages, éditeurs, `~/Downloads`, et les éléments de macOS qui expliquent un disque plein.

| Type | Ce que c'est | Catégorie | Risque | Méthode | Recommandé |
|---|---|---|---|---|---|
| `trash` | Tout le contenu de `~/.Trash` | system | moderate | `delete` (la vide) ; ignoré en mode Corbeille | Non : vider la Corbeille est irréversible |
| `trash-finder` | La Corbeille quand lu-cleaner ne peut pas la lire (pas d'Accès complet au disque) | system | caution | `command` : « Vider la Corbeille » du Finder via `osascript` | Non |
| `downloads-installers`, `downloads-archives`, `downloads-mobile-builds` | Fichiers `.dmg`/`.pkg`, archives et fichiers `.ipa`/`.apk` de `~/Downloads` ni téléchargés ni ouverts depuis 30 jours | system | caution | `delete` | Non |
| `app-update-downloads` | Mises à jour téléchargées par les outils de mise à jour intégrés des applications | system | safe | `delete` | Oui |
| `ios-device-backup`, `ios-device-backups` | Sauvegardes locales d'iPhone et d'iPad | system | caution | `report` | Non |
| `time-machine-snapshots`, `macos-swap`, `macos-staged-updates` | Snapshots locaux APFS, fichiers de swap, mises à jour macOS en attente | system | never | `report` | Non |
| `docker-build-cache` | Cache BuildKit inutilisé | containers | safe | `command` : `docker --context <context> builder prune -a -f` | Oui |
| `docker-unused-images` | Images qu'aucun conteneur n'utilise | containers | moderate | `command` : `docker --context <context> image prune -a -f` | Non |
| `docker-stopped-containers` | Conteneurs arrêtés et leur couche inscriptible | containers | caution | `command` : `docker --context <context> container prune -f` | Non |
| `docker-unused-volumes` | Volumes qu'aucun conteneur n'utilise | containers | caution | `report` | Non |
| `docker-desktop-disk`, `orbstack-data`, `colima-vm`, `lima-vm` | Images disque de VM contenant les conteneurs et les images (celles de Docker Desktop et d'OrbStack uniquement avec l'Accès complet au disque : elles se trouvent dans les conteneurs de ces applications) | containers | caution | `report` | Non |
| `apple-container-data` | Images et snapshots de la CLI `container` d'Apple | containers | moderate | `report` | Non |
| `homebrew-cache` | Cache de téléchargement de Homebrew (ses métadonnées d'API sont conservées) | langs | safe | `delete` | Oui |
| `homebrew-cleanup` | Anciennes versions, verrous et journaux périmés, tels que calculés par `brew cleanup -n` | langs | moderate | `command` : `brew cleanup --prune=all` | Oui |
| `homebrew-old-kegs` | Anciennes versions conservées par des formules obsolètes | langs | moderate | `report` | Non |
| `go-build-cache` | Cache de build et de tests de Go | langs | safe | `command` : `go clean -cache` (une simple suppression quand `go` n'est pas installé) | Oui |
| `go-module-cache` | Cache des modules Go | langs | moderate | `command` : `go clean -modcache` | Si inactif |
| `rustup-toolchain`, `rbenv-ruby` | Toolchains Rust autres que celle par défaut, Rubies rbenv ni globales ni épinglées | langs | moderate | `report` | Non |
| `vscode-cached-data` (et `vscode-insiders-…`, `vscodium-…`) | Caches de code des builds précédents de l'éditeur | ide | safe | `delete` | Oui |
| `vscode-vsix-cache`, `vscode-caches`, `vscode-ls-indexes` | Paquets `.vsix` téléchargés, caches et anciens journaux, index de serveurs de langage non touchés depuis 3 jours | ide | safe | `delete` | Oui |
| `vscode-workspace-orphans` | Stockage de workspace de dossiers supprimés, non touché depuis 7 jours ou plus | ide | moderate | `delete` | Oui |
| `vscode-old-extensions` | Versions d'extensions remplacées | ide | safe | `delete` | Oui |
| `vscode-local-history` | Local History (Timeline) | ide | caution | `report` | Non |
| `vscode-extensions-leftover` | Extensions d'un éditeur qui n'est plus installé | ide | caution | `delete` | Non |
| `zed-caches`, `zed-language-servers`, `zed-leftover` | Caches de Zed, serveurs de langage téléchargés, données d'un Zed désinstallé | ide | safe, moderate, caution | `delete` | Oui, si inactif, non |
| `jetbrains-old-caches`, `jetbrains-logs` | Caches JetBrains des versions d'IDE plus anciennes que les `keep_latest` plus récentes, journaux des IDE | ide | safe | `delete` | Oui |
| `jetbrains-old-settings`, `jetbrains-caches` | Réglages d'anciennes versions d'IDE, caches des versions actuelles | ide | moderate | `delete` | Si inactif |

Les caches JetBrains n'incluent jamais le dossier `LocalHistory`, qui contient votre historique de modifications. Les commandes Docker sont liées au contexte qui a été mesuré et revalidées juste avant leur exécution : un `docker context use` lancé dans un autre terminal ne peut pas les rediriger. Ce sont de simples commandes `docker …` quand `DOCKER_HOST` est défini, et aucune n'est proposée quand le contexte actuel ne peut pas être lu.

Voir [Pourquoi l'espace libéré n'apparaît pas](/lu-cleaner/fr/guides/disk-space-not-freed/) pour les snapshots, la Corbeille et les images disque de Docker.

## catalog

Le scanner catalog évalue environ 200 entrées : des emplacements connus de caches, de journaux et de données d'outils dans votre dossier personnel ou votre dossier temporaire utilisateur. L'`id` de chaque entrée est le type d'élément. La liste complète, avec le risque, la méthode, les chemins et les notes, se trouve dans la [référence du catalogue](/lu-cleaner/reference/catalog/), et sur votre machine :

```bash
lu-cleaner catalog              # toutes les entrées
lu-cleaner catalog -c js        # une catégorie
lu-cleaner catalog -k claude    # les identifiants qui contiennent "claude"
lu-cleaner catalog --json
```

Comment les entrées deviennent des éléments :

- Les chemins sont des globs (`~/.cache/node/corepack/*`, `$TMPDIR/bunx-*`). Par défaut, toutes les correspondances d'une entrée forment **un élément groupé** (`Metro / Haste file maps (18)`) ; certaines entrées produisent à la place **un élément par correspondance** (un par version téléchargée, par exemple), en laissant éventuellement de côté les plus récentes (au moins `keep_latest` d'entre elles).
- Une entrée peut exiger un binaire dans le `PATH` (pour sa commande), ne garder que les correspondances plus anciennes qu'un âge donné, masquer les résultats sous une taille minimale, et déclarer des applications qui ne doivent pas tourner pendant son nettoyage. Une application en cours d'exécution apparaît comme un avertissement lors de l'analyse.
- Les chemins exclus ne correspondent jamais, les chemins protégés seulement pour les entrées en rapport seul, les liens symboliques cassés sont ignorés, et les correspondances situées sur un autre volume sont en rapport seul.
- Sans l'Accès complet au disque, les chemins situés dans les conteneurs des autres applications (`~/Library/Containers/<app>`, `~/Library/Group Containers/<group>`) ne sont jamais lus : macOS bloquerait sur une demande d'autorisation. Une telle entrée devient un élément en rapport seul avec l'avertissement `inside another app's container: needs Full Disk Access (…)`.
- La date de dernière utilisation d'un groupe est celle du fichier le plus récent écrit à l'intérieur, car la date de modification d'un dossier de cache ne change que lorsque des entrées y sont ajoutées ou retirées.
- Les risques et les recommandations suivent les règles génériques ; une entrée peut forcer la recommandation (`Flipper leftovers`).

Pour ajouter une entrée, voir [Contribuer](/lu-cleaner/fr/about/contributing/#ajouter-une-entrée-au-catalogue).
