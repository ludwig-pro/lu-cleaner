# Scans économes : implémentation et validation locale

Ce rapport décrit la v0.3.0. La correction ultérieure du coût des pauses,
ses mesures et ses limites sont dans [SCAN_PACING_FIX.md](SCAN_PACING_FIX.md).
Le réglage local le plus récent est documenté dans
[SCAN_ECO_FINAL.md](SCAN_ECO_FINAL.md).

Date : 2 octobre 2026. Base auditée et checkout initial : `main`,
`08e5ed43c901c4c5b3da089f4a9610107ad85156`. Le checkout était propre avant
l'implémentation ; aucun changement intervenu depuis la base auditée.
Ce rapport conserve les mesures et l'état du checkout à la fin de la
validation locale. La revue avant publication a ensuite complété la
propagation du contexte dans doctor et la confirmation de nettoyage TUI,
sans modifier le moteur ni les parcours artifacts utilisés par le benchmark.

## Comportement livré

`--scan-mode eco|fast` suit la priorité option explicite, configuration TOML,
puis `eco`. Une valeur inconnue échoue avant le scan. `LU_WALKERS` positif
remplace le budget d'E/S partagé ; une valeur invalide garde le défaut du
profil et produit un diagnostic uniquement avec `--verbose`.

| Paramètre | eco | fast |
| --- | ---: | ---: |
| Lots d'E/S simultanés | 2 | 8 |
| Commandes de scan simultanées | 1 | 4 |
| Workers de mesure anticipée | 1 | 4 |
| Entrées par lot | 256 | 256 |
| Repos du créneau après opération | 5 ms | 0 |
| GOMAXPROCS temporaire | min(valeur héritée, 2) | valeur héritée |
| Politique macOS | arrière-plan | héritée |

La politique s'applique à `scan` et son dry-run, au dashboard, aux commandes
spécialisées, au scan avant `clean`, à `analyze` et à `doctor`, même avec
`--no-scan` puisque la Corbeille reste mesurée. La TUI conserve la politique
pendant un nettoyage. Les commandes sans scan ne l'activent pas.
`config show` présente les limites résolues sans les appliquer. Les schémas
JSON de scan, nettoyage et analyse restent inchangés ; les diagnostics vont
sur stderr.

## Points d'intégration

- `internal/scanctl` résout les profils, transporte le contrôleur par contexte
  et admet les E/S et commandes. Un créneau en repos reste indisponible aux
  autres goroutines. Les statistiques séparent travail et temporisation ;
  l'horloge et l'attente sont injectables dans les tests.
- `internal/cli/scanmode.go`, `setup.go` et les entrées CLI réutilisent un
  contrôleur par invocation, y compris les collectes imbriquées de doctor.
  Les lectures initiales des dossiers parents et des racines s'effectuent
  après activation, avec ce même contrôleur. La résolution utilisée par
  l'affichage de configuration reste sans effet sur le processus.
  L'activation macOS utilise les constantes publiques du SDK
  `PRIO_DARWIN_PROCESS=4` et `PRIO_DARWIN_BG=0x1000`, avec restauration des seuls
  réglages appliqués. Une politique d'arrière-plan héritée reste en place.
  Un échec OS avertit une fois ; les quotas logiciels restent actifs.
- `internal/fsx` admet les vraies lectures, y compris les racines, fichiers
  isolés et attributs APFS. Bulk et fallback vérifient le contexte entre les
  entrées, découpent le traitement en lots et libèrent les permis avant les
  callbacks et la récursion. Les helpers de découverte préservent le tri et
  les jeux de noms nécessaires à la classification. Les sondes de marqueurs
  répétées dans les callbacks artifacts/worktrees prennent leur propre permis,
  après libération de celui du walker, même avec un budget de 1.
- `internal/providers/internal/scanio`, `scanwalk` et `scanmemo` mutualisent
  les lots de métadonnées, les parcours et les résultats en cours. Les
  verrous ne couvrent pas les attentes de quota ou inspections lentes.
  L'inventaire détaillé des accès et sondes bornées est dans
  [SCAN_IO_INVENTORY.md](SCAN_IO_INVENTORY.md).
- Les artifacts partagent leur pool de découverte entre toutes les racines.
  Leur prefetch spéculatif conserve au maximum 128 chemins en attente ; un
  refus d'anticipation ne supprime jamais une mesure obligatoire. La file
  TUI contient les mesures obligatoires des listes et se vide à la fermeture.
- `core.Env.OutputTimeout` et `sysx` admettent les commandes avant de créer
  leur timeout d'exécution. Une deadline du parent reste prioritaire.
  Groupes de processus, WaitDelay et protections Git sont conservés.
- Les inspections natives de PID, descripteurs, régions et fichiers ouverts
  en écriture sont annulables et ne publient pas de résultat partiel comme
  un état complet. Une inspection interrompue reste inconnue/incomplète.
  Doctor transmet le même contexte à ses vérifications de processus et
  ne publie aucun rapport après annulation, même avec `--no-scan`. La
  confirmation TUI rejoint son inspection à la fermeture, annule celle d'un
  dialogue abandonné et rejette les résultats d'un ancien dialogue.
- Le cache de tailles déduplique avant l'admission. Annuler un consommateur
  ne coupe pas le calcul partagé attendu par un autre. Le moteur rejoint les
  mesures possédées par le scan avant de fermer le store. La fermeture du
  replay FSEvents rejoint aussi son watcher avant de libérer sa mémoire C.
- La TUI possède explicitement ses tâches de scan et rejoint les workers à
  sa fermeture. Les changements de dossier et rescans annulent les requêtes
  remplacées et rejettent leurs résultats devenus obsolètes.
- La découverte Android conserve profondeur, plafond de 250 000 dossiers et
  états incomplets, sans le timeout mural de 20 secondes incompatible avec
  le ralentissement volontaire.

Les règles de symlinks, volumes, TCC/FDA, exclusions, Git, processus actifs,
hardlinks/clones APFS, risque, sélection et revalidation avant suppression
restent couvertes par la suite existante et les nouvelles régressions.

## Vérifications

Contrôles sur le diff stabilisé :

| Commande | Résultat |
| --- | --- |
| `GOMAXPROCS=4 go test -p 2 -count=1 -json ./...` | Succès : 23 packages, 758 tests/sous-tests réussis, 16 entrées ignorées expliquées ci-dessous. |
| `GOMAXPROCS=2 go vet -p 2 ./...` | Succès. |
| `make build` | Succès, binaire natif CGO. |
| `make nocgo` | Succès, compilation et vet sans CGO. |
| `make universal` | Succès, compilation Darwin arm64/amd64 et assemblage avec lipo. |
| `make docs` | Succès, 16 pages de commandes régénérées et catalogue. |
| `cd site && npm run build` | Succès : 81 pages, tous les liens internes valides. |

Les cibles Make ont été exécutées avec un cache Go temporaire neuf,
`GOMAXPROCS=2` et `GOFLAGS=-p=2`. Ces limites concernent la compilation,
pas les binaires mesurés. Le race detector a réussi pour `scanctl`, `fsx`,
`fsevents`, `engine`, `core`, `safety`, `sysx`, `cli`, `config`, `tui` et
`internal/providers/...`. Après les dernières modifications, `cli`,
`artifacts` et `worktrees` ont été revérifiés avec `-race`. Après le complément
doctor/TUI de la revue avant publication, la suite complète, vet et les
builds ont été relancés, ainsi que `-race` sur `cli` et `tui`.
Les tests natifs complets s'exécutent hors sandbox : celle-ci bloque les API
FSEvents, l'inspection des processus et la politique de priorité macOS.

La CI de revue a également révélé une course dans la fixture Git existante :
la maintenance automatique supprimait `objects/maintenance.lock` pendant
que le test vieillissait ses fichiers. La maintenance et le GC automatiques
sont désactivés dans les commandes de création de cette seule fixture.
`TestRealGitDirtyActivity` a ensuite réussi 30 exécutions consécutives ;
aucun paramètre Git utilisateur ni comportement de production n'est modifié.

Les tests natifs avec `-race` émettent parfois un avertissement du linker
Apple sur `LC_DYSYMTAB` dans un objet Go ; leurs exécutions réussissent.
Le build du site signale aussi des avertissements Astro/Vite de dépréciation
et de directive, sans erreur ni lien cassé.

Régressions significatives :

| Garantie | Preuve automatisée |
| --- | --- |
| Un budget partagé entre providers, racines, découverte et prélectures | `TestProvidersRootsAndDiscoveryShareBudget`, `TestTwelveRootsShareActualIOAdmission` : 12 racines, admission réelle mesurée, plafond 1 respecté. |
| Repos du créneau et acquisitions annulables | `TestCooldownRetainsSlotAndCancels`, `TestSharedAdmissionAndCanceledWait` : horloge injectée/barrières, créneau indisponible aux autres pendant son repos. |
| Permis libéré sur panique, aucun travail avec un contexte annulé | `TestPanicReleasesAndCanceledContextNeverStarts`. |
| Aucun deadlock avec récursion/callback/cache et budget 1 | `TestBudgetOneReentrantCallbackAndCache`, tests des callbacks artifacts/worktrees avec un seul worker. |
| Arrêt pendant le dossier courant | `TestCancelStopsCurrentDirectoryBulkAndFallback` : 2 048 entrées, interruption après le premier callback, en bulk et fallback. |
| Timeout d'exécution indépendant de la file d'attente | `TestCommandTimeoutStartsAfterAdmission` : admission bloquée plus longtemps que le timeout propre à la commande. |
| Annulation d'une commande et de son groupe de processus | `TestCommandCancellationKillsProcessGroup` : le descendant ne crée pas son fichier témoin après interruption. |
| Calcul partagé conservé pour un consommateur encore actif | `TestConsumerCancellationPreservesSharedMeasurement`, tests du store et de `scanmemo` ; chargement annulé non publié comme complet. |
| Fermeture qui rejoint le travail appartenant au scan | `TestEngineCancellationJoinsSharedSizeWorkers`, régressions des pools et de la TUI ; watcher FSEvents rejoint avant la libération C. |
| Découverte annulée ne prouve pas l'absence d'usage | Tests Android/AI/sysx : attribution incomplète, résultat inconnu, conservation des binaires lorsque l'inspection échoue. |
| Inspections doctor et confirmation TUI | `TestDoctorSharesControllerWithProcessChecksAndCollect`, `TestDoctorCancelsProcessInspection`, `TestDoctorNoScanStopsAfterCanceledInspection`, `TestDoctorReportsIncompleteProcessInspection` ; `TestPickerCloseJoinsConfirmationInspectionWithoutCmd` et `TestPickerAbandonedConfirmationCannotReplaceFreshInspection` : contexte partagé, arrêt sans rapport trompeur, erreur visible, tâche rejointe même si Bubble Tea abandonne sa commande, résultat obsolète rejeté. |
| Marqueurs répétés dans les callbacks soumis au quota | `TestCheckoutProbeWaitsForIOAndStopsOnCancellation`, `TestSizeCheckoutMarkersWaitForIOAndCancel`. |
| Préparation initiale des racines sous contrôle, affichage de config sans activation | `TestScanSetupControlsInitialRootReadsAndPureSetupDoesNotActivate`. |
| Priorité et parallélisme restaurés, arrière-plan hérité conservé, échec OS non bloquant | `TestPolicyLifecycleInSubprocess` : sous-processus isolés. |

Les tests existants de sécurité restent verts : exclusions, TCC simulé,
symlinks, volumes, nested repositories et fichiers suivis par Git, processus
actifs, revalidation, hardlinks et clones/reclaim APFS. La comparaison des
résultats réels `eco`/`fast` sur fixtures est présentée dans les mesures.

L'intégration CLI teste explicitement `scan` et son dry-run, le dashboard,
les commandes spécialisées, `clean --yes --smart --dry-run`, `analyze --json`,
le picker/analyzer TUI et `doctor --no-scan`. Les sous-cas de
`TestScanPrioritiesOnlyOnScanPaths` et `TestScanModePrecedenceAndContext`
vérifient l'activation/restauration unique, le contexte partagé, la priorité
flag/config/défaut et des sorties JSON parsables sans diagnostics parasites.
Les tests TUI exercent la fermeture, un scan/nettoyage dry-run simultané,
le rescan, la jonction de l'ancien pool et le rejet de ses résultats.
Les tâches Bubble Tea abandonnées avant exécution de leur `Cmd` sont aussi
rejointes par la fermeture.

Essai complémentaire du binaire de la campagne dans un vrai pseudo-terminal :
analyzer eco puis fast, fixture isolée de 20 dossiers et 20 fichiers,
navigation dans un dossier/retour, rescan, confirmation dry-run et sorties
avec `q` / Ctrl-C clavier. Le dry-run dans le HOME fictif indique
« 1 would be deleted » ; les 20 fichiers et leur contenu sont conservés.
Une première tentative hors du HOME fictif est correctement refusée par le
guard. Les sessions sortent avec code 0, curseur réaffiché et écran alternatif
fermé. Le Ctrl-C clavier Bubble Tea quitte normalement ; les codes de signaux
130/143 sont vérifiés séparément par les tests de processus et le benchmark.

Tests ignorés : sept `TestRealMachine` nécessitent `LU_REAL=1` et scanneraient
la vraie machine ; le walk réel nécessite `LU_BENCH_PATH`, le harness TUI
`LU_TUI_HARNESS`, et deux tests de volumes nécessitent explicitement
`LU_TEST_MOUNT=1` / `LU_TEST_HDIUTIL=1`. Le test TCC réel sans FDA ne s'applique
pas car ce processus dispose de Full Disk Access. Deux tests de fallback de
compilation ne s'appliquent pas à ce build natif avec FSEvents. Les deux
entrées de helpers de sous-processus sont ignorées dans le processus parent
et utilisées par leurs tests appelants. Aucun échec n'est masqué.

## Mesures du binaire de la campagne

Machine : Mac14,9, macOS 26.5.1 arm64, 10 cœurs physiques/logiques,
16 Gio de RAM, Go 1.25.6. Le parent du benchmark hérite de nice 0 et n'est
pas en arrière-plan. Aucun test ou build du projet ne tourne pendant les
mesures. Les autres activités éventuelles de la machine ne sont pas arrêtées.

La fixture contient douze racines indépendantes, chacune avec un projet
`node_modules` de 32 packages, une branche de profondeur 32 et un hardlink.
Une racine contient en plus 2 048 petites entrées et un clone APFS créé avec
succès. Les parcours froids recensent 3 609 fichiers et 793 dossiers.
Les résultats finaux de toutes les répétitions, de la base, de fast et d'eco,
sont identiques après exclusion des champs variables de temps/version/âge :
mêmes items, tailles, reclaim, risques, sélections, totaux et erreurs.

Médianes de cinq répétitions ; « froid » signifie cache de tailles de
lu-cleaner vide. Le cache de pages du système n'est pas purgé. Chaque mesure
chaude réutilise le cache de tailles de la mesure froide précédente.

| Version / cache | Durée réelle (s) | CPU utilisateur (s) | CPU système (s) | Pic RSS (Mio) |
| --- | ---: | ---: | ---: | ---: |
| Base `08e5ed4`, froid | 0,178 | 0,072 | 0,178 | 21,86 |
| Base `08e5ed4`, chaud | 0,192 | 0,075 | 0,086 | 21,27 |
| fast, froid | 0,111 | 0,044 | 0,154 | 30,17 |
| fast, chaud | 0,142 | 0,031 | 0,061 | 21,25 |
| eco, froid | 13,498 | 0,192 | 0,422 | 25,75 |
| eco, chaud | 3,818 | 0,099 | 0,219 | 19,67 |

Les maxima admis d'E/S sont 8 en fast et 2 en eco, sans dépassement dans les
répétitions. En chaud, aucun nouveau parcours de tailles n'est nécessaire
(`files=0 dirs=0`) ; la découverte et la validation restent actives. Cette
fixture ne contient pas de commande Git : son maximum de commandes est 0.
Le plafond des commandes est donc prouvé par les tests avec runners et
processus réels, pas par cette mesure.

Les compteurs disque de `proc_pid_rusage`, échantillonnés toutes les 5 ms,
sont des bornes inférieures du processus principal, sans ses descendants.
Médianes lecture/écriture : 0/0 octet pour la base et fast, 8 192/0 pour eco
froid et 1 196 032/8 192 pour eco chaud. Les compteurs `ru_inblock/outblock`
sont nuls. Ils ne permettent pas de déduire le nombre total de lectures de
métadonnées ou l'activité physique complète du volume ; toutes les valeurs
brutes sont conservées dans le JSON.

Le ralentissement du scan eco est assumé et particulièrement visible avec
ces très petits fichiers : admissions de marqueurs et repos de 5 ms dominent
le temps utile. Le temps CPU total augmente avec les timers et les admissions.
Le but mesuré est la cohabitation et la réduction de pression instantanée,
pas un gain de vitesse ni une diminution du temps CPU total.
La moyenne CPU durant chaque scan froid, calculée comme
`(CPU utilisateur + CPU système) / durée réelle`, a une médiane de
125,7 % d'un cœur pour la base, 177,8 % pour fast et 4,55 % pour eco.
Il s'agit d'une moyenne sur ce scan, sans mesure de pic ni plafond CPU garanti.

Annulation par SIGINT après observation d'un premier parcours terminé,
pendant que d'autres racines restent à mesurer, cache désactivé :

| Version | Signal → sortie du processus | Code |
| --- | ---: | ---: |
| Base `08e5ed4` | 6,00 ms | 130 |
| fast | 1,81 ms | 130 |
| eco | 2,72 ms | 130 |

La mesure comprend la fermeture du processus et rejoint les workers bien
en dessous de 100 ms sur cette fixture. Elle ne mesure pas une annulation
pendant un syscall volontairement bloqué. Les compteurs coopératifs internes
sont respectivement 129 µs (fast) et 224 µs (eco).

### Cohabitation

Le scan est répété avec `LU_NO_CACHE=1` pendant un travail témoin fixe.
Chaque essai est encadré par un témoin seul avant et après ; le ralentissement
est `temps_avec_scan / moyenne(temps_seul_avant, temps_seul_après) - 1`.
L'ordre base/fast/eco tourne entre les essais. Une valeur négative traduit
la variabilité de la machine et ne prouve pas une accélération du témoin.

Témoin CPU : deux processus calculent chacun 64 000 SHA-256 sur 256 Kio.
Témoin E/S : 4 096 lectures et écritures d'1 Mio, sur un fichier indépendant
de 32 Mio, avec `F_NOCACHE` et `fsync` toutes les quatre itérations puis en fin.
La durée du travail témoin est exclue de la préparation des fixtures.

| Témoin | Version | Ralentissement médian | Étendue des cinq essais |
| --- | --- | ---: | ---: |
| CPU | Base `08e5ed4` | +2,07 % | −1,48 à +6,67 % |
| CPU | fast | +2,03 % | +0,78 à +4,75 % |
| CPU | eco | −0,36 % | −2,31 à +0,78 % |
| E/S | Base `08e5ed4` | +17,34 % | −17,90 à +77,05 % |
| E/S | fast | +2,29 % | −23,10 à +95,74 % |
| E/S | eco | −17,59 % | −27,38 à +1,20 % |

Les cinq essais eco de chaque témoin restent sous l'objectif de 15 % de
ralentissement. La cohabitation est ainsi validée pour ce protocole, cette
fixture et cette machine. L'écart négatif E/S ne doit pas être décrit comme
un gain de vitesse : les témoins seuls varient encore de 1,99 à 3,70 s.
La priorité native, le quota et les repos réduisent la charge du scan ; ils
ne garantissent pas ce seuil pour tous les jobs, systèmes de fichiers ou
appels de synchronisation globale.

Données brutes et paramètres :

- [Campagne finale complète](benchmarks/2026-10-02-final.json), binaire
  `bin/lu-cleaner`, SHA-256
  `0874c0a62f5ab3ba81642c2e818290589bf4f239919f7babd973e3b25a344b62`.
  Ce hash identifie le binaire mesuré avant le complément doctor/TUI et
  le changement de version de release ; ces mesures n'ont pas été refaites
  après ces modifications. Les parcours artifacts et leur contrôleur sont
  inchangés ; les nouvelles garanties doctor/TUI sont vérifiées par tests.
- [Première campagne](benchmarks/2026-10-02-scan.json) : binaire intermédiaire,
  trois essais, témoin court et `F_FULLFSYNC`.
- [Campagne de cohabitation intermédiaire](benchmarks/2026-10-02-cohabitation.json) :
  cinq essais longs avec `F_FULLFSYNC`, sans `F_NOCACHE` ; témoin CPU eco entre
  −4,81 et +3,43 %. Les témoins E/S seuls varient de 3,79 à 21,45 s, avec des
  essais eco à +65,13 % et +32,98 % malgré une médiane de −7,94 %. Cette
  campagne ne valide pas le seuil E/S et reste publiée comme limite observée.

Le protocole final mesure des E/S sans cache du descripteur avec `fsync`,
plutôt qu'une demande de vidage matériel global `F_FULLFSYNC`, pour réduire
le bruit et isoler un travail E/S reproductible. Les essais antérieurs ne sont
pas effacés. Deux tentatives interrompues avant sauvegarde complète ne servent
pas aux tableaux. Le script sauvegarde désormais chaque mesure terminée et
signale `complete=false` tant que la campagne n'est pas finie. Un SIGINT envoyé
par le harness avant l'installation du handler Go est distingué d'un crash ;
la campagne finale n'en contient aucun.

## Reproduction

Construire la base sans modifier le checkout courant :

```bash
base_dir=$(mktemp -d /tmp/lu-cleaner-base.XXXXXX)
git archive 08e5ed4 | tar -x -C "$base_dir"
(cd "$base_dir" && go build -o /tmp/lu-cleaner-baseline ./cmd/lu-cleaner)
go build -o /tmp/lu-cleaner-current ./cmd/lu-cleaner
python3 scripts/bench_scan.py --baseline /tmp/lu-cleaner-baseline \
  --current /tmp/lu-cleaner-current --samples 5 --packages 32 \
  --cpu-iterations 64000 --io-iterations 4096 --io-sync fsync --io-no-cache \
  --output /tmp/lu-cleaner-scan-benchmark.json
```

Le script crée puis supprime son propre dossier temporaire, son HOME/config
isolés, douze racines et son fichier témoin. Il ne scanne pas le vrai HOME.
La création des données est exclue des mesures. Il enlève GOMAXPROCS,
GOFLAGS et les overrides de scan/cache de l'environnement des binaires.
Les paramètres employés pour compiler les tests ne plafonnent donc pas les
profils applicatifs mesurés.
Pour explorer le cas de synchronisation matérielle, remplacer
`--io-sync fsync --io-no-cache` par `--io-sync fullfsync`, éventuellement
avec `--witness-only --witness-kind io`. Les résultats de protocoles différents
doivent rester séparés.

Diagnostics utilisateur :

```bash
./bin/lu-cleaner config show --json
LU_WALKERS=1 ./bin/lu-cleaner config show --scan-mode eco --json
LU_TRACE=1 ./bin/lu-cleaner artifacts /chemin/fixture --scan-mode eco \
  --dry-run --verbose --json >scan.json 2>scan.log
./bin/lu-cleaner scan --scan-mode fast --dry-run --verbose
```

Les durées suffixées `_cumulative` sont additionnées entre workers et peuvent
dépasser la durée réelle. `files`/`dirs` concernent les parcours de tailles ;
`cache_hits`/`cache_misses` concernent la mémoïsation de l'invocation.
`LU_TRACE=1` complète ces compteurs avec ceux du cache persistant.

## Limites

Les quotas portent sur les opérations admises, pas sur un pourcentage CPU
garanti. Les commandes directement lancées sont bornées ; leurs descendants
ne constituent pas une autre limite globale. Un appel noyau déjà bloqué peut
retarder la fin de l'annulation coopérative. Les codes 130/143 et la sortie
immédiate au second signal restent inchangés.

Un premier scan peut être sensiblement plus lent en eco. La baisse de la
pression instantanée n'implique pas une baisse du temps CPU total, notamment
avec le coût des timers et des admissions. Le cache n'évite que les mesures
complètes dont la validation est utilisable ; la découverte et les contrôles
de processus restent nécessaires.

La sauvegarde du cache, bornée à 64 Mio, peut encore s'effectuer après
annulation ; un appel noyau de sauvegarde déjà bloqué reste soumis à la même
limite technique d'interruption.

Les petits marqueurs/existence sont des sondes bornées inventoriées ; cette
version ne borne pas la taille totale de chaque entrée JSON/plist. Les
lectures sont découpées, mais les résultats complets et leurs allocations
restent compatibles avec le comportement antérieur.

Le gel complet et le hard reboot signalés initialement n'ont pas été
reproduits. Ces validations sur fixtures ne démontrent pas leur cause exacte
ni une garantie sur tous les volumes et toutes les machines.

## État du diff local avant publication

Au terme de la validation locale, le checkout était sur `main`, HEAD
`08e5ed43c901c4c5b3da089f4a9610107ad85156` : 126 fichiers suivis modifiés et
41 nouveaux fichiers non suivis, rien dans l'index. Le checkout initial étant
propre, ce diff contenait uniquement le travail de cette implémentation.
Les ajouts comprennent le contrôleur, les helpers de lectures/inspections,
les groupes de tâches TUI, leurs régressions, ce rapport, l'inventaire et
le script/données de benchmark. Les modifications suivies couvrent les points
d'intégration CLI/configuration/fsx/providers/sysx/TUI, les protections de
nettoyage utilisant le contexte, FSEvents et la documentation EN/FR.

`gofmt -l` est vide sur les fichiers Go modifiés/nouveaux ;
`git diff --check` réussit. Aucun cache Python n'est ajouté. Les sorties de
build `bin/` et `site/dist/` sont ignorées par Git. Les fixtures des benchmarks
et de l'essai TUI ont été supprimées après leurs vérifications.
Les binaires temporaires de comparaison et le cache de compilation ne font
pas partie du diff. Aucun commit, push, PR ou release n'avait encore été créé.

Pour relire :

```bash
git status --short
git diff --stat
git diff
git ls-files --others --exclude-standard
```

`git diff` présente les fichiers déjà suivis ; les fichiers nouveaux listés
par la dernière commande doivent aussi être ouverts pour une revue complète.
