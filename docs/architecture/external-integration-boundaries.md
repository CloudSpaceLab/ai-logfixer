# External Integration Boundaries

AI LogFixer is the system of record for investigations, diagnoses, remediation plans, approvals, execution attempts, rollback outcomes, and receipts. External platforms integrate through public contracts and APIs; they do not mutate internal workflow state directly.

## Ownership Model

```text
External platform
  -> creates requests, displays state, provides authorized decisions

AI LogFixer
  -> owns state, policy, execution, monitoring, rollback, receipts
```

ControlOne, Slack, Jira, GitHub, CI/CD systems, SIEM tools, observability platforms, and direct CLI/API users are integration consumers. None of them are required internal dependencies.

## Allowed Integration Responsibilities

External systems may:

- create `InvestigationRequest` payloads
- fetch investigation, diagnosis, remediation, attempt, and receipt state
- receive webhooks or event stream notifications
- provide approval or denial decisions when authorized
- store their own references to AI LogFixer IDs
- embed contract-driven UI components
- link platform records through `external_refs`

External systems must not:

- bypass AI LogFixer safety policy
- force remediation execution without required approval
- write internal workflow rows directly
- depend on unexported Go structs as public API
- assume AI LogFixer is tied to one platform's tenant, audit, auth, or approval model
- use embedded UI components as a backdoor around APIs

## Public API Boundary

The public API should expose stable operations for:

- starting an investigation
- getting investigation status
- listing active clusters
- getting diagnosis results
- getting remediation plans
- approving remediation
- denying remediation
- cancelling remediation
- requesting rollback
- fetching receipts
- subscribing to webhook events

State-changing APIs must be idempotent where practical and must enforce the same policy checks as CLI and native UI paths.

## Event Boundary

Outbound integrations should receive integration-neutral event names:

- `investigation.requested`
- `investigation.linked`
- `investigation.queued`
- `investigation.rejected`
- `investigation.started`
- `investigation.completed`
- `diagnosis.completed`
- `remediation.plan_created`
- `remediation.awaiting_approval`
- `remediation.approved`
- `remediation.denied`
- `remediation.running`
- `remediation.succeeded`
- `remediation.failed`
- `remediation.rolled_back`
- `receipt.created`

Webhook delivery should use an outbox pattern so workflow commits and outbound notifications cannot drift silently.

## `external_refs`

Use `external_refs` for platform-specific references.

```text
system: github
type: issue
id: 123
url: https://github.com/example/repo/issues/123
metadata: { "repository": "example/repo" }
```

Examples:

- `system=controlone`, `type=knowledge_tree_node`
- `system=jira`, `type=ticket`
- `system=slack`, `type=thread`
- `system=github`, `type=issue`
- `system=observability`, `type=alert`

Knowledge graph references that AI LogFixer itself reasons over belong in `knowledge_refs`, not `external_refs`.

## Embedded UI Boundary

Composable React components should render public contract state and call public APIs for state changes.

Supported component surfaces can include:

- active investigations
- cluster and branch timeline
- evidence viewer
- diagnosis summary
- confidence and recommendation panels
- remediation preview
- approval panel
- execution timeline
- rollback plan viewer
- receipt viewer
- integration status panel

Embedding rules:

- components are contract-driven
- components can emit callbacks to the host app
- state-changing actions go through AI LogFixer APIs
- components do not execute remediation directly
- components do not bypass approval or safety checks
- components must hide raw evidence when redaction state is unsafe

## Approval Boundary

External platforms may submit approval decisions only when the integration identity has permission to do so.

```text
RemediationPlan
  -> ApprovalRequest
  -> external or native decision
  -> AI LogFixer validates identity, risk, policy, and state
  -> execution may proceed only if still allowed
```

If policy changed after the approval was issued, AI LogFixer must re-evaluate and may block execution.

## Version Boundary

Integrations consume versioned public contracts under `contracts/v1`. Public APIs and webhooks must declare the contract version they emit or accept. Internal package paths, database tables, and workflow implementation details are not integration contracts.
