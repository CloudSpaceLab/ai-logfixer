# RemediationPlan

`RemediationPlan` describes the intended fix journey before execution.

It includes the fix preview, rollback plan, risk level, approval requirement, current status, user-facing message, next actions, timeline events, external references, and knowledge references.

Boundary rule:

`RemediationPlan` can explain and preview a fix. Execution still goes through AI LogFixer approval and safety checks.

Reference rule:

- `external_refs` links to outside systems.
- `knowledge_refs` links to graph nodes that explain what the fix changes or depends on.
