# Runtime Safety Policy

AI LogFixer owns remediation safety even when an investigation starts from an external system. External platforms can request work and supply approval decisions when authorized, but execution must pass through AI LogFixer's policy gates.

## Policy Boundary

Every write path must pass through the same sequence:

```text
candidate fix
  -> evidence redaction check
  -> fix preview
  -> rollback plan
  -> risk classification
  -> approval decision
  -> guarded execution
  -> monitoring
  -> receipt or rollback/escalation
```

The policy layer applies before config writes, source edits, database changes, dependency changes, runtime setting changes, and external-agent handoff.

## Safety Classes

| Class | Default behavior |
| --- | --- |
| `read_only` | May run without approval if it cannot mutate target state. |
| `low_risk` | May auto-apply only when policy explicitly allowlists the operation and rollback is clear. |
| `medium_risk` | Requires approval by default. |
| `high_risk` | Requires stronger approval, clear rollback or explicit rollback limitation, and monitoring. |
| `critical_risk` | Requires explicit human approval and should normally be staged, not automatic. |
| `blocked` | Must not execute. Produce a blocked or escalated plan instead. |

## Redaction Rules

Evidence must have a redaction state before display, persistence, or external-agent handoff.

| State | Meaning |
| --- | --- |
| `redacted` | Sensitive content was removed or masked. |
| `not_needed` | The excerpt was reviewed and does not contain sensitive content. |
| `unknown` | Redaction has not been proven. Raw display and handoff are unsafe. |
| `failed` | Redaction failed. Raw display and handoff are blocked. |

Raw excerpts must stay small and must not include passwords, API keys, access tokens, session cookies, private keys, database URLs with credentials, customer personal data, or full production payloads.

## Approval Rules

- No remediation attempt may run without a remediation plan.
- Risky remediation may not run without an approval request and authorized approval.
- External approval decisions are inputs to AI LogFixer policy; they do not bypass policy.
- Denied, expired, blocked, or missing approval stops execution.
- Approval records must include the risk level, reason, requester, approver when decided, and user-facing message.

## Rollback Rules

- A write plan must include a rollback plan or an explicit rollback limitation.
- `unavailable` rollback requires limitations that explain why rollback is unsafe or impossible.
- Failed monitoring must trigger rollback when rollback is available and policy allows it.
- If rollback is unsafe, unavailable, or fails, the attempt must be escalated with a receipt or failure record.

## Runtime Truth-Recovery Rules

Runtime V2 may plan diagnostic reveal steps when framework suppression hides the real error, but reveal execution must be staged, explicit, redacted, and reversible.

- Do not disable production error suppression automatically.
- Prefer stack traces, framework logs, route maps, and handler-scoped source evidence before reveal steps.
- Reveal plans must identify the suppression site, environment, expected evidence, and rollback step.
- Unsafe reveal requests produce blocked or escalated contracts.

## Status Semantics

Investigation states describe discovery and orchestration: `requested`, `fingerprinted`, `linked`, `queued`, `running`, `needs_more_data`, `completed`, `failed`, `rejected`.

Diagnosis states describe confidence in the explanation: `pending`, `complete`, `failed`, `needs_more_data`, `unsupported_source`, `blocked_by_safety`.

Remediation states describe the fix lifecycle: `pending`, `planning`, `awaiting_approval`, `approved`, `denied`, `running`, `monitoring`, `succeeded`, `failed`, `rolled_back`, `escalated`.

UI, CLI, API, and webhook surfaces must render these states honestly. They must not imply a fix has been applied until execution and monitoring complete.

## Versioning Rules

- Public contracts remain under `contracts/v1` until a schema-breaking change is required.
- Adding optional fields can remain v1.
- Removing required fields, renaming required fields, or changing the meaning of existing fields requires a new major contract version.
- Unsupported contract versions must fail clearly at API, CLI, UI, and integration boundaries.

## Capacity Controls

Default policy should include:

- global active investigation limit
- per-service active investigation limit
- related branch limit per cluster
- active remediation limit
- per-service remediation limit
- queue limit
- cooldown window for repeated remediation attempts

When capacity is full, AI LogFixer should attach duplicates, link related branches, queue, or reject with a user-facing explanation.
