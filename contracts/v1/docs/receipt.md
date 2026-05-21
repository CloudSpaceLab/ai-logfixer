# Receipt

`Receipt` records what happened after an approved action is executed.

Required fields:

- `id`
- `diagnosis_id`
- `action_taken`
- `actor`
- `approver`
- `timestamp`
- `before_state`
- `after_state`
- `audit_refs`
- `knowledge_tree_refs`

Boundary rule:

ControlOne owns the authoritative audit trail. Receipts should link back to ControlOne audit records and Knowledge Tree nodes.
