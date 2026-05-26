# Durable Workflow Architecture

This document records the target architecture that should sit behind the current Phase 1 contracts and CLI proof-of-concept runners.

The current code proves useful slices:

- v1 public contracts, schemas, examples, and drift checks
- a deterministic 503 config-remediation prototype
- a Laravel production error-page runner with guarded external-agent delegation
- a first Goravel framework adapter boundary for route failure analysis and narrow source patch execution
- a reusable source-file remediation executor with snapshot backup and rollback-on-failed-verification

Those slices should now converge into one durable workflow system instead of growing as separate command-specific flows.

## Design Goals

- Keep JSON Schema v1 as the public contract boundary.
- Keep Go structs as implementation types, not the database model.
- Make every investigation, approval, execution, rollback, and receipt durable.
- Treat remediation as a state machine with auditable transitions.
- Support multiple frameworks through adapters rather than per-framework monoliths.
- Allow API, CLI, webhooks, and future React UI to read the same workflow state.
- Make retry, idempotency, and recovery explicit.

## Target Runtime

```text
+-------------------+
| API / CLI /      |
| Log Agents /     |
| Webhooks         |
+---------+---------+
          |
          v
+-------------------+       +---------------------+
| Signal Ingestion  | ----> | Fingerprint Builder |
+---------+---------+       +----------+----------+
          |                            |
          v                            v
+--------------------------------------------------+
| Investigation Orchestrator                       |
| - deduplicate                                   |
| - link related branches                         |
| - enforce capacity                              |
| - schedule workers                              |
+----------------------+---------------------------+
                       |
                       v
+--------------------------------------------------+
| Investigation Workers                            |
| - evidence collection                            |
| - framework adapters                             |
| - diagnosis                                      |
| - patch/rollback preview                         |
+----------------------+---------------------------+
                       |
                       v
+--------------------------------------------------+
| Policy + Approval Gate                           |
| - risk scoring                                   |
| - approval requirement                           |
| - execution permission                           |
+----------------------+---------------------------+
                       |
                       v
+--------------------------------------------------+
| Remediation Runtime                              |
| - stage                                          |
| - validate                                       |
| - apply                                          |
| - restart/reload                                 |
| - verify/monitor                                 |
| - rollback/escalate                              |
+----------------------+---------------------------+
                       |
                       v
+-------------------+       +----------------------+
| Receipt + Audit   | ----> | Webhook Outbox       |
+-------------------+       +----------------------+
```

## Package Shape

The runtime should move toward these packages:

```text
internal/domain
  Contract-independent workflow entities and state transitions.

internal/store
  Durable repositories, transactions, leases, migrations, and outbox.

internal/workflow
  Investigation orchestration, scheduling, capacity checks, and retries.

internal/signals
  Log parsers, metric/trace adapters, fingerprinting, and time-window aggregation.

internal/frameworks/<name>
  Framework-specific route discovery, log parsing, evidence collection, and fix previews.

internal/evidence
  Evidence normalization, artifact storage, source excerpts, and redaction.

internal/policy
  Risk scoring, approval requirement decisions, and execution eligibility.

internal/remediation
  Patch staging, validation, execution, restart/reload, verification, and rollback.

internal/agent
  External-agent prompt packaging, sandboxing, diff review, and validation policy.

internal/api
  Public API handlers and webhook subscriptions.
```

The existing `internal/runtime/v2`, `internal/laravel`, and `internal/agentfix` packages should become callers or adapters into these layers over time. They should not become the long-term product core.

The first durable foundation slice now exists:

- `internal/domain` contains central transition guards for investigation, remediation, and approval state.
- `internal/store` defines repository interfaces for contract records, audit events, workflow leases, and outbox events.
- `internal/store/postgres` implements the first transaction-scoped PostgreSQL repositories, optimistic status updates, leases, audit append, and outbox claiming.
- `internal/workflow` provides the first service layer that owns transitions and appends audit/outbox records atomically.
- `internal/runtime/v2` has the first runner integration point: when a workflow service is supplied, Runtime V2 records remediation plan lifecycle transitions through `internal/workflow`.
- `db/migrations/postgres/0001_workflow_store.sql` is the reference PostgreSQL schema for the tables below.

## Database Model

Use a relational database as the product system of record. PostgreSQL, SQL Server, or Oracle are all suitable. The important design point is to combine typed relational columns for workflow queries with JSON payload columns for versioned public contract payloads.

Suggested tables:

```text
tenants
environments
services
source_adapters

signal_events
signal_fingerprints

investigation_requests
investigation_clusters
investigation_branches
investigation_decisions

evidence_items
diagnosis_results
runbook_recommendations
patch_plans
rollback_plans

approval_requests
approval_decisions

remediation_plans
remediation_attempts
remediation_events
rollback_attempts
receipts

external_refs
knowledge_refs

agent_runs
artifacts
audit_events
workflow_leases
outbox_events
```

### Relational Plus JSON

Each top-level contract record should store both:

- typed columns for filtering, joins, capacity, and dashboards
- the full validated v1 JSON payload for API/UI/webhook rendering

Example `diagnosis_results` shape:

```text
id
tenant_id
service_id
branch_id
contract_version
status
safety_classification
confidence
created_at
updated_at
payload_json
```

Example `remediation_attempts` shape:

```text
id
tenant_id
remediation_plan_id
approval_request_id
status
execution_started_at
execution_finished_at
rollback_attempt_id
created_at
updated_at
payload_json
```

## Indexing And Partitioning

Minimum indexes:

```text
signal_events(tenant_id, service_id, observed_at)
signal_fingerprints(tenant_id, fingerprint_hash, status)
investigation_requests(tenant_id, service_id, created_at)
investigation_clusters(tenant_id, status, updated_at)
investigation_branches(tenant_id, cluster_id, status)
diagnosis_results(tenant_id, branch_id, status)
remediation_plans(tenant_id, diagnosis_result_id, status)
approval_requests(tenant_id, status, expires_at)
remediation_attempts(tenant_id, remediation_plan_id, status)
outbox_events(tenant_id, status, next_attempt_at)
workflow_leases(resource_type, resource_id, expires_at)
```

High-volume tables such as `signal_events`, `evidence_items`, `remediation_events`, and `audit_events` should be partitioned by time and tenant when volume requires it.

Large log excerpts, diffs, patch bundles, and agent work products should be stored in `artifacts` by content hash or external object reference. Contract payloads should reference artifacts instead of embedding unbounded content.

## Database Portability Notes

The checked-in migration is PostgreSQL-first because it gives the repo a concrete executable reference. The logical model is intentionally portable:

- PostgreSQL: `uuid`, `jsonb`, partial indexes, and `timestamptz`.
- SQL Server: `uniqueidentifier`, `nvarchar(max)` with `ISJSON` checks, filtered indexes, `datetimeoffset`, and persisted computed columns for high-traffic JSON paths.
- Oracle: `RAW(16)` or `VARCHAR2(36)` identifiers, native `JSON` or `CLOB/BLOB` with `IS JSON`, function-based indexes for selected JSON paths, and `TIMESTAMP WITH TIME ZONE`.

Keep vendor-specific DDL in separate migration directories. The Go repository interfaces should stay vendor-neutral; only concrete store implementations should know the dialect.

## PostgreSQL Integration Test

The PostgreSQL store has an optional live integration test. It is skipped unless `AILOGFIXER_POSTGRES_DSN` is set. The test creates a private schema, applies `db/migrations/postgres/0001_workflow_store.sql`, seeds tenant/environment/service rows, creates core workflow records, moves a remediation plan through `internal/workflow`, verifies audit/outbox output, and exercises workflow lease acquire/renew/release.

```bash
AILOGFIXER_POSTGRES_DSN='postgres://user:pass@127.0.0.1:5432/ai_logfixer_test?sslmode=disable' \
  go test ./internal/store/postgres -run TestPostgresStoreIntegration -count=1
```

`.github/workflows/ci.yml` runs this integration test against a disposable PostgreSQL service on pull requests, alongside the normal `go test ./...` and `go vet ./...` checks.

## Workflow State

Workflow state should be enforced centrally. Builders can create payloads, but only the workflow layer should move records through states.

Investigation:

```text
requested -> fingerprinted -> linked|queued|running -> needs_more_data|completed|failed|rejected
```

Remediation:

```text
planning -> awaiting_approval -> approved|denied
approved -> running -> monitoring -> succeeded|failed
failed -> rolled_back|escalated
```

Every transition should append an `audit_events` row and, when user-visible, a `remediation_events` or investigation timeline event.

## Idempotency And Leases

Every external request should carry or receive an idempotency key. For automatic signals, derive the key from:

```text
tenant_id + service_id + source + route/error + status_class + time_bucket + deploy_version
```

Workers should acquire leases before changing workflow state or target systems:

```text
workflow_leases
- resource_type
- resource_id
- owner_id
- expires_at
- fencing_token
```

Use fencing tokens in write paths so an expired worker cannot commit stale results after another worker has taken over.

## Framework Adapter Contract

Framework adapters should be narrow. They should not own orchestration, approval, or durable state.

Adapter responsibilities:

```text
- parse framework logs
- normalize console log transport details such as ANSI color escapes
- discover routes
- map route to handler/controller
- collect framework-specific evidence
- propose patch and rollback previews
- declare restart/reload requirements
```

The first concrete adapters are:

```text
internal/frameworks/goravel
cmd/ai-logfixer-goravel -> temporary CLI surface for the Goravel adapter
internal/laravel     -> later split into internal/frameworks/laravel
```

## Remediation Runtime

Remediation should run through an explicit executor interface:

```text
type Executor interface {
    Stage(ctx, plan) (StagedChange, error)
    Validate(ctx, staged) (ValidationResult, error)
    Apply(ctx, staged) (ApplyResult, error)
    Restart(ctx, target) (RestartResult, error)
    Verify(ctx, target) (VerifyResult, error)
    Rollback(ctx, attempt) (RollbackResult, error)
}
```

Source-file patches, config patches, DB migrations, dependency upgrades, runtime-setting changes, and external-agent patches should be different executors behind the same remediation workflow.

## Security Baseline

Before production use, add:

- tenant isolation on every table and query
- actor identity on every approval and execution
- RBAC/capability checks before approval and execution
- central secret redaction before evidence is persisted
- command and filesystem allowlists for external-agent validation
- signed rollback manifests or manifest hashes stored in the DB
- immutable audit events for every policy and execution decision

## Migration Path

1. Keep PR #9 as the core-loop baseline.
2. Keep Laravel remediation capability, but split it behind framework and remediation interfaces.
3. Build the Goravel adapter and CLI slice from issue #11 before claiming Phase 1 framework validation.
4. Add the store layer and migrations.
5. Move the Goravel runner behind `internal/workflow`.
6. Add API/webhook surfaces over the durable store.
7. Add React components after the backend state model is stable enough to render honestly.

The current Goravel panic-patch execution is intentionally narrow. It now proves the route-to-handler-to-source-patch path through a dry-run/approved-apply CLI against a live Goravel clone. A first durable-store slice exists, but production restart management, process supervision, approval persistence, repository implementations, and workflow workers still belong in later store/workflow/remediation layers.

The manual real-framework validation checklist lives in [Goravel real-framework validation](../runbooks/goravel-real-framework-validation.md).
