---
title: Rapports d'erreurs techniques
description: Des rapports facultatifs avec consentement et des données techniques strictement limitées.
sidebar:
  order: 10
---

Les rapports techniques sont **désactivés par défaut**. Ils servent à corriger les erreurs internes et les panics Go récupérés. Aucun suivi d'usage, enregistrement de session, profiling continu ou identifiant permanent d'utilisateur ou de machine. Toutes les fonctions restent disponibles sans consentement.

```sh
lu-cleaner diagnostics status
lu-cleaner diagnostics enable
lu-cleaner diagnostics disable
lu-cleaner diagnostics export > lu-cleaner-report.json
```

`enable` affiche la notice et le destinataire, puis demande une réponse affirmative explicite. Entrée signifie non. Le refus est mémorisé. Les scripts et `--json` ne déclenchent aucun dialogue. Le flag `--yes` du nettoyage ne vaut jamais consentement aux rapports. Pour automatiser, lire la notice et accepter explicitement sa version :

```sh
lu-cleaner diagnostics enable --accept-notice 2026-10-03.2 --json
```

Un changement de destinataire ou de notice nécessite un nouvel accord. `disable` arrête les rapports en attente et supprime le rapport local. Il ne peut pas rappeler une requête déjà partie ou un rapport reçu. Pour une demande d'effacement, contacter [contact@ludwigvantours.dev](mailto:contact@ludwigvantours.dev), avec l'identifiant de l'événement fourni par `diagnostics export` si disponible. Ne pas publier de logs personnels dans une issue publique.

## Destinataire officiel

Le mainteneur utilise le projet dédié **lu-cleaner** de l'organisation Sentry **ludwig-developer**. Les événements d'erreur sont stockés dans sa région **Union européenne (Allemagne)**. Cela décrit le stockage des événements, sans garantir que toutes les données du compte ou des services Sentry restent en Europe.

Le projet utilise uniquement Error Monitoring, avec masquage des IP et règles supplémentaires supprimant les informations géographiques dérivées et les champs utilisateur, requête, extra, breadcrumbs et contextes non sollicités. lu-cleaner n'envoie ni tracing, ni enregistrement de session, ni collecte de logs, ni profiling continu.

Sentry peut ajouter ses propres métadonnées techniques de traitement, notamment un contexte de trace pour une erreur individuelle. La CLI ne transmet aucune transaction de performance ni donnée de session.

Le forfait Developer gratuit fournit [30 jours d'historique détaillé des événements](https://sentry.io/pricing/). Les métadonnées agrégées des issues et releases peuvent rester plus longtemps : cela ne garantit pas la suppression de toutes les données serveur après 30 jours. Le dernier rapport local filtré expire après sept jours. Pour un DSN personnalisé, vérifier sa région, sa conservation, ses règles de confidentialité et son contact avant de consentir.

## Les données

| Incluses | Exclues |
| --- | --- |
| Version, famille du système, architecture | Nom d'utilisateur, hostname, identifiant permanent du Mac |
| Nom de commande, mode eco/fast, composant et code d'erreur fixes | Arguments, noms de projets, racines, URL Git, chemins personnels |
| Fichiers source relatifs, fonctions et lignes de lu-cleaner | Messages bruts de panic ou d'erreur, environnement, variables locales, logs |
| Identifiant aléatoire de cet événement uniquement | Contenus de fichiers, bases SQLite, conversations IA, historique de nettoyage |

Le destinataire HTTPS voit nécessairement l'adresse IP de la connexion. Les rapports sont minimisés, **sans promesse d'anonymat**. Les outils absents, interruptions normales et refus de sécurité ne sont pas automatiquement des bugs à signaler. Les échecs opérationnels sont regroupés par code fixe, sans leur texte d'erreur brut.

Le dernier rapport filtré peut être exporté localement même avant consentement. Il n'est jamais envoyé rétroactivement. Un refus ou retrait supprime ce rapport local. Son expiration de sept jours est contrôlée au prochain accès aux diagnostics ; aucun service ne tourne pour supprimer le fichier pendant que la CLI est inactive.

## Limites et erreurs

Au maximum : un worker d'envoi, 16 événements par invocation, 16 Kio par événement et 256 Kio pour le rapport local. Les erreurs répétées sont dédupliquées. Les requêtes ont un délai de deux secondes, sans redirection ni retry. À la sortie, l'envoi dispose de 500 ms, puis la requête est annulée, avec au plus 100 ms supplémentaires pour fermer le worker. Un serveur indisponible ne change jamais le résultat du nettoyage. Aucune connexion au service de rapports sans consentement.

Un panic de worker rend le scan incomplet ou arrête les nouvelles admissions de nettoyage. Les résultats déjà obtenus restent comptabilisés. Refaire un scan avant de relancer un nettoyage après une erreur interne. Un appel noyau bloqué ne devient pas instantanément annulable. SIGKILL, redémarrage forcé, certaines erreurs fatales du runtime, OOM et crashes natifs peuvent empêcher tout rapport : récupérer les panics ne couvre pas tous les crashes.

L'historique reste sur le Mac : dossier privé `0700`, fichiers `0600`, un JSONL courant et un précédent de 5 Mio chacun. Les anciennes entrées sortent par rotation. Les symlinks, propriétaires étrangers, hardlinks et fichiers spéciaux sont refusés. Une erreur d'écriture produit un avertissement sans transformer une suppression réussie en échec. L'historique brut et les sorties `--verbose` / `LU_TRACE` ne sont jamais envoyés automatiquement.

## Brancher le destinataire

Un binaire sans destinataire reste fonctionnel ; `enable` explique la configuration manquante. Le DSN public Sentry peut être fourni par `LU_DIAGNOSTICS_DSN`, ou inclus dans le binaire par le mainteneur :

```sh
go build -ldflags='-X github.com/ludwig-pro/lu-cleaner/internal/cli.DefaultDiagnosticsDSN=https://PUBLIC_KEY@SENTRY_HOST/PROJECT_ID' -o bin/lu-cleaner ./cmd/lu-cleaner
```

Le projet officiel est configuré séparément de la CLI ; lu-cleaner ne crée aucun projet ni ne modifie la conservation serveur. Un DSN public n'est pas un token d'administration. Ne jamais fournir de token Sentry à la CLI. Le mainteneur retrouve le DSN public dans les réglages **Client Keys (DSN)** du projet.

Le transport minimal utilise les [enveloppes Sentry](https://develop.sentry.dev/sdk/envelopes/) et sérialise uniquement les champs ci-dessus, sans collecte par défaut d'un SDK généraliste. Pour reproduire localement les vérifications du consentement, des données réellement envoyées et du confinement des crashes :

```sh
go test -count=1 ./internal/diagnostics ./internal/statefile ./internal/clean ./internal/cli ./internal/tui ./internal/providers/aitools
go test -race ./internal/diagnostics ./internal/statefile ./internal/clean ./internal/engine ./internal/fsx ./internal/tui ./internal/providers/...
```

### Builds et releases

`make build`, `make install` et `make universal` intègrent le DSN public fourni par `LU_DIAGNOSTICS_DSN`. Sans cette variable, les binaires ne contiennent aucun destinataire. Le consentement reste désactivé : intégrer un DSN ne donne jamais l'accord de l'utilisateur.

```sh
LU_DIAGNOSTICS_DSN='https://PUBLIC_KEY@SENTRY_HOST/PROJECT_ID' make build
./bin/lu-cleaner diagnostics status --json
```

Le workflow Release transmet la variable de dépôt Actions `LU_DIAGNOSTICS_DSN` à GoReleaser. Aucun token d'administration Sentry n'est nécessaire pour compiler ou envoyer un rapport. Un binaire publié n'inclut ce destinataire qu'après une release contenant ces changements de workflow ; renseigner la variable ne modifie pas une release existante.

### Vérifier l'ingestion réelle

Le test suivant est volontairement ignoré par les suites normales et la CI. L'activer autorise **un seul événement technique synthétique** vers le DSN indiqué. Il utilise un consentement dans un dossier temporaire et ne modifie pas votre préférence réelle. Aucun scan du Mac ni contenu personnel n'est collecté.

```sh
LU_DIAGNOSTICS_DSN='https://PUBLIC_KEY@SENTRY_HOST/PROJECT_ID' \
LU_DIAGNOSTICS_LIVE_TEST=1 \
go test -count=1 -v -run '^TestSentryLiveSmoke$' ./internal/diagnostics
```

Le test affiche l'identifiant de l'événement et la release `0.0.0-sentry-test`. Un succès HTTP prouve l'acceptation par le récepteur. Vérifier ensuite dans Sentry l'ingestion, la pile relative, l'absence de données personnelles, le masquage des IP et la conservation effective. Ce succès seul ne prouve ni ces réglages serveur, ni la capture de tous les types de crash.
