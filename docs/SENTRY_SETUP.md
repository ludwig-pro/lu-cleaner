# Configuration Sentry de lu-cleaner

Finalisation du 3 octobre 2026 dans le dépôt lu-cleaner, sur `codex/docs-racing-light`, à partir de `2a732fc` (`v0.3.1`). Configuration distante appliquée ; ce compte rendu décrit les validations et le diff local précédant la livraison de `v0.4.0`.

## Projet officiel

| Paramètre | Valeur vérifiée |
| --- | --- |
| Organisation | `ludwig-developer`, identifiant `4510914016182272` |
| Projet | `lu-cleaner`, identifiant `4512192578715728` |
| Plateforme | Go |
| Région de stockage des événements | Union européenne, Allemagne |
| Forfait | Developer gratuit ; aucun essai Business ni paiement activé |
| Contact de confidentialité | `contact@ludwigvantours.dev` |
| Notice | `2026-10-03.2`, nouveau consentement requis pour une ancienne notice |
| Quota du forfait | 5 000 erreurs par mois, partagé par les projets de l'organisation |
| Conservation annoncée | 30 jours d'historique détaillé des événements ; métadonnées agrégées potentiellement plus anciennes |

[Projet](https://ludwig-developer.sentry.io/projects/lu-cleaner/?project=4512192578715728) · [Confidentialité](https://ludwig-developer.sentry.io/settings/projects/lu-cleaner/security-and-privacy/) · [Clés publiques DSN](https://ludwig-developer.sentry.io/settings/projects/lu-cleaner/keys/).

Le [forfait Developer](https://sentry.io/pricing/) fournit un historique de 30 jours, pas une garantie de purge de toutes les métadonnées serveur après ce délai. La région décrit le stockage des événements, pas celui de toutes les données du compte ou des services Sentry. Aucun contrat ou réglage global des autres projets n'a été modifié. Aucun nouveau token d'administration n'a été créé.

## Réglages appliqués

- Error Monitoring uniquement. Aucun SDK généraliste, Logs, Application Metrics, Session Replay ou transaction de performance activé par lu-cleaner.
- Scrubbers serveur et par défaut activés ; stockage des IP empêché.
- Champs sensibles supplémentaires : `email`, `username`, `hostname`, `server_name`, `ip_address`, `user`, `request`, `extra`, `breadcrumbs`, `argv`, `environ`, `environment_variables`, `absolute_path`, `local_variables`, `city`, `subdivision`, `region`, `country_code`.
- Règle avancée : `[Remove] [Anything] from [$user.** || $http.** || extra.** || $breadcrumb.** || contexts.** || server_name]`.
- Minidumps désactivés par l'héritage existant de l'organisation ; récupération de sources JavaScript et SCM désactivée ; vérification TLS des requêtes sortantes activée.
- Protection contre les pics active ; création automatique des releases active ; alertes de haute priorité conservées. Aucun accès, invitation ou autre projet modifié.

Les sélecteurs récursifs suivent la [syntaxe documentée par Sentry](https://docs.sentry.io/product/data-management-settings/scrubbing/advanced-datascrubbing/). Le simple masquage de l'IP ne suffit pas à supprimer la localisation que Sentry peut en déduire : son [correctif documenté](https://www.sentry.help/en/articles/13964201-can-i-disable-ip-geolocation-for-gdpr-compliance) et le test réel ont motivé le filtrage supplémentaire. Les changements serveur ne s'appliquent qu'aux prochains événements.

## Branchement des builds

Le DSN public officiel est :

```text
https://7722a6368f3b87b5a21c34ae2973c7ef@o4510914016182272.ingest.de.sentry.io/4512192578715728
```

C'est une clé publique d'ingestion, pas un secret d'administration. La variable Actions de dépôt `LU_DIAGNOSTICS_DSN` a été créée dans `ludwig-pro/lu-cleaner`, puis relue et comparée exactement à ce DSN. Le workflow Release transmet cette variable à GoReleaser. Le binaire local et le binaire universel contiennent ce destinataire après compilation. Aucun token d'administration n'est nécessaire ou intégré au build.

```sh
LU_DIAGNOSTICS_DSN='https://7722a6368f3b87b5a21c34ae2973c7ef@o4510914016182272.ingest.de.sentry.io/4512192578715728' make build
env -u LU_DIAGNOSTICS_DSN ./bin/lu-cleaner diagnostics status --json
```

Statut réel vérifié : `enabled: false`, `decision: undecided`, notice `2026-10-03.2`, destinataire `o4510914016182272.ingest.de.sentry.io`. Aucun consentement personnel modifié. Sans variable au build, le binaire reste sans destinataire. Une variable GitHub seule ne modifie pas une release déjà publiée : le code et le workflow devront être livrés dans une future release.

L'utilisateur peut lire la notice et choisir lui-même :

```sh
./bin/lu-cleaner diagnostics enable
./bin/lu-cleaner diagnostics disable
```

## Test distant réellement reçu

Le test opt-in utilise un consentement dans un dossier temporaire et une release `0.0.0-sentry-test`. Il ne scanne aucun dossier utilisateur et n'envoie ni logs ni contenu personnel. Un seul événement est envoyé par exécution.

```sh
LU_DIAGNOSTICS_DSN='https://7722a6368f3b87b5a21c34ae2973c7ef@o4510914016182272.ingest.de.sentry.io/4512192578715728' \
LU_DIAGNOSTICS_LIVE_TEST=1 \
GOMAXPROCS=2 go test -p 2 -count=1 -v -run '^TestSentryLiveSmoke$' ./internal/diagnostics
```

Deux exécutions ont servi à vérifier la configuration :

| Événement | Résultat |
| --- | --- |
| `3c53698a760e4f338253e4c00620b773` | Accepté et reçu en 0,15 s. Premier contrôle : localisation dérivée encore présente, malgré l'IP masquée. Ne constitue pas la preuve du filtrage final. |
| `ff9fcdeeaad14df5927b172a914928d4` | Accepté et reçu après correction, test en 0,24 s. JSON serveur consulté et champs contrôlés. |

Ils sont regroupés dans [LU-CLEANER-1](https://ludwig-developer.sentry.io/issues/151114848/?project=4512192578715728&query=release%3A0.0.0-sentry-test). Le JSON serveur du second événement confirme :

- Release `0.0.0-sentry-test`, commande `doctor`, composant `cli`, code `command_failed`, architecture `arm64`, système `darwin`, mode `eco`, notice `2026-10-03.2`.
- Une frame `internal/diagnostics/live_test.go:31`, avec `filename` et `abs_path` relatifs. Aucun chemin personnel ou contenu de variable locale.
- Aucun identifiant utilisateur, e-mail, username ou IP dans `user` ; champs géographiques `country_code`, `city`, `subdivision` et `region` tous `null`. `_meta.user.geo` atteste le retrait par la règle `project:0`.
- `extra: null`, aucune section `request` ou `breadcrumbs`. Sentry conserve des métadonnées de traitement et génère un contexte de trace à partir de l'identifiant aléatoire de cet événement. Ce contexte ne correspond pas à une transaction de performance ou une session transmise par la CLI.

L'acceptation HTTP et la lecture du JSON serveur sont deux vérifications distinctes. Les temps ci-dessus mesurent le test de transport, pas la latence complète d'affichage dans Sentry ou une garantie de livraison à la fermeture du programme. Le premier événement, antérieur à la correction, n'a pas été effacé ; son filtrage ne peut pas être modifié rétroactivement par les nouveaux réglages.

## Validation et limites

| Vérification après configuration | Résultat |
| --- | --- |
| `GOMAXPROCS=2 go test -p 2 -count=1 ./...` | Réussi ; test distant ignoré sans opt-in |
| `GOMAXPROCS=2 go test -p 2 -race -count=1 ./internal/diagnostics ./internal/cli` | Réussi, aucune race |
| `GOMAXPROCS=2 go vet -p 2 ./...` | Réussi |
| `make build`, `make universal`, `make nocgo`, `make docs` | Réussi ; arm64 et x86_64 dans le binaire universel |
| Statut CLI réel et fixture isolée | Destinataire compilé, consentement désactivé, JSON valide |
| `diagnostics enable --json` sans accord dans la fixture | Refusé, notice finalisée affichée, consentement inchangé |
| Build Astro | 93 pages, liens internes valides |
| Test Sentry opt-in | Réussi ; second événement inspecté dans le JSON serveur |

GoReleaser n'est pas installé localement ; son exécution complète et la future livraison GitHub ne sont pas vérifiées. Les avertissements Darwin `LC_DYSYMTAB` et cible macOS 12 contre objets macOS 26, ainsi que les avertissements Astro existants, ne font pas échouer les contrôles. Le runtime Intel et macOS 12 n'a pas été testé.

La livraison reste au mieux : réseau limité à deux secondes, fermeture avec 500 ms pour livrer puis annulation, sans retry ni backlog disque. Les rapports de panics Go récupérables ne couvrent pas SIGKILL, hard reboot, certains OOM/erreurs fatales ou crashes natifs. Les appels noyau bloqués ne sont pas instantanément annulables. Le récepteur voit l'IP de la connexion HTTPS même si elle n'est pas stockée dans l'événement. Le filtrage n'est pas une promesse d'anonymat ni une validation juridique générale.

Le détail des corrections, de l'état privé et des tests de confinement figure dans [DIAGNOSTICS_IMPLEMENTATION.md](DIAGNOSTICS_IMPLEMENTATION.md).

## État du diff local

À la fin de la configuration : branche `codex/docs-racing-light`, HEAD `2a732fc363b05fb42df11ea53d9c578d7b593faa`, 49 fichiers suivis modifiés, 31 fichiers non suivis, aucun fichier indexé. Cet inventaire comprend les changements de design et les corrections de diagnostics déjà présents ; il ne représente pas 80 nouveaux fichiers de configuration Sentry. Les binaires et le site générés restent ignorés par Git. Aucun commit ni publication effectué.

```sh
git status --short --untracked-files=all
git diff --stat
git diff --cached --stat
git diff --check
```
