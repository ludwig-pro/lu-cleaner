**Audit Mole / lu-cleaner et proposition de diagnostic volontaire — 3 octobre 2026**

Le contrôle de charge et les protections de suppression de lu-cleaner suivent les bons principes examinés chez Mole. Cela ne constitue ni une certification de sécurité, ni une preuve comparative de vitesse. Deux lacunes de gestion des panics ont été reproduites : un sous-scan IA peut échouer silencieusement et un worker de nettoyage peut faire tomber le processus malgré `safeClean`.

La recommandation est de corriger ces lacunes, de fournir un diagnostic local minimal, puis de proposer des rapports automatiques d'erreurs uniquement après un accord explicite. Aucun collecteur ni envoi de diagnostic n'a été ajouté dans cet audit.

**Périmètre et preuves.** lu-cleaner a été inspecté au commit `2a732fc363b05fb42df11ea53d9c578d7b593faa`, sur la branche locale `codex/docs-racing-light`. Les modifications préexistantes du site ont été préservées. Mole a été téléchargé pour lecture au commit `c3562541e876fa9ec1f6e133638b9eb59964279e` de `main` ; ce code de développement n'est pas nécessairement celui de sa dernière release. Mole n'a pas été exécuté sur le dossier personnel. Les vérifications concernent la CLI, pas une application graphique Mole ni le suivi des visiteurs du site de documentation.

**Ce que fait Mole et ce que nous avons déjà.**

| Sujet | Mole, code examiné | lu-cleaner, code actuel | Appréciation |
| --- | --- | --- | --- |
| Découverte | Le nettoyage utilise des scripts shell et des cibles connues. `purge` utilise `fd --threads 8`, sinon un `find` élagué, avec profondeur et délai bornés. | Providers spécialisés, découverte et dimensionnement intégrés au contrôleur commun. | Ne pas copier un nombre de threads sans mesurer l'ensemble des chemins. |
| Dimensionnement | L'analyseur Go partage des budgets distincts : jusqu'à 12 workers d'entrées, au plus 4 `du`, une file d'attente de `du` bornée et un budget pour le parcours de repli. | En `eco` : 2 lots d'E/S, 1 commande, 1 prélecture ; en `fast` : 8/4/4. Lectures de 256 entrées ; prélecture limitée à 128 chemins en attente. | Même principe de parallélisme borné ; le quota global de lu-cleaner doit rester la référence commune. |
| Cohabitation | Les budgets varient selon le chemin shell ou Go ; les plafonds de l'analyseur ne constituent pas un plafond unique de tout Mole. | Priorité macOS d'arrière-plan et `GOMAXPROCS=min(valeur héritée,2)` en `eco`, restaurés en sortie normale. Les profils actuels n'ajoutent plus de sommeil fixe. | Bonne architecture pour privilégier les autres applications, sans garantie de pourcentage CPU. |
| Cache | Cache versionné, admission des sous-arbres coûteux, TTL, plafonds de 5 000 entrées et 50 Mio pour le cache d'analyse. Les résultats partiels gardent un état explicite. | Déduplication des calculs ; cache de tailles de 50 000 entrées/64 Mio maximum ; validation par FSEvents, identité, session de démarrage et inspections d'écritures. Les parcours incomplets ne deviennent pas des entrées valides. | Conserver l'invalidation prudente. Un cache refusé peut expliquer un scan long sans justifier davantage de concurrence. |
| Suppression | Helpers communs de validation/suppression, exclusions, contrôles d'identité, simulations et Corbeille pour l'analyseur. | Garde commune, inodes revérifiés, montages, processus, protections Git/worktrees, simulation et option Corbeille. | Principes cohérents. La revérification réduit les courses ; elle ne rend pas un système de fichiers actif immuable. |
| Diagnostic | Journaux locaux avec rotation. Le document de sécurité exclut la télémétrie. | Historique local, `--verbose`, `LU_TRACE`, compteurs de ressources et temps des providers ; aucun SDK de télémétrie trouvé dans la CLI. | Base utile, mais collecte des erreurs et confidentialité des journaux à renforcer. |

Sources Mole : [découverte des projets](https://github.com/tw93/Mole/blob/c3562541e876fa9ec1f6e133638b9eb59964279e/lib/clean/project.sh#L576), [budgets de l'analyseur](https://github.com/tw93/Mole/blob/c3562541e876fa9ec1f6e133638b9eb59964279e/cmd/analyze/scanner.go#L112), [constantes/cache](https://github.com/tw93/Mole/blob/c3562541e876fa9ec1f6e133638b9eb59964279e/cmd/analyze/constants.go#L9), [validation de suppression](https://github.com/tw93/Mole/blob/c3562541e876fa9ec1f6e133638b9eb59964279e/lib/core/file_ops.sh#L1131), [journaux et rotation](https://github.com/tw93/Mole/blob/c3562541e876fa9ec1f6e133638b9eb59964279e/lib/core/log.sh#L25), [politique sans télémétrie](https://github.com/tw93/Mole/blob/c3562541e876fa9ec1f6e133638b9eb59964279e/docs/SECURITY_DESIGN.md#L248). Le code de mise à jour effectue néanmoins des [requêtes GitHub](https://github.com/tw93/Mole/blob/c3562541e876fa9ec1f6e133638b9eb59964279e/lib/manage/update.sh#L534) : absence de télémétrie ne signifie pas absence de toute fonction réseau.

Points d'intégration lu-cleaner : [profils et permis](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/scanctl/control.go#L31), [politique de ressources](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/scanctl/policy.go#L13), [lectures bornées](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/fsx/read.go#L17), [prélecture](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/providers/internal/sizes/sizes.go#L35), [cache persistant](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/fsx/store.go#L63), [revérifications avant suppression](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/clean/clean.go#L337), [commandes et groupes de processus](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/core/env.go#L72).

**Corrections prioritaires, avant d'ajouter un SDK.**

1. **P1 — Couvrir les panics des workers de nettoyage.** La goroutine de [clean.Run](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/clean/clean.go#L191) ne récupère pas un panic de `runOne`. Le `recover` de [safeClean](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/tui/picker_clean.go#L565) est dans une autre goroutine. L'injection d'un panic dans `Item.Recheck`, en dry-run et sous `safeClean`, termine le sous-processus avec le code 2. Ajouter une frontière d'erreur dans les workers, arrêter les nouvelles suppressions après une erreur interne, rejoindre les tâches déjà lancées et produire un résultat d'échec honnête. Ne pas reprendre automatiquement une suppression après panic et ne pas inventer le nombre d'octets libérés. Vérifier aussi les autres goroutines créées par les providers et les workers de tailles.
2. **P1 — Ne plus perdre un panic de sous-scan IA.** Le [recover du provider IA](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/providers/aitools/provider.go#L142) écrit seulement dans `env.Logf` ; `Scan` retourne ensuite `ctx.Err()`, donc `nil` en l'absence d'annulation. [Logf](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/cli/setup.go#L736) est muet hors verbose. L'injection d'un panic dans le callback d'émission reproduit ce résultat. Propager une erreur structurée au moteur, identifier le sous-scan et marquer son résultat incomplet, sans changer les schémas JSON existants pour cette correction. Le provider Apple fournit déjà un exemple de remontée de panics de ses workers.
3. **P2 — Protéger et borner les journaux locaux.** [appendHistory](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/clean/clean.go#L823) demande les permissions `0755` pour le répertoire et `0644` pour le fichier, sous réserve de l'umask et des permissions des parents. Ce n'est pas une exposition prouvée sur chaque Mac, mais les chemins et commandes peuvent devenir lisibles par d'autres utilisateurs locaux. Préférer `0700`/`0600`, gérer prudemment les fichiers existants et les symlinks, prévoir une rotation et une lecture bornée. Les erreurs d'encodage et d'enregistrement de l'historique sont actuellement ignorées : signaler une indisponibilité du journal sans faire croire que la suppression elle-même a échoué.
4. **P2 — Construire un schéma de diagnostic dédié.** Les erreurs du moteur, `debug.Stack`, [LU_TRACE](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/internal/fsx/trace.go#L25), l'historique et les erreurs d'outils peuvent contenir des chemins, arguments et données tierces. Le nettoyage des séquences de terminal n'est pas une anonymisation. Aucun de ces flux bruts ne doit devenir une pièce jointe automatique.

Le comportement de `recover` par goroutine est défini par [Go](https://go.dev/wiki/PanicAndRecover). Ces deux reproductions démontrent des défauts de confinement/remontée ; elles ne démontrent pas qu'un panic survient spontanément sur un scan normal.

**Améliorations de prévention.** Ajouter le race detector ciblé à la CI, des scénarios de panic dans les workers et des tests garantissant qu'un scan incomplet ne passe pas pour un succès complet. Conserver les tests d'admission globale, d'annulation, de cache partagé et de dry-run. Mole dispose aussi d'un audit des points de suppression et de tests de validation de chemins : c'est une bonne inspiration pour renforcer nos invariants, sans recopier son code GPL dans notre projet MIT. Notre release publie des checksums ; une attestation de provenance et l'épinglage des actions GitHub par SHA seraient des améliorations distinctes, secondaires pour cette demande.

**Le cadre pour la France et l'Union européenne.** Le diagnostic distant d'erreurs est possible en principe. Il faut qualifier le traitement réel ; ce document n'est pas une validation juridique de son futur déploiement. Le consentement n'est pas la seule base possible du RGPD, mais nous proposons de le retenir pour cette fonctionnalité facultative. Il doit correspondre à un choix réel, informé et positif, être démontrable et pouvoir être retiré simplement. Le refus laisse toutes les fonctions du cleaner disponibles. [CNIL : consentement](https://www.cnil.fr/fr/les-bases-legales/consentement).

Il faut également examiner l'article 82 de la loi Informatique et Libertés pour les accès/écritures sur le terminal : ces règles peuvent concerner un logiciel installé, pas uniquement un navigateur. Certains usages strictement nécessaires bénéficient d'exemptions ; le mot « crash » ne suffit pas à en déduire une. L'opt-in préalable proposé évite de baser le produit sur une exemption non démontrée. [CNIL : logiciels et traceurs](https://cnil.fr/fr/cookies-et-autres-traceurs/que-dit-la-loi).

Avant activation, la notice devra nommer le responsable du traitement et le prestataire, décrire les données/finalités, la conservation, les droits et le contact. Une durée de 30 jours pour les événements est une proposition produit à justifier, pas une durée légale universelle. Il faut un contrat de sous-traitance adapté et examiner les transferts éventuels ; une région d'hébergement européenne ne règle pas seule ces questions. [Information des personnes](https://www.cnil.fr/fr/informer-les-personnes), [conservation](https://www.cnil.fr/fr/passer-laction/les-durees-de-conservation-des-donnees), [sous-traitance](https://www.cnil.fr/fr/clauses-contractuelles-types-entre-responsable-de-traitement-et-sous-traitant), [transferts](https://www.cnil.fr/fr/les-outils-de-la-conformite/transferer-des-donnees-hors-de-lue).

Un rapport minimisé ne doit pas être présenté comme forcément anonyme : le destinataire reçoit une connexion réseau, et des champs techniques peuvent permettre des recoupements. L'accord de l'utilisateur n'autorise pas à envoyer les fichiers, secrets ou données de ses clients.

**Parcours utilisateur proposé — commandes à créer, non disponibles aujourd'hui.**

```text
lu-cleaner diagnostics status
lu-cleaner diagnostics enable
lu-cleaner diagnostics disable
lu-cleaner diagnostics export
```

`export` produit un fichier local à relire ; aucun envoi implicite ni ouverture d'issue préremplie avec des données personnelles. `enable` présente la notice et les champs avant activation. Une éventuelle proposition dans la TUI ne doit interrompre ni un scan, ni un nettoyage. L'état initial reste désactivé ; absence de réponse, Entrée ou fermeture ne valent pas acceptation. Le refus est mémorisé. Les scripts et `--json` ne déclenchent aucun dialogue, et le `--yes` du nettoyage ne vaut jamais accord au diagnostic.

Exemple de texte à finaliser une fois le destinataire et la conservation réellement choisis :

```text
Rapports d'erreurs facultatifs

Autoriser l'envoi de rapports techniques à [responsable], via [prestataire],
pour corriger les bugs de lu-cleaner ?

Inclus : version, système, composant et code d'erreur, pile technique filtrée.
Exclus : fichiers, chemins personnels, noms de projets et arguments de commandes.
Conservation : [durée]. Confidentialité et droits : [notice].
Vous pourrez désactiver ces envois avec « lu-cleaner diagnostics disable ».

> Non, garder désactivé
  Oui, activer les rapports d'erreurs
```

Conserver la version de la notice et la date/action d'accord, ainsi qu'une documentation vérifiable du mécanisme. Un changement substantiel de finalité, destinataire ou données impose de réévaluer l'accord. `disable` doit empêcher les nouveaux envois et purger la file non envoyée ; il ne peut pas rappeler un événement déjà reçu. La notice décrit séparément le traitement des demandes d'effacement.

**Données et implémentation proposées.**

| Acceptables dans le schéma fermé proposé | À exclure des envois automatiques |
| --- | --- |
| Version/build de lu-cleaner, architecture, version majeure de macOS | Nom d'utilisateur, hostname, numéro de série, identifiant permanent du Mac |
| Commande parmi des valeurs fixes (`scan`, `clean`…), mode, composant | Arguments, dossier courant, racines, volumes, branches et URL Git |
| Code d'erreur stable, phase, fonctions et fichiers source relatifs au projet | Message brut de panic/outil, variables locales, environnement, stdout/stderr |
| Identifiant aléatoire de l'événement pour support et déduplication | Contenu de fichiers, bases SQLite, sessions IA, historique de nettoyage, dumps mémoire |

Le regroupement doit reposer sur composant/code/pile filtrée/version, sans identifier une machine. Les refus de sécurité attendus, outils absents, accès refusés prévus et interruptions 130/143 ne sont pas des bugs à envoyer systématiquement. Une erreur nouvelle ou interne reste visible localement même lorsque les rapports distants sont refusés. Les métriques d'usage, le profiling continu et les enregistrements de session sont hors de cette proposition. Des rapports volontaires ne donnent pas, à eux seuls, un taux de crash fiable sur tous les utilisateurs.

Un petit package `internal/diagnostics` pourrait porter un rapport typé et un reporter inactif par défaut. Les points d'appel seraient les erreurs de fin de provider, les frontières de workers, les résultats de nettoyage et la sortie CLI. Il doit filtrer avant stockage et transport, limiter les répétitions, et ne jamais effectuer un envoi sous un verrou ou un permis de scan. Proposition initiale : file mémoire de 20 événements, taille de 16 Kio par événement, requêtes bornées, attente de fermeture de 500 ms maximum. Si la livraison échoue, le cleaner termine normalement ; pas de boucle de retry ni de service persistant. Ces valeurs devront être testées avant adoption.

Une file disque éventuelle vient ensuite : `0700`/`0600`, limite en nombre/octets/âge, suppression au retrait, contrôle du consentement lors de l'admission et de l'envoi. Ne pas mettre en attente des rapports automatiques avant consentement pour les envoyer plus tard. Un rapport manuel relu reste possible indépendamment.

Sentry est une option adaptée au regroupement des erreurs Go, à choisir après fixation de ce contrat de données. Les options actuelles comprennent `DataCollection` et `BeforeSend` ; les valeurs par défaut ne constituent pas une preuve de confidentialité, et `SendDefaultPII` est désormais documenté comme déprécié. Tester l'enveloppe réellement envoyée et toutes les catégories activées, pas seulement l'objet d'erreur initial. [Données collectées](https://docs.sentry.io/platforms/go/data-management/data-collected/), [options](https://docs.sentry.io/platforms/go/configuration/options/), [filtrage](https://docs.sentry.io/platforms/go/configuration/filtering/).

Sentry propose le stockage des événements en région UE/Francfort ; certains autres types de données restent stockés aux États-Unis et les conditions d'accès/traitement demeurent applicables. Ne pas promettre « tout reste en Europe » sur la seule sélection de cette région. [Localisation des données Sentry](https://docs.sentry.io/organization/data-storage-location/).

**Limites à annoncer.** Une capture de panics Go n'assure pas la collecte de tous les arrêts : SIGKILL, panne machine, hard reboot, certains OOM/erreurs fatales du runtime ou crashes natifs peuvent empêcher toute écriture ou tout envoi. Une sortie précédente incomplète est un indice, pas la preuve d'un bug. Un SDK ne diagnostiquera pas automatiquement un gel CPU ; les compteurs locaux existants et une reproduction mesurée restent nécessaires. Les appels noyau déjà bloqués ne deviennent pas instantanément annulables.

**Ordre de livraison recommandé.**

1. Réparer la remontée des panics, le confinement des workers et les journaux ; transformer les deux reproductions ci-dessous en tests de régression permanents.
2. Livrer un export local minimal et lisible, avec tests d'absence de chemins/secrets et un canal privé documenté pour les problèmes de suppression ou de sécurité.
3. Choisir le responsable/contact, le destinataire, la durée et la notice ; ajouter un opt-in persistant, réversible et indépendant des autres confirmations. Mettre à jour les FAQ EN/FR qui promettent aujourd'hui [aucune télémétrie](https://github.com/ludwig-pro/lu-cleaner/blob/2a732fc363b05fb42df11ea53d9c578d7b593faa/site/src/content/docs/fr/about/faq.md#L218).
4. Ajouter le transport optionnel et vérifier : zéro requête de diagnostic avant accord/après refus ; retrait et file ; JSON intact ; DNS/réseau indisponible ; timeout ; absence de données interdites même dans des erreurs imbriquées ; panics de workers ; absence de régression mesurée sur les scans.

**Vérifications exécutées.** Machine macOS 26.5.1, arm64, Go 1.25.6. La concurrence de compilation des tests a été réduite pour la cohabitation ; il ne s'agit pas d'une mesure du profil applicatif.

```sh
GOMAXPROCS=2 go test -p 2 ./...
GOMAXPROCS=2 go vet -p 2 ./...
GOMAXPROCS=2 go test -p 2 -race ./internal/scanctl ./internal/engine ./internal/fsx ./internal/sysx ./internal/tui ./internal/providers/aitools ./internal/clean
```

Les trois commandes réussissent. Le race detector n'a signalé aucune race sur ces packages ; le linker Darwin a émis des avertissements `LC_DYSYMTAB` sur plusieurs binaires de test. Certains tests de la première commande ont utilisé le cache Go. Aucun benchmark comparatif Mole/lu-cleaner, scan complet du dossier personnel, build de release ou validation juridique contractuelle n'a été effectué pour cet audit.

Deux sondes supplémentaires, injectées avec l'overlay Go depuis un dossier temporaire, échouent comme attendu et prouvent les lacunes décrites :

```text
TestAuditSubscanPanicIsReported
  subscan panic was recovered, but Scan returned nil
TestAuditSafeCleanContainsWorkerPanic
  worker panic escaped safeClean: exit status 2
```

Les sondes initiales ont été injectées depuis un dossier temporaire, sans modifier les packages du checkout. Leurs scénarios sont désormais couverts par les [tests permanents IA](../internal/providers/aitools/panic_test.go), [de nettoyage](../internal/clean/diagnostics_test.go), [de TUI](../internal/tui/panic_test.go) et [de cache](../internal/fsx/panic_test.go). Reproduction sur la version corrigée, avec des fixtures temporaires :

```sh
GOMAXPROCS=2 go test -p 2 -count=1 \
  -run 'Panic' ./internal/providers/aitools ./internal/clean ./internal/tui ./internal/fsx
```

L'état final du travail de cet audit est l'ajout de ce document uniquement. Aucun changement de code applicatif, de dépendance, de configuration de télémétrie, commit, push ou publication.

Suite à la demande d'implémentation, les corrections et le transport facultatif ont ensuite été réalisés localement, puis le projet Sentry officiel a été configuré. Le [compte rendu d'implémentation](DIAGNOSTICS_IMPLEMENTATION.md) distingue les validations locales de la réception distante. Les constats ci-dessus décrivent le snapshot audité avant ces corrections.
