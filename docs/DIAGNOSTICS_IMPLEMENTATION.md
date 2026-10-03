# Corrections de l'audit et rapports techniques

Implémentation locale du 3 octobre 2026, à partir de `2a732fc` (`v0.3.1`), sur `codex/docs-racing-light`. Les modifications de design déjà présentes sont conservées. Ce compte rendu décrit les validations précédant la livraison de `v0.4.0`.

Les deux défauts P1 reproduits par l'audit sont corrigés : un panic de sous-scan IA remonte une erreur explicite ; un panic de worker de nettoyage est contenu. Les nouvelles admissions de nettoyage s'arrêtent et les résultats déjà obtenus restent comptabilisés. Les tests de régression permanents remplacent les sondes temporaires.

Les frontières de workers des autres providers, du dimensionnement partagé et de la TUI ont également été renforcées. L'erreur est publiée avant le signal de fin du worker. Le parent rejoint ses enfants, y compris après une erreur dans son propre parcours. Un panic de validation invalide le cache et réveille ses consommateurs ; un calcul partagé en échec est retiré du cache pour permettre un nouveau calcul.

L'historique est privé (`0700`/`0600`), protégé contre les destinations symlink/hardlink/spéciales ou appartenant à un autre utilisateur, avec rotation à 5 Mio et un seul fichier précédent. La lecture est bornée, y compris pour un ancien fichier trop grand. Les erreurs d'écriture/fermeture sont signalées séparément du résultat des suppressions. Les verrous de fichiers attendent au plus 100 ms.

Le package `internal/diagnostics` utilise une enveloppe Sentry minimale, sans SDK généraliste ni collecte implicite. Les champs sont fermés et filtrés avant transport et stockage : version, famille du système, architecture, commande, mode de scan, composant/code fixe, pile limitée aux fichiers source relatifs de lu-cleaner et identifiant aléatoire de cet événement. Aucun identifiant permanent, log brut, argument, environnement, contenu de fichier, historique, chemin personnel ou valeur de panic. Les échecs opérationnels sont regroupés par code fixe ; les interruptions et refus de sécurité attendus ne sont pas des rapports de bug.

Le consentement est désactivé par défaut, enregistré dans un fichier privé et lié à la version de la notice et au destinataire. `--yes` ne donne jamais cet accord. Entrée signifie non ; le refus est enregistré. Les scripts doivent accepter explicitement une version de notice. Un changement de notice/destinataire impose un nouvel accord. Le retrait empêche les nouveaux envois en attente et supprime le rapport local, sans rappeler une requête déjà partie.

La file mémoire contient au plus 16 événements par invocation, de 16 Kio chacun. Un seul worker d'envoi, délai réseau de deux secondes, aucune redirection ni retry ni backlog disque. La fermeture dispose de 500 ms pour livrer, puis annule la requête, avec au plus 100 ms supplémentaires pour fermer le worker. Une panne du récepteur ne change pas le résultat du nettoyage. Le dernier rapport local est borné à 256 Kio ; son expiration à sept jours est contrôlée au prochain accès. Aucun envoi rétroactif de rapports obtenus sans consentement.

Commandes disponibles :

```sh
./bin/lu-cleaner diagnostics status
./bin/lu-cleaner diagnostics enable
./bin/lu-cleaner diagnostics disable
./bin/lu-cleaner diagnostics export > lu-cleaner-report.json
```

Le branchement au projet Sentry officiel est maintenant configuré, avec une notice finalisée (`2026-10-03.2`), le contact privé `contact@ludwigvantours.dev` et le DSN public intégré au binaire local. Le consentement personnel reste indécis et les rapports désactivés. La section de configuration ci-dessous précise les réglages et la validation distante. Le destinataire reçoit l'IP de la connexion : la notice ne promet pas l'anonymat. La procédure et les limites figurent dans les guides EN/FR.

Validation locale :

| Contrôle | Résultat |
| --- | --- |
| `go test ./...` | Réussi |
| `go vet ./...` | Réussi |
| Race detector : diagnostics, état privé, cleaner, CLI, engine, fsx, TUI et tous les providers | Réussi, aucune race signalée |
| Panics IA et cleaner, worker de taille, parcours bulk/fallback | Erreurs explicites, résultats incomplets ou échecs sans crash du processus |
| Cache partagé : deux consommateurs, un loader en panic, puis nouveau calcul | Consommateurs réveillés ; résultat en échec non réutilisé |
| Consentement absent/refusé, notice ancienne et destinataire modifié | Aucun worker réseau ni requête |
| Récepteur HTTPS local : enveloppe et filtrage, déduplication, retrait, file pleine, timeout, redirection | Réussi |
| `make build`, `make nocgo`, `make universal` | Réussi ; architectures arm64 et x86_64 confirmées |
| `make docs` et `npm run build` dans `site` | Réussi ; 93 pages, liens internes valides |
| CLI réelle sur fixture : scan dry-run JSON et analyze JSON | Un artefact final ; 40 960 octets analysés |
| CLI réelle : refus de `--yes`, accord explicite puis retrait | Réussi |
| Récepteur surveillé pendant les invocations saines avec accord | Zéro connexion : aucune remontée d'usage |

Les tests ont utilisé `GOMAXPROCS=2` et `-p 2` pour limiter la compilation. Certains contrôles finaux réutilisent le cache Go ; des suites complètes et le race detector ont aussi été exécutés sans cache (`-count=1`). Ces paramètres ne constituent pas une mesure du profil de scan. Aucun gain de performance ni taux de crash global n'est revendiqué. Le linker Darwin émet des avertissements `LC_DYSYMTAB` et des avertissements liés à des objets mis en cache pour macOS 26 contre une cible 12.0 ; les commandes réussissent. L'exécution sur macOS 12 et sur une machine Intel n'a pas été vérifiée.

Le test CLI réel a utilisé une fixture dans un dossier temporaire. Lors de cette première validation, le récepteur était local et temporaire ; aucun rapport n'avait encore été envoyé à Sentry. Les fixtures de suppression appartiennent uniquement aux dossiers temporaires des tests. Le dossier utilisateur réel n'a pas servi de test de charge. Les tests distants réalisés ensuite envoient uniquement des événements synthétiques, avec un consentement temporaire.

Reproduction des tests ciblés :

```sh
GOMAXPROCS=2 go test -p 2 -count=1 ./internal/diagnostics ./internal/statefile ./internal/clean ./internal/cli ./internal/tui ./internal/providers/aitools
GOMAXPROCS=2 go test -p 2 -count=1 -run 'TestRecursivePanic|TestSharedMeasurementPanic|TestCacheValidationPanic' ./internal/fsx
GOMAXPROCS=2 go test -p 2 -race ./internal/diagnostics ./internal/statefile ./internal/clean ./internal/cli ./internal/engine ./internal/fsx ./internal/tui ./internal/providers/...
```

Limites : les panics Go récupérables ne couvrent pas SIGKILL, un hard reboot, certains OOM/erreurs fatales du runtime ou crashes natifs. Les appels noyau déjà bloqués ne sont pas instantanément annulables. Le transport et le consentement sont validés contre un récepteur de test ; la validation du projet réel est décrite ci-dessous. Cette implémentation ne vaut pas validation juridique du service.

## Branchement Sentry du 3 octobre 2026

Les cibles Make et GoReleaser acceptent maintenant `LU_DIAGNOSTICS_DSN` pour intégrer un DSN public. Le workflow Release lit la variable Actions du dépôt du même nom. Aucun token d'administration n'est intégré au binaire. Un build avec un DSN de fixture a été exécuté : `diagnostics status --json` retrouve le destinataire compilé après retrait de la variable d'environnement et confirme que les rapports restent désactivés.

`TestSentryLiveSmoke` fournit un test réel opt-in : `LU_DIAGNOSTICS_LIVE_TEST=1` et un DSN public sont tous deux requis. Un événement synthétique est envoyé, avec une release de test et un consentement temporaire. La suite normale ne contacte jamais Sentry. L'identifiant retourné permet de vérifier ensuite l'ingestion et les réglages du projet ; l'acceptation HTTP seule ne les prouve pas.

Le token d'environnement ne permet pas de lister les organisations ou projets de sentry.io (HTTP 403). Après connexion de l'utilisateur, la configuration a été faite dans l'interface authentifiée, sans créer de token d'administration ni modifier les autres projets. Projet `lu-cleaner` (`4512192578715728`), plateforme Go, organisation `ludwig-developer`, région UE, forfait Developer gratuit. Aucun essai Business, changement de forfait ou moyen de paiement ajouté.

La variable Actions de dépôt `LU_DIAGNOSTICS_DSN` a été créée et relue : son contenu correspond exactement au DSN public du projet. Le binaire local et le binaire universel ont été reconstruits avec ce destinataire. Le statut réel et une fixture isolée confirment le destinataire compilé, un JSON valide et l'absence de consentement. L'activation non interactive sans accord explicite est refusée et affiche la notice finalisée. Aucun consentement personnel ni release existante n'a été modifié.

Le premier événement synthétique (`3c53698a760e4f338253e4c00620b773`, release `0.0.0-sentry-test`) a été accepté puis retrouvé dans l'issue `LU-CLEANER-1` (`151114848`). Son inspection a révélé que le masquage de l'IP seul laissait une localisation approximative calculée par Sentry. Le filtre a été corrigé avec les sélecteurs récursifs documentés : `[Remove] [Anything] from [$user.** || $http.** || extra.** || $breadcrumb.** || contexts.** || server_name]`, plus `city`, `subdivision`, `region` et `country_code` dans les champs sensibles. Ces règles ne modifient que les prochains événements ; le premier rapport ne constitue pas la preuve du filtrage final.

Les scrubbers serveur et par défaut ainsi que le masquage des IP sont activés ; minidumps désactivés ; récupération de sources JavaScript et SCM désactivée ; vérification TLS des requêtes sortantes activée ; protection contre les pics active. Le forfait Developer fournit 30 jours d'historique détaillé des événements, sans garantie de purge de toutes les métadonnées agrégées après ce délai. La notice précise cette distinction et le contact privé d'effacement. Voir les [réglages Sentry](https://ludwig-developer.sentry.io/settings/projects/lu-cleaner/security-and-privacy/).

Vérifications du branchement final : `go test ./...` sans cache, race detector diagnostics/CLI, `go vet ./...`, `make build`, `make universal`, `make nocgo` et `make docs` réussis. GoReleaser n'est pas installé localement ; son exécution complète n'a pas été vérifiée. Les avertissements Darwin préexistants restent présents. La validation distante finale et le résultat du build documentaire sont consignés dans [SENTRY_SETUP.md](SENTRY_SETUP.md).
