# PatchPlan

`PatchPlan` previews a possible change before anything is executed.

Supported target types:

- `file`
- `db_schema`
- `config`
- `dependency`
- `runtime_setting`

Required fields:

- `id`
- `target_type`
- `target_refs`
- `diff_preview`
- `risk_level`
- `requires_approval`
- `blocked_reasons`

Boundary rule:

`PatchPlan` is preview-only. AI LogFixer owns approval checks, guarded execution, rollback handling, and receipts.
