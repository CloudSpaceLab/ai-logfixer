# Phase 1 Progress And Architecture Review

Review date: 2026-05-25

This review reconstructs the current AI LogFixer architecture from the codebase, open issues, and open PRs. It also records the next best engineering step after the live Goravel framework validation.

## Current GitHub State

Open PRs:

| PR | State | Mergeability | Role |
| --- | --- | --- | --- |
| #9 Add Runtime V2 config remediation | open | mergeable | Core contract-backed config-remediation loop: detect, diagnose, plan, patch, verify, receipt. |
| #10 Add guarded Laravel external-agent remediation | open | mergeable | Stacked on #9; adds Laravel production error classification and guarded external-agent remediation. |

Open issues:

| Issue | Current reading |
| --- | --- |
| #2 Master plan | Still the product north star: standalone AI LogFixer with API, CLI, UI, integrations, safety, and durable remediation. |
| #3 v1 contracts | Mostly implemented by the current contracts package, schemas, examples, and drift tests. Remaining work is TypeScript generation and closing the issue against the current artifact list. |
| #4 fixtures | Partially covered by Go test fixtures and the Goravel live validation runbook. Still needs stable standalone fixtures committed outside temp/generated app state. |
| #5 safety/redaction/status/versioning | Partially implemented in contracts and narrow executors. Still needs central policy enforcement, redaction pipeline, approval persistence, RBAC, and audit model. |
| #6 integration boundaries | Partially documented by `external_refs` and architecture docs. Still needs explicit API ownership rules, webhooks, and connector boundaries. |
| #11 real Goravel validation | Technically validated in this branch, with a GitHub issue comment recording the live result. Needs PR packaging before it can be closed. |

## Reconstructed Architecture

The repo has evolved into four layers:

```text
contracts/v1 + internal/contracts/v1
        |
        v
proof runners
  - cmd/ai-logfixer-v2
  - cmd/ai-logfixer-laravel
  - cmd/ai-logfixer-goravel
        |
        v
framework/domain adapters
  - internal/runtime/v2
  - internal/laravel
  - internal/frameworks/goravel
        |
        v
remediation executors
  - internal/agentfix
  - internal/remediation
```

The most important architectural correction from issue #11 is now proven: real framework failures require a framework adapter layer. The adapter must understand logs, route maps, handler ownership, source evidence, restart requirements, and patch risk. A generic "count 503s in a demo log" loop is not enough.

## What Is Now Proven

- Contract-backed workflow payloads exist for investigation, diagnosis, remediation plans, attempts, and receipts.
- The Runtime V2 config remediation loop works end to end.
- Laravel has a guarded path for production error-page detection, missing-class stubs, and external-agent staging.
- Goravel has a real-framework slice:
  - parses ANSI-normalized access logs
  - groups repeated route failures
  - maps `routes/web.go` to `UserController.Index`
  - collects handler source evidence
  - builds contract-valid source patch and rollback plans
  - applies a narrow approved panic-line patch
  - snapshots source before edit
  - restarts through an operator-supplied command
  - verifies route recovery
  - records attempt and receipt output
- A durable-store foundation slice has started:
  - central transition guards in `internal/domain`
  - repository, lease, audit, and outbox interfaces in `internal/store`
  - PostgreSQL repository implementation in `internal/store/postgres`
  - transaction-scoped workflow transition service in `internal/workflow`
  - first runner integration point in `internal/runtime/v2`, recording the remediation plan lifecycle when a workflow service is supplied
  - optional live PostgreSQL integration test gated by `AILOGFIXER_POSTGRES_DSN`
  - GitHub Actions CI job that runs the PostgreSQL integration test with a disposable Postgres service
  - reference PostgreSQL workflow schema in `db/migrations/postgres/0001_workflow_store.sql`

The live 2026-05-25 Goravel validation passed against a shallow-cloned real Goravel app after injecting the issue #11 panic fault.

## Biggest Gaps

1. Goravel is not routed through the durable workflow yet.
   Contracts are valid, the first PostgreSQL repository implementation exists, a workflow transition service now owns audit/outbox writes, CI is wired to prove migration/repository round trips against PostgreSQL, and the Runtime V2 runner has a first workflow-service hook. The real Goravel path still emits command output directly and should be the next runner moved behind `internal/workflow`.

2. Orchestration is still embedded in runners.
   `internal/runtime/v2`, `internal/laravel`, and `internal/frameworks/goravel` each build contracts directly. A workflow service should own state transitions and call adapters/executors as plugins.

3. Safety is distributed.
   Approval requirements, rollback details, redaction, command execution, and risk levels are present but not centralized. Production use needs one policy layer before writes, command execution, external-agent application, and evidence persistence.

4. Framework adapters are narrow by design, but not yet standardized.
   Goravel is cleaner than Laravel structurally. Laravel should eventually move from `internal/laravel` into `internal/frameworks/laravel` and share adapter/executor interfaces.

5. Restart/process management is external.
   The current Goravel CLI correctly requires operator-provided restart/verify commands. Production architecture needs supervised process targets, timeouts, logs, health probes, and rollback-aware restarts.

6. Fixtures are not yet stable product assets.
   The live Goravel app was validated under ignored `tmp/`. That is right for exploration, but closing issue #4 should add stable lightweight fixtures or scripted fixture generation.

7. No API/UI package yet.
   Contracts include UI-ready fields, but there is no backend API, event stream, or React package consuming those contracts.

## Target Architecture

The durable target remains:

```text
API / CLI / Webhooks
        |
        v
Workflow Orchestrator
        |
        +-- Signal Detector
        +-- Framework Adapter Registry
        +-- Evidence Collector
        +-- Diagnosis Builder
        +-- Remediation Planner
        +-- Approval Policy
        +-- Executor Registry
        +-- Monitor / Rollback Worker
        |
        v
Durable Store + Audit Log
```

Database posture:

- relational columns for identity, status, ownership, timestamps, risk, dedupe keys, and query paths
- JSON columns for contract payload snapshots and evidence excerpts
- append-only events for audit and UI timelines
- idempotency keys for request and execution dedupe
- leases with fencing tokens for workers
- artifact table for rollback snapshots, manifests, prompts, diffs, and receipts

The durable architecture details are in `docs/architecture/durable-workflow-architecture.md`.

## Recommended Next Step

Package the issue #11 follow-up and continue the durable workflow layer behind it.

Concrete sequence:

```text
1. Decide whether the Goravel adapter and durable-store foundation land in PR #10 or a new PR stacked on #9.
2. Merge or unblock PR #9 first because it is the baseline contract/config-remediation layer.
3. Keep PR #10 stacked for Laravel external-agent remediation unless the Goravel work becomes its own PR.
4. Start routing the Goravel runner through `internal/workflow` instead of direct contract-only command output.
5. Add API/webhook surfaces over the durable store after runner integration proves the service boundary.
```

The reason to keep DB work immediately behind issue #11 is simple: issue #11 changed the architecture. Now that the framework-adapter boundary is proven, the durable store can be implemented around the correct runtime shape instead of the earlier demo-only model.
