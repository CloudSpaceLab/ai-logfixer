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

## Phase 1 core-tech MVP

Phase 1 now includes a minimal end-to-end remediation loop:

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
AI LogFixer backs up the config, patches the upstream URL, verifies /orders, and records a Receipt
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

Run the MVP fixer:

```bash
go run ./cmd/ai-logfixer-mvp \
  -base-url http://127.0.0.1:8090 \
  -config ./tmp/demo-goravel-app.json \
  -log ./tmp/demo-goravel-app.log \
  -healthy-upstream http://127.0.0.1:8090/upstream/orders \
  -threshold 3
```

Verify the app recovered:

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8090/orders
```

Expected result:

```text
200
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
