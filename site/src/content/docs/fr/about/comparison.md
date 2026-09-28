---
title: Comparaison avec d'autres outils
description: Comment lu-cleaner se compare à CleanMyMac, mole, npkill, kondo, ncdu/dua/gdu et DevCleaner for Xcode, ce que chaque outil fait le mieux et comment ils se complètent.
sidebar:
  order: 1
---

lu-cleaner a volontairement un périmètre restreint. Il cible **les machines macOS des développeurs qui créent des applications React Native, iOS ou Android et travaillent avec des agents de code IA**. D'autres outils couvrent un champ plus large, ou fonctionnent sur davantage de plateformes. Cette page les compare honnêtement pour vous aider à choisir le bon, ou à en combiner plusieurs.

:::note
Les informations sur les autres outils proviennent de leur documentation publique, à jour en septembre 2026. Ces outils évoluent vite. Si quelque chose ici n'est plus exact, merci d'[ouvrir une issue](https://github.com/ludwig-pro/lu-cleaner/issues).
:::

## En un coup d'œil

| Outil | Ce que c'est | Plateformes | Interface | Sortie JSON | Licence · prix |
|---|---|---|---|---|---|
| **lu-cleaner** | Nettoyeur de disque pour développeurs | macOS | CLI + interface terminal | Oui (`scan`, `clean`, `doctor`, `history`, `analyze`) | MIT · gratuit |
| **CleanMyMac** (application) | Entretien de tout le Mac | macOS | Interface graphique | Non | Propriétaire · abonnement |
| **CleanMyMac CLI** | Nettoyage pour développeurs dans le terminal (bêta publique) | macOS | Menus dans le terminal | Non documenté | Propriétaire · gratuit pendant la bêta |
| **mole** | Nettoyage de tout le Mac, désinstallation, analyseur, état du système | macOS | CLI + interface terminal | Pour `analyze`, `status`, `history` | GPL-3.0 · gratuit (application payante distincte) |
| **npkill** | Recherche de `node_modules` | Tous (Node.js) | Interface terminal | Oui (`--json`, `--json-stream`) | MIT · gratuit |
| **kondo** | Nettoyeur d'artefacts de build pour plus de 20 écosystèmes | macOS, Linux, Windows | Invites en CLI, interface graphique optionnelle | Non documenté | MIT · gratuit |
| **ncdu, dua, gdu** | Analyseurs d'utilisation du disque | Tous | Interface terminal | Export (ncdu, gdu) | MIT · gratuit |
| **DevCleaner for Xcode** | Nettoyeur des données Xcode | macOS | Interface graphique (Mac App Store) | Non | GPL-3.0 · gratuit, pourboires facultatifs |

## Couverture pour les développeurs

| Outil | Worktrees des agents IA | Artefacts React Native / iOS / Android | Simulateurs et émulateurs | Données des outils d'IA | Vérifications avant suppression |
|---|---|---|---|---|---|
| **lu-cleaner** | Les trouve, affiche leur statut (modifié, non poussé, fusionné, orphelin), les supprime avec `git worktree remove` | `node_modules`, `ios/Pods`, `ios/build`, `android/app/build`, `.gradle`, `.cxx`, `.expo`… avec fichiers marqueurs et vérifications git | Appareils iOS, runtimes, dossiers d'appareils orphelins, enregistrements XCTest, journaux ; AVD Android, images système, NDK | Claude Code, Codex, Cursor, Conductor… avec détection des orphelins | Garde-fou de sécurité, inode inchangé, applications en cours d'exécution, bases de données ouvertes, processus à l'intérieur, `yes` à taper pour les éléments risqués |
| **CleanMyMac** (application) | Non documenté | Fichiers inutiles d'Xcode | Non documenté | Non documenté | Revue avant nettoyage |
| **CleanMyMac CLI** | Non documenté | `node_modules`, `Pods`, `.next`, `target`, `.venv`… plus les caches Xcode, Gradle et CocoaPods | Anciens simulateurs | Oui (Claude mentionné) | Revue, liste d'exclusion, artefacts récents non présélectionnés |
| **mole** | Nettoie les artefacts *à l'intérieur* des worktrees d'agents, jamais les worktrees eux-mêmes | `node_modules`, `build`, `dist`, `target`, `.build` | Non documenté | Non documenté | Simulation, liste blanche, artefacts récents non présélectionnés |
| **npkill** | Non | `node_modules` (autres noms avec `--targets`) | Non | Non | Signale les dossiers dont des applications ont besoin |
| **kondo** | Non | Node.js, Gradle, Swift, React Native et bien d'autres | Non | Non | Invite par projet, filtre `--older` |
| **ncdu, dua, gdu** | Non | Générique : c'est vous qui décidez ce qu'est un dossier | Non | Non | Confirmation uniquement |
| **DevCleaner for Xcode** | Non | DerivedData et Archives d'Xcode | Device support, journaux des simulateurs et des appareils (pas les appareils ni les runtimes) | Non | Revue dans l'application |

« Non documenté » signifie que la documentation de l'outil n'en parle pas, pas que la fonctionnalité est absente.

## Ce que lu-cleaner ne fait pas

- **Pas de désinstallation d'applications, pas d'analyse antimalware, pas d'« optimisation » de la RAM ou du système.** Utilisez CleanMyMac ou mole pour cela.
- **Pas de nettoyage au niveau système.** lu-cleaner ne supprime que dans votre dossier personnel et votre dossier temporaire utilisateur, et n'utilise jamais `sudo`. Les caches de `/Library` ou des dossiers d'autres utilisateurs sont hors périmètre.
- **macOS uniquement.** Pas de version Linux ni Windows.
- **Pas de nettoyage des disques externes.** Les éléments situés sur un autre volume sont signalés, jamais supprimés.
- **Pas d'interface graphique.** C'est un outil en ligne de commande, doté d'une interface interactive dans le terminal.

## Quel outil pour quel besoin

| Vous voulez… | Utilisez |
|---|---|
| Garder tout un Mac en ordre : restes d'applications, fichiers inutiles du système, désinstallation d'applications | CleanMyMac ou mole |
| Balayer rapidement les `node_modules` sur n'importe quel OS | npkill |
| Nettoyer les dossiers de build de projets Rust, Unity, .NET, Python… | kondo |
| Comprendre de quoi est fait un dossier, sur n'importe quelle machine, y compris des serveurs et des disques externes | ncdu, dua ou gdu |
| Une interface graphique dédiée aux données d'Xcode | DevCleaner for Xcode |
| Récupérer l'espace englouti par les worktrees d'agents, les projets React Native, les simulateurs, les émulateurs, les stores des gestionnaires de paquets et les données des outils d'IA, sans perdre de travail | lu-cleaner |

## Les utiliser ensemble

lu-cleaner ne conserve aucun état en dehors de son fichier d'historique, il n'entre donc pas en conflit avec d'autres nettoyeurs. Une combinaison courante :

1. Un nettoyeur généraliste (CleanMyMac ou mole) pour les fichiers inutiles du système et les restes d'applications.
2. lu-cleaner pour les données de développement : `lu-cleaner` pour le tableau de bord, ou `lu-cleaner clean --yes --smart` à intervalles réguliers (voir [Automatisation](/lu-cleaner/fr/guides/automation/)).
3. Un analyseur de disque, ou `lu-cleaner analyze`, quand quelque chose semble encore trop volumineux.

Deux choses distinguent lu-cleaner, même là où les outils se recoupent :

- **Il connaît l'état, pas seulement les noms de dossiers.** Un worktree avec des modifications non commitées, un simulateur démarré, une base de journaux Codex ouverte, des données Claude Code dont le dossier existe encore : lu-cleaner vérifie chacun de ces cas avant de proposer ou de supprimer quoi que ce soit. Voir le [modèle de sécurité](/lu-cleaner/fr/concepts/safety/).
- **Il explique ce qui revient et à quel prix.** Chaque élément a un [niveau de risque](/lu-cleaner/fr/concepts/risk-and-smart-select/) et une note qui indique comment il est régénéré, du cache qui se remplit tout seul au modèle qu'il faudrait retélécharger.

## Voir aussi

- [Fonctionnement](/lu-cleaner/fr/concepts/how-it-works/)
- [Ce qui est analysé](/lu-cleaner/fr/reference/scanners/)
- [FAQ](/lu-cleaner/fr/about/faq/)
