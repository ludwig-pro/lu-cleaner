# Comparaison avant / après les protections de scan

Rapport historique des essais avant intégration. Le réglage finalement retenu
et sa validation sont dans [SCAN_ECO_FINAL.md](SCAN_ECO_FINAL.md).

Mesures locales du 2 octobre 2026. **Le correctif améliore fortement la
v0.3.0, mais ne résout pas encore la lenteur observée en eco.** Sur cette
campagne, l'eco corrigé reste 6,6 fois plus lent en médiane que la CLI
antérieure aux protections. Une répétition atteint 15,19 s pour seulement
8 229 fichiers. Le mode fast reste dans le même ordre de grandeur que
l'ancienne CLI.

Un [scan réel complet effectué ensuite](SCAN_REAL_CURRENT.md) termine en
7 min 53 s et révèle zéro réutilisation du cache persistant après un échec
de validation FSEvents. Ce test complète les mesures sur fixtures ci-dessous.

## Versions et protocole

- Avant : `08e5ed43c901c4c5b3da089f4a9610107ad85156`, avant l'introduction
  des profils de ressources.
- Release : v0.3.0, `6670034ba0cf37fd2016f077dd192f4ceeecb953`, eco avec
  repos de 5 ms après chaque opération.
- Local : binaire `v0.3.0-dirty`, avec repos de 5 ms après 2 ms cumulées
  de détention du créneau, testé en eco et en fast.

Mac14,9, 10 cœurs, 16 Gio, macOS 26.5.1 arm64, binaires Go 1.25.6 avec cgo.
Les hashes exacts des trois binaires figurent dans le
[JSON brut](benchmarks/2026-10-02-before-after.json).

Une seule fixture immuable, partagée par toutes les versions : 12 racines,
8 229 fichiers, 1 957 dossiers, arbres de type `node_modules`, profondeur
32, dossier large de 2 048 fichiers, hardlinks et clone APFS. Les mesures
portent sur `artifacts <racines> --dry-run --json --min-size 0 --verbose`.
La création et le comptage des fichiers sont exclus des chronométrages.

Trois répétitions par profil et état de cache, soit 24 exécutions
séquentielles. L'ordre des profils tourne à chaque répétition. Chaque
profil/répétition commence avec un HOME, une configuration et un cache de
tailles isolés, puis réutilise ce cache immédiatement. Les overrides
GOMAXPROCS, GOFLAGS et LU_WALKERS sont retirés des processus mesurés.
Chaque exécution est bornée à 120 s avant SIGINT ; aucune n'a atteint
cette borne. Toutes ont terminé avec le code 0.

« Froid » signifie cache applicatif vide. **Le cache de pages du système
n'est pas purgé.** Aucune compilation ou autre campagne de test lu-cleaner
ne tourne en parallèle. D'autres applications et jobs étaient actifs ;
la charge système moyenne sur une minute varie de 6,69 à 30,79 pendant
les essais. Cette métrique n'est pas un pourcentage CPU. L'ordre alterné
réduit certains biais temporels, mais ne rend pas la charge externe
identique entre les profils.

## Durée et ressources

Cache applicatif vide, médianes de trois exécutions :

| Version / profil | Durée | Minimum – maximum | CPU cumulé user + système | CPU moyen | RSS maximale |
| --- | ---: | ---: | ---: | ---: | ---: |
| Avant les protections | 0,233 s | 0,205 – 0,249 s | 0,394 s | 181,9 % | 22,88 Mio |
| v0.3.0 eco | 28,456 s | 28,269 – 55,407 s | 1,481 s | 4,9 % | 26,33 Mio |
| Local eco corrigé | 1,529 s | 1,318 – 15,192 s | 0,520 s | 34,0 % | 27,75 Mio |
| Local fast | 0,262 s | 0,155 – 0,387 s | 0,323 s | 160,5 % | 32,34 Mio |

Le CPU moyen est calculé pour chaque exécution comme
`100 × (CPU user + système) / durée`, puis résumé par sa médiane.
100 % correspond à un cœur logique ; ce n'est ni un pic, ni un pourcentage
de la capacité totale de la machine. Chaque colonne est une médiane
indépendante. La RSS est le maximum du processus, puis sa médiane sur
les trois essais ; elle n'est pas la somme des mémoires des descendants.

Cache applicatif réutilisé :

| Version / profil | Durée médiane | Minimum – maximum |
| --- | ---: | ---: |
| Avant les protections | 0,359 s | 0,220 – 0,563 s |
| v0.3.0 eco | 20,402 s | 5,903 – 38,574 s |
| Local eco corrigé | 1,184 s | 0,723 – 1,974 s |
| Local fast | 0,206 s | 0,190 – 0,212 s |

Le correctif local est 18,6 fois plus rapide que la release eco en médiane
à froid. Il reste 6,6 fois plus lent que la CLI d'origine. Le profil eco
réduit le CPU moyen occupé pendant le scan, mais ne réduit pas le travail
CPU cumulé dans cette comparaison : 0,520 s contre 0,394 s auparavant.
Les plages de durée sont essentielles pour interpréter les médianes.

Les compteurs disque échantillonnés sont conservés dans le JSON. Ce sont
des bornes inférieures pour le processus, incluant son démarrage et son
cache, pas une mesure exhaustive des E/S disque de toute la machine.
Les lectures servies par le cache système n'y correspondent pas à des
lectures physiques. Ils ne permettent pas de conclure à une réduction
de l'activité disque sur le scan général.

## Résultats et diagnostics

- Les 24 rapports normalisés sont identiques : 12 éléments,
  33 603 584 octets, mêmes tailles, chemins, risques et décisions.
  Seul `age_days` est retiré des éléments ; les totaux et erreurs sont
  aussi comparés. Aucun résultat manquant sur cette fixture.
- E/S actives maximales observées : 2 en eco, 8 en fast. L'ancienne
  version ne fournit pas cette instrumentation. Aucun de ces scans
  d'artefacts ne lance de commande via le quota de commandes ; cette
  campagne ne constitue donc pas un test de ce quota.
- Tous les scans chauds du correctif obtiennent 12 hits du cache
  persistant, sans nouveau parcours de taille. La latence restante
  inclut donc aussi préparation, découverte, validation et fermeture.
- Une répétition chaude de v0.3.0 ne réutilise que 6 tailles : la
  validation FSEvents prend 8,871 s et 6 demandes repartent en parcours
  avant sa fin. Aucun changement n'a invalidé les tailles. Un cache
  présent n'assure donc pas qu'un scan évite de tout mesurer à nouveau.
- L'essai eco local à 15,192 s consomme seulement 0,849 s de CPU.
  Le provider prend 10,527 s, avec 8,478 s de repos cumulés entre les
  créneaux. Les attentes du système, la priorité d'arrière-plan et la
  régulation logicielle sont des pistes ; cette campagne ne les isole
  pas causalement. Les durées cumulées des quotas ne sont pas des
  durées murales et ne doivent pas être additionnées à celles du scan.

## Portée de la conclusion

Cette comparaison prouve un ralentissement restant sur des artefacts
contrôlés. **Elle ne mesure pas la durée totale de `scan --dry-run` sur
le vrai HOME et ses huit providers.** Le dernier processus lancé par
l'utilisateur n'était plus actif lors du début de la campagne ; son
temps total et sa cause de sortie n'ont pas été recueillis.

L'[essai réel antérieur borné à deux minutes](SCAN_PACING_FIX.md#essai-du-scan-général-réel-borné-à-deux-minutes)
avait parcouru 793 892 fichiers et 80 955 dossiers pour les tailles sans
terminer. Ce résultat ne doit pas être extrapolé à partir de la fixture.
La cause initiale du gel complet n'a pas été reproduite. Cette campagne
ne revalide pas non plus l'objectif de moins de 15 % de ralentissement
d'un autre job ; la variabilité disque documentée précédemment demeure.

La prochaine investigation doit mesurer séparément préparation,
découverte, tailles, validation FSEvents et fermeture sur un périmètre
réel borné. Il faut ensuite isoler l'effet de la priorité native et des
repos logiciels sous charge stable avant de changer à nouveau les
réglages. À ce stade, déclarer la lenteur du scan général résolue serait
prématuré.

## Reproduction

Conserver trois binaires distincts ; le premier doit dater d'avant les
profils, et le deuxième de la release v0.3.0 :

```bash
python3 -B scripts/compare_scan_versions.py \
  --before /chemin/vers/lu-cleaner-08e5ed4 \
  --released /chemin/vers/lu-cleaner-v0.3.0 \
  --current ./bin/lu-cleaner \
  --samples 3 --packages 128 --timeout 120 \
  --output /tmp/lu-scan-three-versions.json
```

Cette commande crée puis supprime uniquement ses propres fixtures
temporaires. Elle ne scanne pas le dossier utilisateur. Le JSON conserve
les mesures brutes, diagnostics de cache, ordre, charge système et hashes
des résultats normalisés.

Le correctif applicatif reste local et non publié. Cette campagne ajoute
le script de comparaison, ce rapport et les données brutes ; elle ne
modifie pas le comportement du binaire.
