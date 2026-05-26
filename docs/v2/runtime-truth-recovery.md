# Runtime V2 Truth Recovery

Runtime V2 makes AI LogFixer an error truth-recovery system first and a guarded remediation system second.

The core idea is simple: do not guess from friendly error pages or custom log messages. Recover the real exception, map it to source ownership, redact the evidence, then build a scoped fix bundle for explicit staged repair.

## Runtime Flow

```text
Opaque failure signal
        |
        v
Runtime V2 truth recovery
        |
        +-- detect repeated HTTP failures, friendly error pages, stack traces, or custom log messages
        +-- resolve full stack traces directly to source ownership when available
        +-- detect custom catch/recover/error-handler suppression sites when traces are hidden
        +-- plan diagnostic reveal steps only for staging/local/canary contexts
        +-- redact secrets before any fix-bundle handoff
        +-- create InvestigationRequest, DiagnosisResult, RemediationPlan, RemediationAttempt, and Receipt
        +-- apply only conservative allowlisted remediators
        +-- escalate safely when the real error or safe patch target is not known
```

## Current Runtime V2 Capabilities

Included:

- conservative JSON config remediation with explicit route/status matching
- explicit JSON key-path patching, including nested keys such as `dependencies.payment_url`
- config backup before patching and restore on failed verification
- blocked/escalated contracts when a config patch descriptor is missing or unsupported
- stack trace parsing and source owner resolution
- suppression-site detection for custom catch/recover/error-response paths
- staged reveal planning that blocks automatic production suppression changes
- evidence redaction before fix-bundle handoff
- scoped fix-bundle construction for explicit opencode delegation
- Goravel route-to-handler mapping with handler-scoped single-panic source patching
- Loghub-style Apache/OpenStack corpus grouping that escalates when no source owner exists

Not included yet:

- automatic production disabling of error suppression
- implicit opencode execution from Runtime V2
- broad production patch execution beyond conservative allowlisted config/source remediators
- long-running daemon orchestration
- React UI

## Command

The Runtime V2 config-remediation runner is:

```bash
go run ./cmd/ai-logfixer-v2 \
  -mode config \
  -service checkout-api \
  -log ./tmp/checkout.log \
  -config ./tmp/checkout.json \
  -route /checkout \
  -status 503 \
  -config-key dependencies.payment_url \
  -replacement-value http://127.0.0.1:8090/upstream/payment \
  -verify-url http://127.0.0.1:8090/checkout \
  -expected-status 200 \
  -threshold 3
```

The legacy `/orders` demo still works through defaults, but it is now only a fixture for the Runtime V2 path.

When a full stack trace is already available, Runtime V2 can resolve source ownership and emit a redacted fix bundle:

```bash
go run ./cmd/ai-logfixer-v2 \
  -mode truth \
  -service payments-api \
  -framework go \
  -environment staging \
  -message "payment failed" \
  -stack-trace-file ./tmp/payment-stacktrace.txt
```

When only a custom error message is visible, Runtime V2 can inspect source files for catch/recover/error-handler suppression sites and produce a staged reveal plan:

```bash
go run ./cmd/ai-logfixer-v2 \
  -mode truth \
  -service checkout-api \
  -framework go \
  -environment staging \
  -message "checkout failed" \
  -source-file ./app/http/controllers/checkout_controller.go
```

## Safety Model

Runtime V2 may plan diagnostic reveal steps, but it must not automatically expose raw production stack traces or disable production error suppression. Production reveal attempts become blocked/escalated remediation records with manual-review next actions.

Framework adapters should help Runtime V2 find the truth reliably:

- stack traces map directly to source files/functions
- custom log messages map to nearby catch/recover/error-handler code
- framework debug switches are treated as diagnostic reveal controls
- opencode receives only scoped, redacted fix bundles

## Verification

Config remediation:

```bash
go test ./internal/runtime/v2 -count=1
```

Truth recovery:

```bash
go test ./internal/truth -count=1
```

Framework and corpus safety:

```bash
go test ./cmd/ai-logfixer-goravel ./internal/frameworks/goravel ./internal/remediation ./internal/signals/loghub -count=1
```
