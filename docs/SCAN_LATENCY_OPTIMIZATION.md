# Durée du scan et cohabitation — 2 octobre 2026

Rapport historique des essais avant intégration. Le réglage finalement retenu
et sa validation sont dans [SCAN_ECO_FINAL.md](SCAN_ECO_FINAL.md).

## Parallélisme : trois scans réels avec 2 / 4 / 2 E/S

Nouvelle comparaison à partir de 23 h 02 : trois scans complets sur le vrai
HOME, toutes les catégories et racines habituelles, dans l'ordre **2 E/S,
4 E/S, puis 2 E/S**. Le cache de tailles est désactivé pour les trois essais
(`LU_NO_CACHE=1`). Le cache de pages macOS n'est pas purgé ; le retour à 2 E/S
après le passage à 4 permet de vérifier si le gain subsiste malgré l'ordre
des passages. Les données et les activités externes restent variables.

La CLI travaille déjà en parallèle : une goroutine par provider et plusieurs
parcours de dossiers. Seul le budget d'admission des E/S est modifié avec
`LU_WALKERS`. Les trois passages conservent eco, la priorité arrière-plan,
`GOMAXPROCS <= 2`, une commande et une prélecture simultanées.

| Passage | Durée | CPU cumulé wait4 | CPU parent moyen | Pic CPU parent échantillonné | RSS maximale |
| --- | ---: | ---: | ---: | ---: | ---: |
| 2 E/S, référence | 3 min 15,697 s | 120,500 s | 54,2 % | 114,9 % | 91,33 Mio |
| 4 E/S | **2 min 10,341 s** | **117,776 s** | 79,8 % | 124,7 % | 100,59 Mio |
| 2 E/S, contrôle après | 3 min 41,480 s | 121,905 s | 48,5 % | 90,9 % | 90,53 Mio |

100 % de CPU représente **un cœur**, pas les dix cœurs logiques du Mac. Le CPU
wait4 peut inclure les enfants attendus ; les moyennes et pics échantillonnés
ne concernent que le processus parent, à intervalles d'environ une seconde.

Les trois scans terminent avec le code 0, les huit providers réussis et sans
arrêt forcé. Les deux premiers parcourent exactement 2 932 713 fichiers et
345 235 dossiers ; le dernier parcourt six fichiers et dix dossiers de plus
dans cet environnement actif. Les plafonds observés sont respectivement
2/2, 4/4 et 2/2 E/S, avec 1/1 commande dans tous les cas.

**Quatre E/S réduisent ici la durée de 33,4 % par rapport au premier contrôle
et de 41,1 % par rapport au second, sans hausse du temps CPU cumulé.** Cela
soutient l'intérêt d'un peu plus de concurrence des lectures sur ce Mac.
Ce n'est pas une mesure isolée du ralentissement d'un autre job, ni une
garantie sur d'autres machines. Le défaut eco reste à 2 E/S : cette étape
évalue l'override existant, sans changer le code applicatif ou les règles
de sécurité. Le timeout de validation du cache reste un problème distinct.

Pour utiliser le réglage testé tout en conservant le cache habituel :

```sh
LU_WALKERS=4 ./bin/lu-cleaner scan --dry-run --verbose
```

Pour reproduire le travail sans cache de cette comparaison, ajouter
`LU_NO_CACHE=1`. Les chiffres détaillés, paramètres et hashes sont dans
[les mesures 2/4/2](benchmarks/2026-10-02-io-workers-real.json).
Les traces privées sont conservées dans `/private/tmp/lu-current-real-ojy1a3dp/`,
`/private/tmp/lu-current-real-l70tgisi/` et
`/private/tmp/lu-current-real-fawlf_fk/`.

## Dernier essai réel : configuration et cache habituels, 22 h 17

À la demande d'un nouveau test réel, le binaire local a été relancé sur le
vrai HOME, toutes les catégories et racines habituelles, **sans vider, copier ou
préparer le cache**. Aucun override de configuration, cache, GOMAXPROCS ou
LU_WALKERS. La commande exécutée est celle utilisée dans le terminal :

```sh
LU_TRACE=1 LU_TRACE_MS=1000 ./bin/lu-cleaner scan --dry-run --verbose
```

Les sorties texte sont capturées dans des fichiers pour la mesure ; ce test
porte sur le scan complet, pas sur l'affichage interactif du spinner. Aucun
autre scan ou build lu-cleaner n'est exécuté en parallèle.

| Mesure | Résultat |
| --- | ---: |
| Durée réelle totale | **4 min 05,025 s** |
| Fin | Code 0, 8 providers terminés, aucune erreur de provider |
| Fichiers / dossiers parcourus, compteurs cumulés | 2 931 190 / 345 124 |
| Tailles réutilisées depuis le cache persistant | **0** |
| Maximum E/S / commandes admises | 2 / 1 |
| CPU parent moyen échantillonné, 100 % = un cœur | 45,9 % |
| Pic CPU parent échantillonné sur environ une seconde | 100,5 % d'un cœur |
| CPU user + système wait4, enfants attendus inclus | 128,966 s |
| RSS maximale | 92,84 Mio |

La validation du cache réel échoue : FSEvents reçoit 184 968 événements mais
ne termine pas la relecture dans le budget de 8 secondes. La validation complète
prend 11,008 s, avec les inspections préparatoires. Les 5 736 tailles chargées
ne sont donc pas considérées valides, puis les arbres sont recalculés. Le scan
enregistre normalement les nouvelles mesures admissibles à sa fermeture.

**Ce test confirme que le lancement habituel peut encore durer plusieurs
minutes.** Le résultat précédent de 73 secondes concernait un cache validé ;
il n'est pas représentatif de ce lancement. Le problème de timeout du cache
est maintenant reproduit et distingué d'un échec de création du flux. Sa cause
précise et sa correction restent à traiter. Aucun changement du code applicatif
n'a été effectué pendant cet essai.

Preuves locales : `/private/tmp/lu-current-real-slhplyvr/` contient
`metadata.json`, `scan.log`, `report.txt`, `samples.jsonl` et `summary.json`.
Les mesures agrégées sans chemins personnels figurent sous
`after_real_default_cache_2217` dans
[le fichier de résultats](benchmarks/2026-10-02-latency-real.json).

## Choix retenu

Eco conserve les plafonds partagés de **2 E/S, 1 commande et 1 prélecture**, les
lots de 256 entrées, `GOMAXPROCS <= 2` et la priorité macOS d'arrière-plan. Les
pauses artificielles sont désactivées. Fast et les règles de sécurité ne changent
pas. Le cache ne réutilise toujours que des tailles validées ; aucun résultat
partiel ou douteux n'est accepté pour gagner du temps.

Le réglage précédent attendait 5 ms après 2 ms cumulées de détention d'un permis.
Cette durée comprend aussi les attentes dans le noyau : ce n'est pas du temps
CPU. Ajouter une temporisation après ces attentes retarde inutilement le scan
et multiplie les réveils, alors que macOS régule déjà les E/S d'arrière-plan.

Le contrôleur conserve son mécanisme interne de temporisation et ses tests,
mais aucun profil public ne l'active. `config show` expose zéro pour
`pause_ms` et `work_window_ms`. Les diagnostics `LU_TRACE=1` montrent maintenant
la fin de validation du cache immédiatement, ainsi que la cause native d'un
échec de relecture FSEvents. Les schémas JSON de scan et nettoyage restent
inchangés ; ces messages vont sur stderr.

## Méthodes utilisées ailleurs

- Apple recommande de choisir une qualité de service adaptée au travail :
  celle-ci règle notamment les priorités CPU et E/S. Le système peut alors
  arbitrer avec les autres applications. Voir le
  [guide Apple sur la QoS](https://developer.apple.com/library/archive/documentation/Performance/Conceptual/EnergyGuide-iOS/PrioritizeWorkWithQoS.html).
- Syncthing documente la baisse de priorité du processus, la limitation du
  parallélisme Go et l'utilisation de notifications de changements. Voir sa
  [FAQ sur la consommation CPU](https://docs.syncthing.net/users/faq.html#why-does-it-use-so-much-cpu).
- Watchman conserve des index et permet les requêtes depuis un événement connu
  pour éviter de refaire une exploration complète à chaque requête. Voir sa
  [documentation des requêtes](https://facebook.github.io/watchman/docs/file-query).

L'application à lu-cleaner est une décision de conception : limiter le travail
simultané, laisser l'ordonnanceur privilégier les autres applications et réutiliser
les calculs dont la validité est prouvée. Le cache FSEvents existait déjà ; cette
modification ne prétend pas introduire un nouvel index ou résoudre tous les
échecs de relecture.

## Expérience isolant le coût des pauses

Mesure réelle du même `node_modules`, sans cache de tailles : 94 379 fichiers,
12 998 dossiers. Deux répétitions de chaque variante, ordre inversé au second
passage. Même priorité d'arrière-plan, deux E/S et deux processeurs Go.
Le cache de pages macOS n'est pas purgé.

| Réglage | Durées | CPU cumulé médian | Taille et résultat |
| --- | --- | ---: | --- |
| Ancien eco local, fenêtre 2 ms / pause 5 ms | 15,170 s ; 13,075 s | 4,502 s | Identiques |
| Eco sans pauses artificielles | 4,524 s ; 4,291 s | 2,951 s | Identiques |

La durée médiane est divisée par **3,20** et le temps CPU cumulé baisse de
**34,4 %**. L'égalité porte aussi sur les octets récupérables, hardlinks/clones,
nombres de fichiers/dossiers, dernière modification et erreurs. Le maximum
d'E/S admises est 2 pour toutes les variantes.

Une priorité E/S `utility` et une priorité normale ont aussi été sondées.
Ces réglages ne sont pas retenus : cette expérience démontre déjà le coût des
pauses sans nécessiter de relever la priorité du scan. Les sondes ne prouvent
pas que la suppression de la priorité d'arrière-plan préserverait les autres
applications.

## Comparaison CLI sur données fixes

Douze racines temporaires, 32 packages par racine, arbre profond, dossier de
2 048 entrées, hardlinks et clone APFS. Trois répétitions par profil/cache ;
création exclue des mesures, cache de pages système non purgé. `baseline`
désigne ici l'ancien binaire local avec fenêtre 2 ms / pause 5 ms, pas la
release v0.3.0 ni la version antérieure aux protections.

| Profil | Cache de tailles | Durée médiane | CPU médian | RSS médiane |
| --- | --- | ---: | ---: | ---: |
| Ancien eco local | Vide | 2,141 s | 0,327 s | 26,44 Mio |
| Nouvel eco | Vide | 0,685 s | 0,268 s | 27,38 Mio |
| Fast | Vide | 0,133 s | 0,207 s | 29,36 Mio |
| Ancien eco local | Réutilisé | 3,138 s | 0,282 s | 19,27 Mio |
| Nouvel eco | Réutilisé | 0,568 s | 0,225 s | 19,72 Mio |
| Fast | Réutilisé | 0,158 s | 0,109 s | 21,28 Mio |

**Les 18 résultats finaux sont identiques** après normalisation de l'âge :
chemins, tailles, risques et décisions conservés. La nouvelle version eco
termine 3,13 fois plus vite sans cache que l'ancien réglage local. L'annulation
eco après le premier parcours terminé rejoint le processus en 5,4 ms, code 130.
Le résultat sur de petites fixtures ne prédit pas la durée du scan complet.

### Effet sur des tâches concurrentes

Les scans de fixtures sans cache sont répétés pendant une quantité fixe de
travail témoin. Chaque essai est encadré par deux passages du témoin seul ;
l'ordre des profils tourne entre les répétitions. Le ralentissement compare
le temps concurrent à la moyenne de ces deux contrôles.

| Témoin | Répétitions | Ancien eco local, médiane | Nouvel eco, médiane | Fast, médiane |
| --- | ---: | ---: | ---: | ---: |
| CPU : deux workers SHA-256, 64 000 itérations chacun | 3 | −4,0 % | −2,1 % | +8,6 % |
| Disque : 1 024 itérations, F_NOCACHE et F_FULLFSYNC | 5 | −10,1 % | +2,7 % | −7,2 % |

Les valeurs négatives reflètent le bruit de mesure, pas une accélération causée
par le scan. L'objectif inférieur à 15 % est atteint **en médiane** pour le
nouvel eco ; il ne l'est pas à chaque essai disque : dispersion de −44,7 % à
+55,8 %, avec de fortes variations des témoins seuls. Il serait incorrect de
promettre une borne de 15 % dans toutes les conditions sur cette base.

Un premier témoin disque de 512 itérations avec `fsync` durait souvent moins
d'une seconde et présentait une forte dispersion (jusqu'à +332 % pour un essai
eco). Il est conservé dans les résultats bruts, mais n'est pas utilisé pour
conclure à un ralentissement disque stable. La seconde campagne ci-dessus
double le travail et impose une synchronisation complète pour allonger la
mesure. Aucun essai n'a été éliminé des fichiers de résultats.

Les mesures ont lieu avant les builds/tests, dans des processus où
`GOMAXPROCS`, `GOFLAGS` et `LU_WALKERS` sont absents. Tous les scans concurrentiels
se terminent normalement ou sur l'annulation demandée par le banc ; aucun
arrêt forcé n'a été nécessaire.

## Scans complets sur le vrai dossier utilisateur

Mac14,9, 10 cœurs logiques, 16 Gio, macOS 26.5.1 arm64, Go 1.25.6. Tous les
providers, racines automatiques habituelles, commande `scan --dry-run --verbose
--json`, `LU_TRACE=1 LU_TRACE_MS=1000`. Aucun build ou test lu-cleaner en parallèle.
Les autres applications et jobs continuent : les données et la charge ne sont
pas figées. L'état mémoire observé pendant le passage sans cache comporte
environ 17,4 Gio de swap utilisé.

| Passage | Durée | CPU user + système | RSS maximale | Résultat |
| --- | ---: | ---: | ---: | --- |
| Ancien eco local, validation du cache échouée | 7 min 52,846 s | 170,477 s | 91,25 Mio | 8 providers, code 0 |
| Nouvel eco, cache validé | 1 min 13,198 s | 56,977 s | 89,64 Mio | 8 providers, code 0 |
| Nouvel eco, cache désactivé explicitement | 7 min 35,232 s | 131,886 s | 87,36 Mio | 8 providers, code 0 |

Les trois sorties sont du JSON valide sans erreur de provider. Le nouveau
passage avec cache réutilise 5 673 tailles et évite le parcours de 1 894 711
fichiers ; il mesure encore 1 024 316 fichiers. Sans cache, 2 927 230 fichiers
et 344 729 dossiers sont parcourus, en comptage cumulé. Ces compteurs ne
représentent pas nécessairement des entrées uniques.

**Le passage de presque huit minutes à 73 secondes inclut l'effet du cache.**
Il ne mesure donc pas le seul gain du code. Le passage complet sans cache
reste lent sur cette machine occupée ; les 17,6 secondes gagnées ne permettent
pas de promettre une forte accélération du premier scan. Une période d'environ
deux minutes presque sans consommation CPU a été observée au début de ce
passage ; sa cause précise n'est pas établie par ces traces.

Un essai supplémentaire de l'ancien binaire avec le même instantané initial de
cache valide a été arrêté après 120 s, avant la fin. Il n'est pas utilisé comme
une durée complète avant/après. La sortie coopérative a pris 110 ms (mesure
externe échantillonnée), dont 39 ms rapportées par le contrôleur.

Les nouveaux scans respectent 2/2 E/S et 1/1 commande, avec zéro pause logicielle.
Les CPU moyens sont respectivement 77,8 % et 29,0 % d'un cœur. Le pic du processus
échantillonné sur environ une seconde atteint 110,7 % d'un cœur avec cache,
93,5 % sans cache. **100 % représente un cœur, pas les dix cœurs du Mac.**
Le temps CPU `wait4` peut inclure les enfants attendus ; les échantillons de pic
et d'activité disque ne couvrent que le processus parent.

## Cache : ce qui a été vérifié

Six sondes de relecture d'un instantané du vrai cache, en eco/fast et avec
différents ensembles de racines, ont réussi en 153 à 558 ms. Les scans avec
cache ont aussi validé leur historique. Ces sondes isolées n'ont pas reproduit
l'échec ; l'essai réel ultérieur de 22 h 17 ci-dessus reproduit en revanche
un dépassement du délai. Le curseur d'événements était déjà
mis à jour correctement à la fermeture : aucune modification de cet algorithme
n'a été nécessaire.

Les invalidations de clones APFS, fichiers ouverts en écriture, modifications
et erreurs restent conservatrices. Un cache invalide impose toujours un nouveau
calcul. Les nouveaux diagnostics permettront de distinguer un timeout, un
échec de démarrage du flux et une limite de tampon si le problème réapparaît.

## Reproduire et diagnostiquer

```sh
make build
./bin/lu-cleaner config show --json
LU_TRACE=1 LU_TRACE_MS=1000 ./bin/lu-cleaner scan --dry-run --verbose --json >scan.json 2>scan.log
```

Un second passage peut réutiliser les tailles validées. Pour mesurer volontairement
le coût sans cache, ajouter `LU_NO_CACHE=1` ; cela peut durer plusieurs minutes.
Le dry-run continue d'effectuer toutes les découvertes et vérifications.

Les résultats bruts des scans réels restent dans des dossiers temporaires privés
(ils contiennent des chemins personnels). Les mesures agrégées et paramètres
du benchmark sont disponibles dans
[les mesures réelles](benchmarks/2026-10-02-latency-real.json),
[les fixtures et témoins initiaux](benchmarks/2026-10-02-latency-fixtures.json) et
[le témoin disque prolongé](benchmarks/2026-10-02-latency-io.json).

Pour reproduire les campagnes synthétiques avec deux binaires conservés :

```sh
python3 scripts/bench_scan.py --baseline /chemin/ancien-lu-cleaner --current ./bin/lu-cleaner --output /tmp/scan-comparison.json --samples 3 --packages 32 --cpu-iterations 64000 --io-iterations 512 --io-sync fsync --io-no-cache
python3 scripts/bench_scan.py --baseline /chemin/ancien-lu-cleaner --current ./bin/lu-cleaner --output /tmp/scan-io.json --samples 5 --packages 32 --witness-only --witness-kind io --io-iterations 1024 --io-sync fullfsync --io-no-cache
```

L'annulation demeure coopérative : les attentes logicielles et boucles s'arrêtent,
mais un appel noyau déjà bloqué ne peut pas être interrompu instantanément.
La priorité arrière-plan peut augmenter fortement la durée lorsque le disque
ou la mémoire sont déjà sous pression ; les plafonds ne sont pas une garantie
de pourcentage CPU ni de durée maximale.

## Vérifications et état local

- `go test -json ./...` : 23 packages testés, 763 tests/sous-tests réussis,
  16 ignorés. Les skips concernent les tests opt-in de vrais dossiers, montages
  d'images disque, TUI manuelle, processus auxiliaires et conditions de plateforme
  non applicables. Les deux autres packages ne contiennent pas de tests.
- `go test -race ./internal/scanctl ./internal/fsx ./internal/fsevents
  ./internal/engine ./internal/cli ./internal/tui` : six packages réussis.
- `go vet ./...`, `make build`, `make nocgo`, `make universal`, `make docs` :
  réussis. La génération documentaire ne produit pas de diff supplémentaire.
- `npm run build` dans `site` : 81 pages, liens internes valides.
- `git diff --check` : réussi. Le test de régression sans temporisation a
  échoué avant la modification de Resolve, puis réussi avec le nouveau profil.

Les contrôles Go utilisent un cache de compilation temporaire, `GOMAXPROCS=2`
et `GOFLAGS=-p=2` pour la compilation et les tests uniquement. Ces variables
ne sont pas présentes pendant les benchmarks applicatifs. Le linker signale
des avertissements `LC_DYSYMTAB` pour les exécutables du race detector et des
objets SDK macOS 26 liés avec une cible 12 pour le build local ; aucun contrôle
n'échoue. Les builds universels sont produits, sans essai runtime sur un Mac Intel
ou sous macOS 12 dans cette session.

Le binaire mesuré et reconstruit localement a le SHA-256
`dff2ff903501665ec2d038fecd2afd82d311007d28c66e2f9f69a0617a8efa2d`.
Le binaire précédent conservé pour comparaison a le SHA-256
`a07f8aa78620970237931ab03624147255d9a83e8ab0d8b4352e1b2d9d19e679`.

Checkout final : branche `main`, HEAD `6670034`, **13 fichiers suivis modifiés
et 10 fichiers non suivis**, y compris les rapports et changements préexistants.
Cette étape modifie le profil dans `scanctl`, adapte ses tests et les diagnostics
CLI, ajoute les traces de validation dans `fsx`/`fsevents`, met à jour les pages
EN/FR et ajoute ce rapport avec trois fichiers de mesures. Aucun commit, push
ou changement de release n'est effectué pendant cette optimisation.
