# Régulation eco : correction du coût par petite opération

Rapport historique des essais avant intégration. Le réglage finalement retenu
et sa validation sont dans [SCAN_ECO_FINAL.md](SCAN_ECO_FINAL.md).

Validation locale du 2 octobre 2026, après la release v0.3.0 (`6670034`).

Ce rapport conserve un réglage intermédiaire. Le profil actuel supprime les
pauses artificielles tout en gardant les quotas et la priorité arrière-plan ;
voir [la validation suivante](SCAN_LATENCY_OPTIMIZATION.md).

La [comparaison ultérieure avec la CLI d'origine](SCAN_BEFORE_AFTER.md)
confirme un ralentissement restant : sur une fixture plus grande et une
machine occupée, l'eco corrigé reste 6,6 fois plus lent en médiane que la
base antérieure aux protections. Le gain ci-dessous face à v0.3.0 ne suffit
donc pas à considérer la lenteur du scan général comme résolue.

## Défaut et correction

La v0.3.0 applique 5 ms de repos après chaque opération admise, même une
ouverture, un lstat ou la lecture vide signalant la fin d'un dossier. Un scan
réel a dépassé 39 minutes pour 2 min 21 s de CPU cumulé. Sur une fixture de
1 024 fichiers dans 256 dossiers, le binaire v0.3.0 prenait 3,54 s en eco
contre 0,018 s en fast. La baisse de charge était obtenue au prix d'une
pénalité de latence excessive.

Le créneau conserve maintenant son budget entre les appelants : après 2 ms
cumulées pendant lesquelles il est détenu, il se repose 5 ms. Le contrôle
se fait à la fin d'une opération bornée. Les attentes de quota et les repos
ne consomment pas le budget ; un créneau resté inactif au moins 5 ms repart
sans dette de travail. Le créneau reste indisponible pendant son repos.

Les plafonds restent de 2 E/S, 1 commande et 1 prélecture en eco. La priorité
macOS d'arrière-plan, GOMAXPROCS plafonné à 2, les lots de 256, les contrôles
de sécurité et le mode fast sont conservés. Le temps compté est une durée
de détention du permis, pas une mesure du temps CPU. Un appel noyau déjà
bloqué reste non interruptible immédiatement.

`config show --json` expose `scan.work_window_ms = 2` et `scan.pause_ms = 5`.
Le mode verbose présente aussi la fenêtre de travail effective.

## Mesures reproductibles

Mac14,9, 10 cœurs, 16 Gio, macOS 26.5.1 arm64, Go 1.25.6.
Pas de tests ni de compilation pendant la campagne. Les activités externes
de la machine ne sont pas contrôlées. Trois répétitions, douze racines,
arbres profonds, dossier large, hardlinks et clone APFS. Création des fixtures
hors mesure. « Froid » signifie cache de tailles vide, sans purge du cache
de pages du système.

Dans le [JSON brut](benchmarks/2026-10-02-pacing.json), `baseline` désigne
**v0.3.0 en eco**, et non la base antérieure à l'introduction des profils.
Les chemins, hashes des binaires et paramètres exacts y sont consignés.

| Profil | Cache | Durée médiane | CPU user + système médian | RSS maximale médiane |
| --- | --- | ---: | ---: | ---: |
| v0.3.0 eco | froid | 13,038 s | 0,528 s | 25,03 Mio |
| eco corrigé | froid | 0,455 s | 0,258 s | 27,19 Mio |
| fast | froid | 0,105 s | 0,225 s | 28,67 Mio |
| v0.3.0 eco | réutilisé | 4,144 s | 0,290 s | 19,31 Mio |
| eco corrigé | réutilisé | 0,493 s | 0,213 s | 19,67 Mio |
| fast | réutilisé | 0,251 s | 0,082 s | 20,89 Mio |

Le gain médian eco est de 28,6× à froid et 8,4× avec cache réutilisé.
Les résultats normalisés de toutes les répétitions sont identiques.
Le maximum d'E/S admises reste 2 en eco et 8 en fast. L'annulation eco
après le premier parcours terminé, avec d'autres parcours actifs, rejoint
le processus en 3,32 ms et renvoie 130 (fin coopérative interne : 1,55 ms).
Cela ne garantit pas un délai maximal pour toutes les situations : un
essai court distinct d'annulation au démarrage a pris environ 208 ms de
bout en bout, dont 4,85 ms de fermeture coopérative interne.

### Cohabitation : résultat partiel, limite disque non validée

Chaque durée du témoin en concurrence est comparée à la moyenne de ses
exécutions seules juste avant et après. Trois essais par mode. Témoin CPU :
deux processus, chacun 64 000 SHA-256 sur 256 Kio. Témoin disque : fichier
de 32 Mio, 4 096 lectures/écritures de 1 Mio, F_NOCACHE et fsync.

| Profil | Variations du témoin CPU | Variations du témoin disque |
| --- | --- | --- |
| v0.3.0 eco | −4,9 %, +24,8 %, +3,5 % | −1,7 %, +45,7 %, +32,9 % |
| eco corrigé | −0,3 %, +0,2 %, +11,5 % | +13,2 %, −5,9 %, +49,3 % |
| fast | +0,3 %, +5,1 %, +24,8 % | −12,6 %, −44,4 %, −11,6 % |

Le seuil de 15 % est respecté pour les trois essais CPU du correctif.
**Il n'est pas validé pour les écritures disque.** Les témoins disque seuls
varient de 2,21 à 17,34 s, et l'ancien eco présente aussi des dépassements.
Ces données ne permettent ni d'attribuer tout l'écart au correctif, ni de
garantir qu'il est sans effet sur un autre job d'écriture. Les valeurs
négatives ne prouvent pas une accélération du témoin. Les quotas sont
prouvés ; la garantie générale de cohabitation disque reste à établir dans
un environnement plus stable. Cette correction locale ne constitue pas
une nouvelle validation de release.

Reproduction (conserver d'abord une copie du binaire v0.3.0) :

```bash
make build
python3 scripts/bench_scan.py \
  --baseline /chemin/vers/lu-cleaner-v0.3.0 \
  --current ./bin/lu-cleaner --output /tmp/lu-pacing.json \
  --samples 3 --packages 32 --cpu-iterations 64000 \
  --io-iterations 4096 --io-sync fsync --io-no-cache
```

## Régressions et vérifications

- Horloge injectable : plusieurs petites opérations partagent une fenêtre,
  y compris entre goroutines ; chaque créneau garde son propre budget ;
  l'inactivité ne crée pas de dette ; une panique à la limite de la fenêtre
  libère le créneau après son repos.
- Les régressions existantes vérifient annulation des attentes/repos,
  admission de douze racines avec un budget de 1, réentrance, cache partagé,
  fermeture TUI, inspections interrompues et timeout des commandes après
  admission.
- `go test -p 2 -count=1 -json ./...` : 762 tests/sous-tests réussis,
  16 entrées ignorées, 23 packages validés.
- Race detector : scanctl, fsx, engine, cli, tui et artifacts réussis.
- `go vet ./...`, `make build`, `make nocgo`, `make universal`, `make docs`
  et build du site réussis. Les builds/tests utilisent GOMAXPROCS 2 ou 4 ;
  ces overrides sont retirés de l'environnement des binaires mesurés.
- Le binaire local `bin/lu-cleaner` est recompilé avec le correctif.

Les fichiers de mesure complets du scan réel, qui peuvent contenir des
chemins personnels, restent dans le dossier temporaire local ; seules les
mesures sur fixtures artificielles sont ajoutées au dépôt.

## Essai du scan général réel, borné à deux minutes

Commande : `./bin/lu-cleaner scan --dry-run --verbose --json --scan-mode eco`,
avec la configuration habituelle et les caches existants. Aucun autre test
ou build du projet ne tournait pendant cet essai.

- Après 120 s : 793 892 fichiers et 80 955 dossiers parcourus pour les tailles.
- CPU user + système : 46,76 s ; RSS maximale : 80,73 Mio.
- Maximum observé : 2 E/S, 1 commande. Temps de repos cumulé : 114,98 s ;
  attente cumulée d'E/S : 5 h 10 min, répartie entre les nombreux appelants.
  Ces durées cumulées ne sont pas des durées murales.
- Le provider Apple avait terminé après 83,45 s ; le scan général restait
  actif à la limite de deux minutes. Sa durée totale n'est donc pas connue.
- SIGINT envoyé par le protocole d'essai : sortie 130 en 38,54 ms,
  fermeture coopérative interne de 18,30 ms, sans arrêt forcé.

L'essai prouve la progression et la fermeture du scan sur cette machine,
pas l'exhaustivité d'un scan interrompu ni une comparaison à périmètre/cache
identiques avec les 39 minutes observées auparavant.
