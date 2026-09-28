---
title: Raccourcis clavier
description: Toutes les touches du sélecteur interactif (tableau de bord, clean, worktrees, artifacts, devices) et de l'analyseur de disque (analyze), y compris celles des fenêtres de confirmation.
sidebar:
  order: 4
---

lu-cleaner propose deux écrans interactifs :

- **Le sélecteur** s'ouvre avec `lu-cleaner`, `lu-cleaner clean`, `lu-cleaner devices`, `lu-cleaner worktrees` et `lu-cleaner artifacts`. Les deux dernières commandes sautent la vue d'ensemble des catégories et s'ouvrent directement sur la liste des éléments.
- **L'analyseur** s'ouvre avec `lu-cleaner analyze [path]` : un explorateur à la ncdu pour n'importe quelle arborescence.

Appuyez sur `?` dans l'un ou l'autre pour afficher l'aide intégrée ; n'importe quelle touche la referme. Les flèches et leurs équivalents Vim fonctionnent partout.

## Sélecteur

### Navigation

| Touche | Action |
| --- | --- |
| `↑` / `k` | Monter. |
| `↓` / `j` | Descendre. |
| `pgup` / `ctrl+b` | Page précédente. |
| `pgdown` / `ctrl+f` / `ctrl+d` | Page suivante. |
| `home` / `g` | Première ligne. |
| `end` / `G` | Dernière ligne. |
| `→` / `l` / `enter` | Vue d'ensemble des catégories : ouvrir la catégorie. Liste des éléments : afficher ou masquer le panneau de détails. |
| `←` / `h` / `esc` / `backspace` | Revenir à la vue d'ensemble des catégories. |
| `tab` / `shift+tab` | Catégorie suivante / précédente, depuis une liste d'éléments. |

Dans `worktrees` et `artifacts`, qui n'ont pas de vue d'ensemble des catégories, `←`, `tab` et `shift+tab` sont sans effet.

### Sélection

| Touche | Action |
| --- | --- |
| `space` | Liste des éléments : sélectionner ou désélectionner l'élément sous le curseur. Vue d'ensemble des catégories : sélectionner tous les éléments nettoyables de la catégorie **sauf** les éléments <span class="risk caution">caution</span> ; si une partie de la catégorie est déjà sélectionnée, désélectionner plutôt toute la catégorie. |
| `a` | Sélection intelligente sur la vue : les éléments recommandés sont sélectionnés, tous les autres désélectionnés. |
| `A` | Sélectionner tous les éléments nettoyables de la vue, éléments <span class="risk caution">caution</span> compris. |
| `n` | Désélectionner tous les éléments de la vue. |
| `i` | Inverser la sélection de la vue. |

« La vue » désigne la liste d'éléments affichée (après un éventuel filtre de texte) quand une catégorie est ouverte, et tous les éléments visibles quand vous êtes sur la vue d'ensemble des catégories. Les éléments en rapport seul ne peuvent jamais être sélectionnés : `space` sur l'un d'eux en indique la raison dans la ligne d'état.

La sélection intelligente s'exécute aussi une fois, automatiquement, à la fin de l'analyse : toujours avec `lu-cleaner`, avec `lu-cleaner clean` sauf si vous passez `--no-smart`, et avec `worktrees`, `artifacts` et `devices` uniquement si vous passez `--smart`. Voir [Niveaux de risque et sélection intelligente](/lu-cleaner/fr/concepts/risk-and-smart-select/).

### Affichage

| Touche | Action |
| --- | --- |
| `s` | Liste des éléments : alterner le tri entre taille (les plus gros d'abord), âge (les plus anciens d'abord) et nom. Vue d'ensemble des catégories : basculer entre l'ordre par défaut et le tri par taille. |
| `/` | Filtrer par texte (voir [Filtre de texte](#filtre-de-texte)). |
| `H` / `.` | Afficher ou masquer les entrées vides qui ne peuvent pas être sélectionnées (taille 0, purement informatives). Elles sont masquées par défaut. |

### Actions

| Touche | Action |
| --- | --- |
| `d` / `x` | Nettoyer la sélection. Ouvre la [fenêtre de confirmation](#fenêtre-de-confirmation). |
| `t` | Activer ou désactiver le mode Corbeille : les éléments sélectionnés sont déplacés dans `~/.Trash` au lieu d'être supprimés. L'espace n'est libéré qu'une fois la Corbeille vidée. |
| `o` | Afficher l'élément sous le curseur dans le Finder (liste des éléments uniquement). |
| `?` | Aide. Elle liste aussi les scanners qui ont échoué, le cas échéant. |
| `q` / `ctrl+c` | Quitter. |

### Filtre de texte

`/` ouvre une ligne de filtre en bas de l'écran. Le filtre recherche, sans tenir compte de la casse, n'importe où dans le nom, l'emplacement, le type, le projet ou l'identifiant du scanner d'un élément.

| Touche | Action |
| --- | --- |
| N'importe quel caractère | L'ajouter au filtre. La liste se met à jour pendant la saisie. |
| `backspace` | Effacer le dernier caractère. |
| `alt+backspace` / `ctrl+w` | Effacer le dernier mot. |
| `ctrl+u` | Vider la ligne de filtre. |
| `↑` / `↓` | Se déplacer dans la liste pendant la saisie. |
| `enter` | Conserver le filtre et revenir à la liste. Le fil d'Ariane affiche `filter "…"`. |
| `esc` | Effacer le filtre et revenir à la liste. |

Un filtre conservé reste actif pendant que vous naviguez. Appuyez sur `esc` sur la vue d'ensemble des catégories (ou dans les listes de `worktrees` et `artifacts`) pour l'effacer.

### Fenêtre de confirmation

Les touches dépendent de la présence d'éléments <span class="risk caution">caution</span> dans la sélection.

| Sélection | Confirmer | Annuler |
| --- | --- | --- |
| Aucun élément caution | `y` ou `enter` | `n`, `esc` ou `q` |
| Au moins un élément caution | taper `yes`, puis `enter` | `esc` |

Pendant la saisie de `yes`, `backspace`, `alt+backspace`, `ctrl+w` et `ctrl+u` modifient la saisie. `ctrl+c` quitte lu-cleaner sans rien nettoyer.

### Pendant et après le nettoyage

| Écran | Touche | Action |
| --- | --- | --- |
| Progression | `ctrl+c` | Annuler : les éléments en cours se terminent, les autres sont ignorés avec le motif `cancelled`. Les autres touches sont sans effet. |
| Récapitulatif | n'importe quelle touche | Revenir aux listes. Les éléments nettoyés disparaissent ; les éléments ignorés ou en échec restent sélectionnés. |
| Récapitulatif | `ctrl+c` | Quitter. |

## Analyseur

`lu-cleaner analyze` démarre dans votre dossier personnel ; passez un chemin pour démarrer ailleurs. Les entrées sont triées par taille et mesurées en arrière-plan.

### Navigation

| Touche | Action |
| --- | --- |
| `↑` / `k`, `↓` / `j` | Monter / descendre. |
| `pgup` / `ctrl+b`, `pgdown` / `ctrl+f` / `ctrl+d` | Page précédente / suivante. |
| `home` / `g`, `end` / `G` | Première / dernière entrée. |
| `→` / `l` / `enter` | Ouvrir le dossier sous le curseur. Les liens symboliques ne sont jamais suivis. |
| `←` / `h` / `backspace` | Remonter au dossier parent, jusqu'à `/`. |

### Marquer et supprimer

| Touche | Action |
| --- | --- |
| `space` | Marquer ou démarquer l'entrée sous le curseur, puis descendre. |
| `esc` | Effacer toutes les marques. |
| `d` / `x` / `delete` | Supprimer les entrées marquées, ou l'entrée sous le curseur si rien n'est marqué. |

`delete` est la touche d'effacement vers l'avant (`fn` + `⌫` sur un clavier Mac). La touche `⌫` seule envoie `backspace`, qui remonte au dossier parent.

La suppression vous demande toujours de taper `yes` puis d'appuyer sur `enter` (`esc` annule), car on peut tout sélectionner dans l'analyseur. Chaque chemin passe tout de même par le [garde-fou de sécurité](/lu-cleaner/fr/concepts/safety/), et un worktree git lié est retiré avec `git worktree remove` au lieu d'être supprimé. `--dry-run` et `--trash` s'appliquent ici aussi.

Pendant une suppression, `ctrl+c` annule les entrées restantes et quitte une fois celles en cours terminées. Sur l'écran de résultat, n'importe quelle touche ramène à la liste.

### Affichage et actions

| Touche | Action |
| --- | --- |
| `s` | Alterner le tri entre taille (les plus gros d'abord), nom et âge (les plus anciens d'abord). |
| `.` | Masquer ou afficher les fichiers cachés (dotfiles). Ils sont affichés par défaut. |
| `r` | Réanalyser le dossier courant. |
| `o` | Afficher l'entrée sous le curseur dans le Finder. |
| `?` | Aide. |
| `q` / `ctrl+c` | Quitter. |

## Voir aussi

- [Votre premier nettoyage](/lu-cleaner/fr/getting-started/first-cleanup/) : le sélecteur, écran par écran.
- [`lu-cleaner analyze`](/lu-cleaner/reference/commands/analyze/) et [`lu-cleaner clean`](/lu-cleaner/reference/commands/clean/) : les options de la ligne de commande.
