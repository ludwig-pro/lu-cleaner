# Scan réel du binaire actuel — 2 octobre 2026

Rapport historique des essais avant intégration. Le réglage finalement retenu
et sa validation sont dans [SCAN_ECO_FINAL.md](SCAN_ECO_FINAL.md).

Instantané avant la dernière optimisation. Voir
[les nouveaux essais et leurs limites](SCAN_LATENCY_OPTIMIZATION.md) pour
l'état actuel ; les mesures ci-dessous restent celles du binaire identifié ici.

**Le scan complet en eco a terminé en 7 min 52,85 s.** Pour un scan courant
dont l'utilisateur attend le résultat, cette durée reste trop longue.
Le correctif local ne peut pas être présenté comme une résolution de la
lenteur du scan général.

## Conditions

- Binaire local `v0.3.0-dirty`, SHA-256
  `a07f8aa78620970237931ab03624147255d9a83e8ab0d8b4352e1b2d9d19e679`.
- Vrai HOME, configuration habituelle : quatre racines de projets et
  trois racines de worktrees, aucune catégorie désactivée.
- Eco : 2 E/S, 1 commande, 1 prélecture, lots de 256, fenêtre cumulée
  de 2 ms et repos de 5 ms, GOMAXPROCS plafonné à 2, priorité arrière-plan.
- Aucun override GOMAXPROCS, LU_WALKERS, LU_NO_CACHE, LU_NO_BULK ou
  LU_NO_CLONES. Le cache existant n'a pas été vidé avant le test.
- Dry-run avec JSON et traces des opérations lentes :
  `LU_TRACE=1 LU_TRACE_MS=1000 ./bin/lu-cleaner scan --dry-run --verbose --json`.
- Limite de dix minutes prévue ; le scan a terminé spontanément avant
  cette limite. Aucun signal d'arrêt envoyé, aucune suppression.
- Aucune autre campagne de test ou compilation lu-cleaner en parallèle.
  Les autres applications de la machine continuaient de fonctionner.

## Résultat observé

| Mesure | Résultat |
| --- | ---: |
| Durée totale du processus | 472,846 s |
| Sortie | code 0, JSON valide, aucune erreur de provider |
| CPU cumulé user + système, retourné par wait4 | 170,477 s |
| Maximum RSS du processus | 91,25 Mio |
| Fichiers parcourus pour les tailles, comptage cumulé | 2 918 663 |
| Dossiers parcourus pour les tailles, comptage cumulé | 344 718 |
| Parcours de tailles | 9 220 |
| Maximum d'E/S admises | 2 / 2 |
| Maximum de commandes admises | 1 / 1 |
| Tailles réutilisées depuis le cache persistant | 0 |

Les compteurs de parcours ne dénombrent pas nécessairement des fichiers
uniques, car certains arbres peuvent se recouvrir. Le CPU cumulé est
équivalent à 36 % d'un cœur en moyenne sur la durée totale ; ce n'est pas
un pic ni 36 % de tous les cœurs de la machine. Le scan n'a pas provoqué
de gel observé pendant ce test.

Les providers s'exécutent en parallèle ; leurs durées ne s'additionnent pas :

| Provider | Durée |
| --- | ---: |
| Apple | 1 min 32,63 s |
| Android | 3 min 05,81 s |
| Système | 5 min 51,69 s |
| Worktrees | 6 min 51,91 s |
| JavaScript | 7 min 33,95 s |
| Artefacts | 7 min 46,27 s |
| Outils IA | 7 min 50,74 s |
| Catalogue | 7 min 52,53 s |

## Problème confirmé : aucune réutilisation du cache persistant

Le diagnostic final indique :

```text
cache hits 0 (0 files not walked), misses 9068
replay failed: 0 events in 9.779s (history unavailable or replay timed out)
saved 5736 entries (0 kept, 5736 new)
```

La lecture du code confirme qu'un échec de validation FSEvents empêche de
déclarer les tailles chargées valides. Le comportement conserve la sécurité,
mais impose de recalculer les tailles demandées. Les hits du cache mémoire
de l'invocation sont des compteurs différents : ils ne contredisent pas
l'absence de hits du cache persistant.

La cause exacte de cet échec reste à isoler : l'interface actuelle regroupe
historique indisponible et dépassement de délai dans le même diagnostic.
Cette mesure ne prouve pas que la priorité arrière-plan en soit la cause.
Elle identifie néanmoins un problème concret à examiner avant une nouvelle
modification des paramètres de régulation. Il faut ensuite vérifier qu'un
second scan complet réutilise effectivement ses tailles et mesurer sa durée.

Les quotas sont respectés, mais cela ne suffit pas à rendre l'attente
acceptable. Les 8 min 44 s de repos et 13 h 19 min d'attente E/S consignées
par le contrôleur sont des durées cumulées entre appelants/créneaux, pas des
durées murales. Elles ne permettent pas d'attribuer seules les huit minutes
de scan à un mécanisme particulier.

## Portée et traces

Une seule exécution réelle, avec des fichiers et activités externes qui
peuvent évoluer. Ce n'est ni une comparaison avant/après à données fixes,
ni une reproduction du gel initial. Un parcours contenant des entrées
illisibles a été exclu du cache, conformément aux protections existantes.
Aucun changement du code applicatif n'a été effectué pendant ce test.

Les traces complètes, le JSON de résultat, la configuration, les échantillons
de ressources et la synthèse restent dans le dossier temporaire local
`/private/tmp/lu-current-real-h7_7nzu6/`, car ils contiennent des chemins
personnels. `summary.json` et `samples.jsonl` contiennent les mesures finales
validées ; le compteur CPU de la console intermédiaire a été corrigé dans
ces fichiers après conversion des ticks Mach en secondes.
