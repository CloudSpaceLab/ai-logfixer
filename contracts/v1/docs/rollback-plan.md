# RollbackPlan

`RollbackPlan` describes how to undo or recover from a proposed change.

Supported rollback types:

- `snapshot`
- `reverse_patch`
- `migration_down`
- `restore_config`
- `manual_only`
- `unavailable`

Required fields:

- `id`
- `rollback_type`
- `snapshot_refs`
- `restore_steps`
- `limitations`
- `risk_level`
- `requires_manual_review`

Safety note:

If rollback is unavailable, limitations must explain why and how operators should reason about recovery.
