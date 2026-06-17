# ai-logfixer

`ai-logfixer` provides contracts and future engine components for standalone AI-assisted investigation, diagnosis, and guarded remediation.

The product should also expose a modular, composable React UI so the native AI LogFixer app and external integrations can render the same investigation and remediation workflows from the public contracts.

Contracts separate outside-system links from knowledge graph links:

- `external_refs` points to external systems such as GitHub, CI/CD, SIEM, Slack, or ControlOne records.
- `knowledge_refs` points to graph nodes in either an AI LogFixer-owned knowledge graph or a central shared graph used across products.

## Phase 1 contracts

The v1 contracts live in `contracts/v1`.

```text
------------------+
| JSON Schema v1  |
+--------+---------+
         |
         +--> Go structs
         +--> examples
         +--> drift checks
         +--> future TypeScript types
```

Run the full test suite:

```bash
go test ./...
```

Run the operational drift Docker readiness lab:

```bash
labs/readiness/bin/run-docker-lab.sh --mode fixture-health
labs/readiness/bin/run-docker-lab.sh --mode benchmark
```

Fixture health is expected to pass. Benchmark mode is expected to fail today unless a real candidate fixer is provided through `AI_LOGFIXER_CANDIDATE_COMMAND`. See [Operational Drift Docker Readiness Lab](labs/readiness/README.md).

Run the optional PostgreSQL store integration test by pointing it at an empty test database. The test creates and drops an isolated schema:

```bash
AILOGFIXER_POSTGRES_DSN='postgres://user:pass@127.0.0.1:5432/ai_logfixer_test?sslmode=disable' \
  go test ./internal/store/postgres -run TestPostgresStoreIntegration -count=1
```

The GitHub Actions workflow runs the normal Go suite and a PostgreSQL-backed integration job on pull requests.

## Architecture direction

The current repo is intentionally contract-first. Runtime work should now converge on a durable workflow architecture instead of growing as separate CLI-only flows.

- [Durable workflow architecture](docs/architecture/durable-workflow-architecture.md) describes the target system of record, workflow state machine, framework adapter boundary, remediation runtime, and DB shape.
- [Runtime safety policy](docs/architecture/safety-policy.md) defines remediation risk classes, redaction, approval, rollback, status, versioning, and capacity rules.
- [External integration boundaries](docs/architecture/external-integration-boundaries.md) defines API, webhook, `external_refs`, approval, and embedded UI ownership rules for external platforms.
- [Runtime V2 truth recovery](docs/v2/runtime-truth-recovery.md) describes the active runtime architecture for recovering real errors, detecting suppression sites, redacting evidence, and preparing guarded fix bundles.
- [Incident evidence intake](docs/v2/incident-evidence-intake.md) describes the small generic intake package for normalizing logs, probes, config snapshots, manifests, permissions, dependencies, services, and process metadata before resolver handoff.
- [Environment variable diagnostics](docs/v2/env-var-diagnostics.md) describes the safe Runtime V2 env-var drift MVP for missing variable detection, secret blocking, and explicit non-secret default writes.
- [Framework permission intelligence](docs/v2/framework-permission-intelligence.md) describes the Runtime V2 permissions mode for policy-backed Laravel permission repair with stat evidence, rollback manifests, and verification.
- [Phase 1 progress and architecture review](docs/reviews/phase-1-progress-and-architecture-review.md) maps the current codebase, open issues, PRs, biggest gaps, and recommended next step.
- [Live scenario validation](docs/reviews/live-scenario-validation-2026-05-26.md) records real local Runtime V2/Goravel runs and evaluates public log/error corpora for future fixtures.
- `internal/domain` centralizes allowed investigation, remediation, and approval state transitions.
- `internal/store` defines the durable repository, lease, audit, and outbox boundary that future API/worker implementations should use.
- `internal/store/postgres` is the first concrete SQL implementation for transaction-scoped contract records, optimistic status updates, workflow leases, audit events, and outbox delivery.
- `internal/workflow` is the first service layer over the store: it owns status transitions and writes audit/outbox records in the same transaction.
- `internal/engine` contains shared incident-signal grouping, dynamic contract ID generation, and blocked/escalated remediation helpers.
- `internal/runtime/v2` is the Runtime V2 conservative JSON-config remediation path: the demo app still uses `/orders` and `upstream_url`, but the reusable runner can match other routes/statuses, patch an explicit JSON key path, verify an explicit URL, and escalate safely when no allowlisted patch descriptor exists. It can also call `internal/workflow` when a workflow service is supplied.
- `internal/runtime/permissions` is the Runtime V2 framework permission resolver. The first supported policy is Laravel writable runtime directories; it detects drift with stat/write-probe evidence, blocks unsafe paths, applies bounded `mkdir`/`chmod` repairs, writes rollback manifests, and verifies recovery.
- `internal/truth` defines the Runtime V2 truth-recovery layer: stack trace resolution, suppression-site detection, staged reveal planning, redaction, and scoped fix-bundle creation for explicit opencode handoff.
- `db/migrations/postgres/0001_workflow_store.sql` is the first reference PostgreSQL schema for the durable workflow store.
- `internal/frameworks/goravel` is the first framework-adapter slice: it parses real Goravel access logs, maps failing routes to controller handlers, collects source evidence, builds contract-valid source patch previews, and can execute only a handler-scoped single-panic source patch through restart/verify callbacks.
- `internal/remediation` contains reusable remediation executors such as source-file patching with snapshot backup and rollback-on-failed-verification.
- `internal/signals/loghub` adds corpus-style Apache/OpenStack signature grouping that produces blocked/escalated contracts when no source owner or safe patch target is known.

## Runtime V2 Truth Recovery

Runtime V2 centers the product around error truth recovery before remediation. The demo path uses `/orders` and `upstream_url`, while the reusable Runtime V2 runner supports explicit route/status matching, JSON key-path patching, and verification URL/status options.

Runtime V2 must not automatically disable production error suppression. When stack traces are hidden by custom handlers, framework adapters should plan staged/local diagnostic reveal steps, redact the recovered evidence, and only then prepare a scoped opencode fix bundle.

```text
broken demo app config
        |
        v
repeated /orders 503 responses
        |
        v
AI LogFixer detects the threshold
        |
        v
AI LogFixer traces logs + config
        |
        v
AI LogFixer writes DiagnosisResult + RemediationPlan
        |
        v
AI LogFixer backs up the config, patches the allowlisted JSON key, verifies the route, and records a Receipt
```

Run the demo app in a broken state:

```bash
go run ./cmd/demo-goravel-app \
  -addr 127.0.0.1:8090 \
  -config ./tmp/demo-goravel-app.json \
  -log ./tmp/demo-goravel-app.log \
  -init-broken=true
```

In another shell, generate repeated 503s:

```bash
for i in 1 2 3 4 5; do
  curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8090/orders
done
```

Run the Runtime V2 fixer:

```bash
go run ./cmd/ai-logfixer-v2 \
  -base-url http://127.0.0.1:8090 \
  -config ./tmp/demo-goravel-app.json \
  -log ./tmp/demo-goravel-app.log \
  -route /orders \
  -status 503 \
  -config-key upstream_url \
  -healthy-upstream http://127.0.0.1:8090/upstream/orders \
  -threshold 3
```

For non-demo services, pass an explicit `-route`, `-status`, `-config-key`, `-replacement-value`, `-verify-url`, and `-expected-status`. If those patch descriptors are incomplete, AI LogFixer records blocked/escalated contracts and leaves the target unchanged.

Run truth recovery directly when you already have a stack trace:

```bash
go run ./cmd/ai-logfixer-v2 \
  -mode truth \
  -service payments-api \
  -framework go \
  -environment staging \
  -message "payment failed" \
  -stack-trace-file ./tmp/payment-stacktrace.txt
```

Run truth recovery against custom error suppression code when the real exception is hidden:

```bash
go run ./cmd/ai-logfixer-v2 \
  -mode truth \
  -service checkout-api \
  -framework go \
  -environment staging \
  -message "checkout failed" \
  -source-file ./app/http/controllers/checkout_controller.go
```

Production reveal attempts are blocked by design; Runtime V2 emits an escalated remediation plan instead of disabling suppression automatically.

Run issue #27 runtime drift resolvers through JSON input files:

```bash
go run ./cmd/ai-logfixer-v2 -mode envvars -input ./tmp/envvars-input.json
go run ./cmd/ai-logfixer-v2 -mode database -input ./tmp/database-input.json
go run ./cmd/ai-logfixer-v2 -mode resources -input ./tmp/resources-input.json
go run ./cmd/ai-logfixer-v2 -mode restart -input ./tmp/restart-input.json
go run ./cmd/ai-logfixer-v2 -mode tokens -input ./tmp/tokens-input.json
go run ./cmd/ai-logfixer-v2 -mode versions -input ./tmp/versions-input.json
```

These modes expose the existing safe resolver packages as black-box product commands. Mutating modes still require explicit allowlists or policy fields in the input, and secret-bearing diagnostics continue to block or redact rather than invent values.

Verify the app recovered:

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8090/orders
```

Expected result:

```text
200
```

## Goravel framework runner

`cmd/ai-logfixer-goravel` is the first real-framework validation path for issue #11. It reads a Goravel access log, detects repeated route failures, maps the failing route through `routes/web.go` to the owning controller method, and emits contract-valid investigation, diagnosis, and remediation plan JSON. Goravel access lines may come from stdout capture rather than `storage/logs`, so pass `-access-log` explicitly when needed. The reproducible manual validation steps are in [Goravel real-framework validation](docs/runbooks/goravel-real-framework-validation.md).

Dry-run analysis:

```bash
go run ./cmd/ai-logfixer-goravel \
  -target /path/to/goravel-app \
  -access-log /path/to/goravel-app/app.stdout.log \
  -service goravel-app \
  -threshold 3
```

Apply mode is intentionally narrow: it only removes exactly one identified `panic(...)` line inside the mapped handler, snapshots the source file first, runs an optional restart command, requires a verification command, and rolls back if restart or verification fails. If the handler has no allowlisted panic patch or has multiple panic lines, the adapter emits a blocked/escalated remediation plan instead of editing source.

```bash
go run ./cmd/ai-logfixer-goravel \
  -target /path/to/goravel-app \
  -access-log /path/to/goravel-app/app.stdout.log \
  -service goravel-app \
  -apply=true \
  -approve-source-patch=true \
  -restart-command "systemctl restart goravel-app" \
  -verify-command "curl -fsS http://127.0.0.1:3000/users"
```

## Laravel production error-page runner

`cmd/ai-logfixer-laravel` handles a Laravel failure mode where production renders the friendly error page even when the load balancer/browser flow reports `200`. It does not rely on status codes alone. It:

- probes the URL body for Laravel production error-page signatures such as `Sorry.` / `Go Back`
- reads the latest `storage/logs/laravel*.log` or a supplied `-log`
- classifies common Laravel/PHP/database failures including missing classes, undefined methods, missing views, missing routes, failed container bindings, missing tables/columns, permission failures, syntax errors, and undefined variables/keys/properties
- scans the target directory for PSR-4 `App\...` references whose expected files are missing
- auto-remediates eligible missing `App\...` classes by generating a conservative compatibility stub from observed PHP/Blade usage, writing a rollback marker, linting with `php -l` when PHP is available, and re-probing the URL
- refuses unsafe automatic patches and returns a blocked/escalated remediation result with evidence when the issue requires a real migration, config change, dependency fix, source edit, or manual review
- can delegate unsupported issues to an external coding agent such as `opencode`, in a staging copy, then validate, apply, verify, and record rollback metadata

Example against a deployed Laravel target:

```bash
ai-logfixer-laravel \
  -target /var/www/fraudv \
  -service fraudv \
  -url http://192.168.61.34/transactions/3478538 \
  -log /var/www/fraudv/storage/logs/laravel-2026-05-24.log \
  -apply=true
```

If the page requires an authenticated session, pass the cookie/header from the failing browser request:

```bash
ai-logfixer-laravel \
  -target /var/www/fraudv \
  -service fraudv \
  -url http://192.168.61.34/transactions/3478538 \
  -header 'Cookie: fraudsniper_session=...' \
  -apply=true
```

For this incident class, avoid `-http-status-only=true`; the whole point is that Laravel may return a friendly error page through infrastructure that appears healthy.

The Laravel runner is intentionally not an "auto-fix everything" tool. It can catch broad Laravel failure signals and produce contract-valid diagnosis output for unknown or unsupported errors, but it only writes changes for low-risk missing-class compatibility stubs that can be inferred from local usage.

### External agent remediation

For more complex errors, enable the guarded external-agent path:

```bash
ai-logfixer-laravel \
  -target /var/www/fraudv \
  -service fraudv \
  -url http://192.168.61.34/transactions/3478538 \
  -log /var/www/fraudv/storage/logs/laravel-2026-05-24.log \
  -external-agent=true \
  -agent-model "anthropic/claude-sonnet-4" \
  -validate "php artisan test --no-interaction" \
  -apply=true
```

The external agent receives a structured evidence prompt and edits only a staging copy. By default, AI LogFixer runs `opencode run --file {prompt_file}`; pass `-agent-command` to use another opencode invocation or compatible CLI. AI LogFixer diffs the staging copy against the target, runs automatic PHP lint when PHP is available, runs every `-validate` command, applies the patch only after validation passes, re-probes the failing URL, and writes a rollback manifest under `.ai-logfixer-backups`.

Rollback uses the recorded manifest:

```bash
ai-logfixer-rollback \
  -manifest /var/www/fraudv/.ai-logfixer-backups/external-20260524T084500Z/rollback-manifest.json
```
