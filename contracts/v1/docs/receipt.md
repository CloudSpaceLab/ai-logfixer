# Receipt

`Receipt` records what happened after an approved action is executed.

Required fields:

- `id`
- `diagnosis_id`
- `remediation_plan_id`
- `remediation_attempt_id`
- `action_taken`
- `actor`
- `approver`
- `timestamp`
- `before_state`
- `after_state`
- `outcome`
- `summary`
- `timeline_events`
- `external_refs`

Boundary rule:

AI LogFixer owns the product receipt. External systems can be linked through `external_refs`.
