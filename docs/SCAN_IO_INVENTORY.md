# Scan I/O inventory

The scan controller is carried by the invocation context. Standalone provider
entry points call `scanctl.Ensure`; they do not change process priority. The CLI
sets the actual eco/fast policy.

## Discovery admitted by the shared controller

| Operation | Implementation and callers |
| --- | --- |
| Directory names | Every provider discovery loop uses `fsx.ReadDir` or `fsx.ReadNames`: complete sorted results, reads of at most 256 entries, cancellation between reads. No permit is held while classifying entries, inspecting git, recursively walking, or measuring sizes. |
| Scan root preparation | Scan commands activate the validated profile before resolving root spelling and validating configured directories. `readSetupNames` reads at most 256 names per admitted operation with the same eco cooldown; the pure configuration-display setup does not activate policy. |
| Globs | Every provider scan glob uses `fsx.Glob(ctx, pattern)`, including catalog expansion, Android SDK/Studio versions, simulator assets, Codex databases, Claude sessions, and Homebrew API cache lookup. Its directory reads use the same batching and admission. |
| Implicit directory walks | Ollama manifests, AI per-project activity (`newestDeep`), and worktree secret discovery use `internal/scanwalk.WalkDir`. Lexical order, symlink handling, and `SkipDir`/`SkipAll` behavior are preserved; cancellation cannot be suppressed by the callback. |
| Metadata arrays | `internal/scanio.Lstats` and `UnixLstats` admit at most 256 path probes per batch and retain errors per path. Cancellation returns no partial array. AI/system entry lists, artifacts child identity and project activity, catalog matches, Apple direct-child activity, simulator recordings/logs, and Downloads allocation probes use these batches. |
| Scalar metadata during classification | Metadata in functions carrying a scan context uses `fsx.Lstat`; native allocation/device checks there use `scanctl.DoIO`. Retained legacy marker and existence helpers are listed below. Worktree `sameAsMain` admits its metadata and reads at most two files already bounded by `maxCompare` (1 MiB each). |
| Known metadata files | AI session indexes/manifests, app product and extension registries, workspace descriptors, tool state JSON, Homebrew receipts/API indexes, and git `commondir` use `fsx.ReadFile(ctx, path)`. Opens and reads of at most 64 KiB are admitted; parsing runs after releasing the permit. The original unlimited-total-size behavior is preserved. |
| Commands | All scan command wrappers call `Env.OutputTimeout`: git, sqlite, Spotlight, process listing, pgrep, xcrun/xcodebuild, package manager and engine discovery. The timeout starts after command admission. Remaining direct Runner calls and old `WithTimeout` blocks are cleaning rechecks below. |
| Native process probes | Default process guards use `sysx.RunningContext`, `CwdInsideContext`, or `ExecInsideContext`. Legacy provider function fields remain test seams. Failed guard inspection warns that state is unavailable; cancelled executable inspection keeps old binaries. Native process helpers share cancellable snapshot waits. |
| Protection guards | Every provider protection check uses `Env.IsProtectedContext` with that provider's scan context. A cancelled interactive scan cannot leave guard globs running under the longer-lived invocation context. Cancellation is fail-closed; legacy guard callbacks remain test seams. |
| Provider caches | `internal/scanmemo` shares in-flight directory listings, Spotlight/native process results and tool-state reloads without holding a mutex during the inspection. Field initialization uses a cancellable `Once` whose loader also runs outside its mutex. Cancelled consumers never read unfinished fields; cancelled keyed loads are discarded so a live consumer may retry. Worktree reconciliation snapshots its repository list and releases its state lock before reading admin directories. |
| Repeated size-walk callbacks | Artifacts `checkoutProbe.skip` and the worktree size breakdown callback admit their `.git` / bare-layout marker probes as one lot of at most four metadata calls per directory. Prune/allow decisions happen first where applicable. No provider mutex or size-walker permit is held during admission; results are recorded afterward. Cancellation stops descent and cannot prove a subtree free of repositories. These are repeated inventory probes, not the punctual checks below. |

Artifacts use one walker and one semaphore for every initial root and every
follow-up ignored-directory walk. Each traversal has its own task counter;
initial submissions keep it positive before waiting. The pool is bounded by the
scan I/O limit. Provider-specific goroutine ceilings still exist, but inventory
I/O and commands share the invocation's admissions.

Artifact size prefetch uses the resolved mode's worker count (eco 1, fast 4) and
at most 128 pending paths. `Add` never blocks. Only accepted paths are recorded
as seen, so dropped speculative work can be retried. Required size measurements
always run independently of that queue. `Close` drops pending paths and joins
all workers; cancelled scans wake idle workers and cancel their size consumers.
The engine joins run-owned cache measurements separately before closing stores.

The analyzer's `sizePool.stack` contains required measurements for every
unmeasured listed directory and post-clean remeasurements. Its pending count
follows those complete listings; unlike speculative artifact prefetch, it cannot
drop work at 128 paths without leaving rows and totals incomplete. Active workers
still use the profile's prefetch count and the shared I/O budget. A rescan cancels
and joins the previous pool, drops its pending work, and rejects obsolete results;
replaced listings have their own canceled context. Analyzer exit joins listings,
repository inspections, sizing workers, and any cleaning already started.

Android project discovery uses the parent context, configured depth (default
8), and a 250,000-directory cap. There is no separate 20-second wall timeout.
Cancellation, a cap, and an unreadable directory leave project attribution
incomplete: they cannot prove a version unused.

## Retained bounded probes

These checks operate on a known path or a small fixed set of marker paths. They
are not directory inventories. Their legacy signatures are retained to avoid
expanding every existence/safety API in this change.

| Area | Retained checks and bounds |
| --- | --- |
| Missing/existing project paths | AI `pathExistence`, `ancestorReadable`, `missingVolume`, `isDir`/`fileExists`; JS `pathMissing`, `isMountPoint`; worktrees `confirmedMissing` and `offlineVolume`; Apple `workspaceGone`. Ancestor walks stop at the filesystem root. Their readability probe is `Readdirnames(1)`, including EOF for an empty readable parent. TCC, EACCES, missing mounts and uncertain symlinks remain unknown. |
| Repository markers and identity | Artifacts `hasGitEntry`, `isBareRepo`, `validCacheDirTag` and `keyOf`/`realDir`; worktrees `gitKind`, `looksBare`, `keyOf`, and `readGitFile`; system `holdsGitRepo`. Their standalone uses inspect a fixed small marker set (`.git`, HEAD, objects, refs, CACHEDIR.TAG) or one path identity. Calls repeated inside size-walk callbacks are explicitly admitted as described above. |
| Volume/path resolution | Provider device seams (`devOf`/`statDev`), root validation, `EvalSymlinks`/`Readlink`, Android `resolveFrom` (bounded symlink hops), JS `uniqDirs` and `isDirOrLink`, fixed Android `exists`/`mtime`, system `exists`/`ctime`. They resolve or stat a specific path. Catalog per-match volume resolution is admitted explicitly. |
| Small file readers | Existing `readSmall`, plist, bounded JSON-field and git-text readers retain their bounds. The only retained provider `os.ReadFile` calls are JS `readSmall` (its prior 64 KiB metadata-size check) and worktree `sameAsMain` (inside an admitted operation, at most two files passing its 1 MiB size check). These calls do not enumerate trees. |

This change does not claim that every possible JSON/plist input has a bounded
allocation: chunked admission bounds each file I/O operation but preserves the
original total-size behavior. Input-size hardening would be a separate behavior
change.

## Cleaning rechecks retained

- `worktrees/recheck.go` keeps its fail-closed nested-repository walk. It runs
  immediately before cleaning, outside scan discovery.
- Apple simulator rechecks and system Docker rechecks retain their direct
  runner timeouts. JS Watchman rechecks retain their existing command wrapper.
- Existence and repository safety rechecks retain their legacy behavior. Shared
  helpers that now require a context (Claude resolver/listings and worktree
  admin-directory inventories) receive the *cleaning* context, not the expired
  scan context.
- `sysExecInside` is a legacy real-probe test seam; default AI scan discovery
  calls the context-aware native API.

## Validation

Provider tests cover existing TCC, volumes, symlinks, nested repositories,
clones/reclaim, rechecks, and inventories. Additional tests cover queue pressure
and retry, prefetch worker joins, a shared multi-root walker, cancellation while
waiting for I/O, a cached resolver returning unknown after cancellation,
conservative executable inspection, traversal skip compatibility, and metadata
batch errors/symlinks/cancellation. Cache tests check shared inspections,
independent keys, cancellation of waiting consumers and retry after an owner
is cancelled. Size-callback tests cover an occupied quota, cancellation and
complete nested-repository detection with a single I/O worker. The providers
suite is also run with Go's race detector.
