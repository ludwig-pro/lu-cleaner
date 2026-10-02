# Eco : réglage retenu après les essais de latence

Validation locale du 2 octobre 2026, à partir de v0.3.0 (`6670034`).

## Comportement livré

Eco conserve **2 opérations d'E/S simultanées et supprime les pauses
artificielles**, tout en gardant la priorité macOS d'arrière-plan,
`GOMAXPROCS <= 2`, une commande externe simultanée, une prélecture et les lots
de 256 entrées. `LU_WALKERS` reste l'override du budget d'E/S partagé. Fast
reste à 8 E/S et 4 commandes, avec les priorités héritées.

La v0.3.0 imposait 5 ms après chaque opération, y compris un simple `lstat`
ou la lecture vide de fin de dossier. Les essais intermédiaires ont regroupé
ces pauses, puis démontré l'intérêt de les supprimer : une durée de détention
du permis inclut les attentes noyau et ne mesure pas la charge CPU. Les
plafonds logiciels et la priorité macOS sont conservés pour limiter l'impact
sur les autres applications.

Le réglage expérimental `work_window` et son comptage par créneau ne sont pas
livrés. Le contrôleur conserve son mécanisme de pause existant, testé
indépendamment, mais aucun profil public ne l'active. Le format de
`config show --json` reste celui de la v0.3.0 ; `scan.io` vaut 2 et
`scan.pause_ms` vaut 0 en eco.

Les règles de découverte, de taille, de sélection et de nettoyage restent
inchangées. Les JSON de scan/nettoyage/analyse ne changent pas. Avec `LU_TRACE=1`,
stderr affiche dès sa fin la validation du cache et distingue les échecs natifs
de FSEvents : démarrage du flux, timeout, capacité/allocation du tampon.

## Effet mesuré de la suppression des pauses

Sur le même dossier réel `node_modules`, sans cache de tailles : 94 379
fichiers, 12 998 dossiers. Deux répétitions de chaque variante, ordre inversé
au second passage ; même priorité d'arrière-plan, deux E/S et deux processeurs
Go. Le cache de pages macOS n'est pas purgé.

| Réglage | Durées | CPU cumulé médian |
| --- | --- | ---: |
| Ancien eco local, fenêtre 2 ms / pause 5 ms | 15,170 s ; 13,075 s | 4,502 s |
| Eco sans pauses artificielles | 4,524 s ; 4,291 s | 2,951 s |

La durée médiane est divisée par **3,20** et le CPU cumulé baisse de **34,4 %**.
Les résultats sont identiques, y compris les octets récupérables,
hardlinks/clones, nombres de fichiers/dossiers, dernière modification et
erreurs. Ce test isole le coût des pauses ; ce n'est pas le scan général.
Les données et les tests antérieurs sont dans
[SCAN_LATENCY_OPTIMIZATION.md](SCAN_LATENCY_OPTIMIZATION.md).

### Comparaison finale avec la release

Le binaire final à deux E/S sans pauses est comparé à la v0.3.0 et à la base
`08e5ed4`, antérieure aux profils. Trois répétitions par profil et état de
cache, ordre tournant, 12 racines immuables, 5 157 fichiers et 1 189 dossiers,
avec arbre profond, dossier large, hardlinks et clone APFS. La création des
données est exclue de la mesure. Le cache de pages macOS n'est pas purgé.

| Version / profil | Cache vide, médiane | CPU cumulé médian à vide | RSS médiane à vide | Cache réutilisé, médiane |
| --- | ---: | ---: | ---: | ---: |
| Avant les protections | 0,399 s | 0,279 s | 22,31 Mio | 0,240 s |
| v0.3.0 eco | 21,279 s | 0,947 s | 25,44 Mio | 5,431 s |
| **Eco livré, 2 E/S sans pauses** | **0,416 s** | **0,339 s** | 28,14 Mio | **0,598 s** |
| Fast | 0,156 s | 0,257 s | 32,09 Mio | 0,315 s |

Les **24 rapports normalisés sont identiques** : chemins, tailles, risques,
décisions, totaux et erreurs conservés ; seul l'âge variable est retiré.
Tous les processus terminent avec le code 0, sans timeout ni arrêt forcé.
À froid, les trois essais eco finaux vont de 0,378 à 0,478 s, contre 20,961
à 22,249 s pour la release. Les diagnostics de cache, durées individuelles,
compteurs disque et hashes des binaires sont conservés dans
[les résultats bruts](benchmarks/2026-10-02-eco-final-versions.json).
Ce gain sur une petite fixture ne s'extrapole pas au vrai HOME ; les scans
complets mesurés ci-dessous prennent encore plusieurs minutes.

```sh
python3 -B scripts/compare_scan_versions.py \
  --before /chemin/vers/lu-cleaner-08e5ed4 \
  --released /chemin/vers/lu-cleaner-v0.3.0 --current ./bin/lu-cleaner \
  --samples 3 --packages 64 --timeout 120 --output /tmp/lu-versions.json
```

## Scans complets réels et choix du parallélisme

Trois scans complets du vrai HOME, tous les providers et racines habituelles,
cache de tailles désactivé, même binaire et ordre 2/4/2. Le cache de pages
macOS n'est pas purgé et les autres applications restent actives.

| Budget d'E/S | Durée | CPU cumulé wait4 | RSS maximale |
| --- | ---: | ---: | ---: |
| 2, premier passage | 195,697 s | 120,500 s | 91,33 Mio |
| 4, option évaluée | 130,341 s | 117,776 s | 100,59 Mio |
| 2, contrôle après | 221,480 s | 121,905 s | 90,53 Mio |

Les premiers passages parcourent chacun 2 932 713 fichiers et 345 235 dossiers ;
le dernier en parcourt six et dix de plus. Tous les providers terminent sans
erreur. Quatre E/S réduisent ici la durée de 33 à 41 %, sans hausse du CPU
cumulé. À quatre E/S, le CPU parent moyen est 79,8 % d'un cœur et son pic
échantillonné 124,7 % d'un cœur. Ces pourcentages ne représentent pas les dix
cœurs du Mac. Le CPU wait4 peut inclure les descendants attendus ; le CPU
échantillonné et la RSS concernent le parent.

Machine : Mac14,9, 10 cœurs logiques, 16 Gio, macOS 26.5.1 arm64, Go 1.25.6.
[Données et protocole détaillés](benchmarks/2026-10-02-io-workers-real.json).
Ces scans mesurent une amélioration sur cette machine, pas une garantie de
durée ou de pourcentage CPU sur d'autres machines.

### Pourquoi le défaut reste à deux E/S

Deux campagnes supplémentaires comparent séparément les candidats à quatre
puis trois E/S au binaire local à deux E/S, tous sans pauses. Chaque campagne
comprend 18 scans sur fixtures immuables, puis trois répétitions de tâches
témoins CPU et disque, chacune encadrée par deux contrôles du témoin seul.
Les profils tournent entre répétitions. Aucun build ni autre test lu-cleaner
ne tourne pendant les mesures ; les autres applications restent actives.

Le témoin CPU utilise deux workers SHA-256 de 64 000 itérations. Le témoin
disque utilise un fichier borné à 32 Mio, 1 024 lectures/écritures de 1 Mio,
`F_NOCACHE` et `F_FULLFSYNC` toutes les quatre itérations. Le ralentissement
compare la durée concurrente à la moyenne des contrôles avant/après.

| Campagne / profil | Ralentissement médian CPU | Ralentissement médian disque |
| --- | ---: | ---: |
| Contrôle 2 E/S de la campagne 4 | −9,5 % | +0,9 % |
| Candidat 4 E/S | −4,3 % | **+53,0 %** |
| Fast, campagne 4 | +18,1 % | +4,1 % |
| Contrôle 2 E/S de la campagne 3 | −5,5 % | +0,0 % |
| Candidat 3 E/S | −16,5 % | **+20,0 %** |
| Fast, campagne 3 | +8,2 % | −9,4 % |

**Les candidats trois et quatre E/S ne sont pas retenus par défaut** : leur
ralentissement disque médian dépasse l'objectif de 15 %. Le défaut à deux E/S
reste sous cet objectif en médiane dans les deux campagnes. La dispersion
reste forte : les contrôles à deux E/S de la campagne 4 vont de −15,2 % à
+36,8 %, ceux de la campagne 3 de −31,0 % à +8,4 %. Les valeurs négatives
reflètent le bruit de mesure, pas une accélération due au scan. Il serait
incorrect de promettre une borne générale de 15 % ou d'attribuer tous ces
écarts au seul nombre de créneaux. Tous les échantillons sont conservés.

Les 18 résultats normalisés de chaque campagne sont identiques. Les bornes
d'admission sont respectées ; les annulations après le premier parcours
terminé rejoignent les processus en moins de 15 ms, avec le code 130, sans
arrêt forcé. Sur les petites fixtures, quatre E/S ne sont d'ailleurs pas
toujours plus rapides : 0,421 s contre 0,282 s à deux E/S en médiane à froid.
La campagne à trois donne 0,336 s contre 0,520 s. Ces micro-mesures ne
permettent pas de prédire la durée du scan complet.

Données : [candidat quatre E/S](benchmarks/2026-10-02-eco-four-final.json),
[candidat trois E/S](benchmarks/2026-10-02-eco-three-final.json).
`baseline` y désigne le précédent binaire local à deux E/S sans pause,
**pas la release v0.3.0**. Le réglage testé à quatre reste disponible
explicitement avec `LU_WALKERS=4`.

## Limite restante : validation du cache

Un scan réel avec le cache habituel a terminé en 245,025 s après un timeout
de relecture FSEvents : 184 968 événements reçus sans fin de relecture dans
le budget de 8 s. Les 5 736 tailles chargées sont restées invalides et les
arbres ont été recalculés. Un autre passage avec cache validé avait terminé
en 73,198 s. Ces deux situations ne sont pas une comparaison du seul code.

Cette modification réduit le coût des parcours ; **elle ne corrige pas ce
timeout du cache** et n'assouplit aucune condition de validité. L'annulation
reste coopérative : un appel noyau déjà bloqué ne peut pas être interrompu
instantanément. Les commandes externes peuvent aussi créer des descendants ;
leur nombre total n'est pas borné par le quota des commandes.

## Reproduction

```sh
make build
./bin/lu-cleaner config show --json
LU_TRACE=1 ./bin/lu-cleaner scan --dry-run --verbose
```

Pour tester plus de lectures en parallèle en gardant eco et sa priorité
d'arrière-plan, en acceptant un impact potentiellement supérieur sur les
autres jobs disque :

```sh
LU_WALKERS=4 ./bin/lu-cleaner scan --dry-run --verbose
```

Pour reproduire une campagne sur fixtures après avoir conservé les binaires
à comparer (la création des données est exclue des chronométrages) :

```sh
python3 scripts/bench_scan.py \
  --baseline /chemin/vers/binaire-reference --current ./bin/lu-cleaner \
  --output /tmp/lu-cohabitation.json --samples 3 --packages 64 \
  --cpu-iterations 64000 --io-iterations 1024 --io-sync fullfsync --io-no-cache
```

Les rapports `SCAN_PACING_FIX`, `SCAN_BEFORE_AFTER`, `SCAN_REAL_CURRENT` et
`SCAN_LATENCY_OPTIMIZATION` conservent les essais intermédiaires et leurs
binaires, y compris les pistes non retenues. Leurs paramètres ne décrivent
pas nécessairement le défaut livré ici.

## Vérifications finales du code livré

- `go test -count=1 -json ./...` : **761 tests/sous-tests réussis**, 23 packages
  validés, 16 tests ignorés et 2 packages sans tests. Les tests ignorés sont
  les helpers de sous-processus, les variantes incompatibles avec ce build
  cgo/macOS, les montages de volumes opt-in et les tests manuels du vrai HOME
  ou de la TUI. Les scans réels décrits plus haut sont exécutés séparément.
- `go test -race -count=1` sur `scanctl`, `fsx`, `fsevents`, `engine`, `cli`,
  `tui`, `providers/artifacts` et `sysx` : **8 packages réussis**, aucune course
  détectée. Le linker macOS émet son avertissement `LC_DYSYMTAB` avec le race
  detector ; les binaires de test sont construits et exécutés avec succès.
- `go vet ./...`, `make build`, `make nocgo`, `make universal`, `make docs`
  et `npm run build` dans `site` réussis ; **81 pages** générées.
- Formatage Go, `git diff --check`, lecture des JSON de mesures et validation
  syntaxique/aide du script Python réussis.

Le premier passage de la suite a échoué dans le test d'admission des commandes
système : démarrer `/bin/echo` dans un délai réel de 100 ms dépendait de la
charge de la machine. Les deux tests de timeout après admission utilisent
maintenant `testing/synctest` et une barrière : le temps virtuel dépasse le
budget pendant l'attente, puis la vraie commande s'exécute. Vingt-cinq
répétitions passent. Une mutation temporaire par overlay Go plaçant le timeout
avant l'admission fait échouer les deux tests avec `context deadline exceeded` ;
elle n'est pas intégrée au code. La suite entière a ensuite été relancée avec
succès, sans augmenter les délais applicatifs ni changer le runner.

Les builds/tests utilisent `GOMAXPROCS=2`, `GOFLAGS=-p=2`, `CC=clang` et
`MACOSX_DEPLOYMENT_TARGET=12.0` pour borner la charge de compilation. Les
processus des benchmarks retirent ces overrides de parallélisme ainsi que
`LU_WALKERS` ; leurs environnements et hashes sont enregistrés dans les JSON.
Les logs complets des contrôles restent dans
`/private/tmp/lu-eco-final-validation/`.
