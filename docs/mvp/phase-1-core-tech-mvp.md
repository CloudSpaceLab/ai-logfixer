# Phase 1 Core-Tech MVP

This MVP proves the smallest complete AI LogFixer loop before deeper UI and integration work.

The demo app intentionally starts with a bad upstream URL. Its `/orders` route calls that upstream, returns `503` when the upstream is unavailable, and writes structured log lines. AI LogFixer reads those logs, confirms the 503 threshold, traces the bad config, builds contract payloads, backs up the config, patches the upstream URL, verifies recovery, and records a receipt.

## Runtime Flow

```text
Demo app config
  upstream_url = http://127.0.0.1:1/orders
        |
        v
GET /orders
        |
        v
503 responses + app log lines
        |
        v
AI LogFixer MVP runner
        |
        +-- detect repeated 503s
        +-- read app config
        +-- create InvestigationRequest
        +-- create DiagnosisResult
        +-- create RemediationPlan
        +-- save config backup
        +-- patch upstream_url
        +-- verify GET /orders returns 200
        +-- create RemediationAttempt
        +-- create Receipt
```

## Scope

Included:

- deterministic local demo app
- repeated 503 detection from logs
- config tracing
- low-risk config patching
- config backup before patching
- recovery verification through HTTP
- contract-backed investigation, diagnosis, remediation, attempt, and receipt records
- end-to-end Go test

Not included yet:

- real Goravel framework bootstrapping
- Laravel/PHP runtime
- AI model calls
- DB migrations
- multi-service orchestration
- production-safe patch execution
- long-running daemon mode
- React UI

## Why This Shape

This intentionally keeps the first MVP small. The goal is to prove the core loop works before investing in framework-specific adapters, UI, or external integrations.

The current demo app mirrors the relevant Goravel/Laravel failure mode: application config points to a bad dependency, the app emits repeated 503s, and a safe config patch can restore service. A future phase can replace the demo app with a full Goravel or Laravel scaffold after this core loop is stable.

## Acceptance Test

The primary test is:

```bash
go test ./internal/mvp -run TestMVPDetectsRepeated503AndAppliesConfigFix -count=1
```

It verifies:

- the demo app starts broken
- `/orders` returns repeated `503`
- AI LogFixer creates an automatic investigation
- AI LogFixer diagnoses a bad upstream config
- AI LogFixer builds a remediation plan
- AI LogFixer backs up and patches the config
- AI LogFixer verifies `/orders` returns `200`
- AI LogFixer returns a successful remediation attempt and receipt
