---
title: Technical error reports
description: Optional, consent-based bug reports with a strictly limited technical payload.
sidebar:
  order: 10
---

Technical reports are **off by default**. They help the maintainer diagnose internal errors and recovered Go panics. There is no usage analytics, session replay, continuous profiling or permanent user/device identifier. Every feature works without consent.

```sh
lu-cleaner diagnostics status
lu-cleaner diagnostics enable
lu-cleaner diagnostics disable
lu-cleaner diagnostics export > lu-cleaner-report.json
```

`enable` displays the notice and recipient, then asks for an explicit affirmative answer. Enter means no. Refusal is saved. Scripts and `--json` never prompt. The cleaning `--yes` flag never gives consent to reporting. For automation, read the notice and explicitly accept its current version:

```sh
lu-cleaner diagnostics enable --accept-notice 2026-10-03.2 --json
```

A changed recipient or notice requires new consent. `disable` stops pending reports and removes the local report. It cannot recall a request already in flight or a report already received. For deletion requests, contact [contact@ludwigvantours.dev](mailto:contact@ludwigvantours.dev), including the event ID from `diagnostics export` if available. Do not post personal logs publicly.

## Official recipient

The maintainer uses the dedicated **lu-cleaner** project in the **ludwig-developer** Sentry organization. Error events are stored in its **European Union (Germany)** region. This describes event storage, not a guarantee that every kind of Sentry account or service data stays in Europe.

The project uses Error Monitoring only, with IP scrubbing and additional rules to remove derived geographic information and unsolicited user, request, extra, breadcrumb and context fields. There is no tracing, session replay, log collection or continuous profiling from lu-cleaner.

Sentry can add its own technical processing metadata, including a trace context for an individual error. The CLI sends no performance transactions or session data.

The free Developer plan provides a [30-day lookback for event details](https://sentry.io/pricing/). Aggregated issue and release metadata can remain longer: this is not a guarantee that all server data is deleted after 30 days. The latest local sanitized report expires after seven days. For a custom DSN, verify that recipient's region, retention, privacy rules and contact before consenting.

## What is included

| Included | Excluded |
| --- | --- |
| Release, OS family, architecture | Username, hostname, permanent machine ID |
| Command name, eco/fast mode, fixed component and error code | Arguments, project names, roots, Git URLs, personal paths |
| Relative source filenames, function names, line numbers in lu-cleaner | Raw panic/error messages, environment, local variables, logs |
| Random ID for this individual error event | File contents, SQLite data, AI conversations, cleanup history |

The HTTPS receiver necessarily sees the connection's IP address. Reports are minimized, **not claimed to be anonymous**. Missing tools, normal interruptions and safety refusals are not automatically reported as bugs. Operational failures are grouped by a fixed code rather than their raw error text.

The latest sanitized report can be exported locally even before consent. It is never uploaded retroactively. Refusal/revocation suppresses this local report. Its seven-day expiry is checked on the next diagnostics access; there is no background service to delete it while the CLI is idle.

## Bounds and failure handling

There is at most one sender, a maximum of 16 events per invocation, 16 KiB per event and a 256 KiB local report. Repeated failures are deduplicated. Requests time out after two seconds, do not follow redirects and are not retried. Exit gives delivery 500 ms, then cancels the request (with a further 100 ms bound for shutdown). An unavailable receiver never changes the cleanup outcome. Nothing connects to the reporting service while consent is absent.

Worker panics mark a scan incomplete or stop further cleanup admissions. Already completed results remain accounted for. Rescan before retrying cleanup after an internal error. Cancellation cannot instantly interrupt a blocked kernel call. SIGKILL, hard reboot, some runtime fatal errors, OOM and native crashes may prevent any report; recovered panics do not cover every possible crash.

Local history stays on the Mac: private directory `0700`, files `0600`, one current JSONL file and one previous file, each limited to 5 MiB. Older entries rotate out. Symlinks, foreign owners, hardlinks and special files are refused. A history write failure produces a warning without turning a successful deletion into a failed one. Raw history and `--verbose` / `LU_TRACE` output are never automatically uploaded.

## Configuring the receiver

A binary without a configured receiver remains usable; `enable` explains the missing configuration. The public Sentry DSN can be supplied through `LU_DIAGNOSTICS_DSN`, or included by the maintainer at build time:

```sh
go build -ldflags='-X github.com/ludwig-pro/lu-cleaner/internal/cli.DefaultDiagnosticsDSN=https://PUBLIC_KEY@SENTRY_HOST/PROJECT_ID' -o bin/lu-cleaner ./cmd/lu-cleaner
```

The official project is configured separately from the CLI; lu-cleaner never creates projects or changes server retention. A public DSN is not an administrative API token. Never provide a Sentry auth token to the CLI. Maintainers can find the public DSN under the project's **Client Keys (DSN)** settings.

The implementation uses a small [Sentry envelope transport](https://develop.sentry.dev/sdk/envelopes/) that serializes only the fields above; it does not activate a general-purpose SDK's default data collection. To reproduce the wire, consent and crash containment checks locally:

```sh
go test -count=1 ./internal/diagnostics ./internal/statefile ./internal/clean ./internal/cli ./internal/tui ./internal/providers/aitools
go test -race ./internal/diagnostics ./internal/statefile ./internal/clean ./internal/engine ./internal/fsx ./internal/tui ./internal/providers/...
```

### Builds and releases

`make build`, `make install` and `make universal` embed the public DSN provided through `LU_DIAGNOSTICS_DSN`. Without that variable, binaries contain no receiver. Consent stays off: embedding a DSN never grants the user's permission.

```sh
LU_DIAGNOSTICS_DSN='https://PUBLIC_KEY@SENTRY_HOST/PROJECT_ID' make build
./bin/lu-cleaner diagnostics status --json
```

The Release workflow passes the Actions repository variable `LU_DIAGNOSTICS_DSN` to GoReleaser. No Sentry administration token is required to build or deliver a report. A published binary only contains this receiver after a release built from these workflow changes; setting a repository variable does not change an existing release.

### Verify live ingestion

The following test is deliberately skipped in ordinary suites and CI. Enabling it authorizes **one synthetic technical event** to the specified DSN. It uses consent in a temporary directory and leaves your actual preference untouched. It does not scan your Mac or collect personal content.

```sh
LU_DIAGNOSTICS_DSN='https://PUBLIC_KEY@SENTRY_HOST/PROJECT_ID' \
LU_DIAGNOSTICS_LIVE_TEST=1 \
go test -count=1 -v -run '^TestSentryLiveSmoke$' ./internal/diagnostics
```

The test prints the event ID and release `0.0.0-sentry-test`. HTTP success proves acceptance by the receiver. Then verify ingestion, relative frames, absence of personal data, IP scrubbing and effective retention in Sentry. HTTP success alone proves neither these server settings nor coverage of every crash type.
