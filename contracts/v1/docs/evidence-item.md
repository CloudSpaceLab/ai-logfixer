# EvidenceItem

`EvidenceItem` is one piece of supporting data used to explain a diagnosis.

Supported evidence types:

- `log`
- `trace`
- `metric`
- `db`
- `config`
- `cve`

Required fields:

- `id`
- `type`
- `source`
- `timestamp`
- `title`
- `summary`
- `raw_excerpt`
- `redaction_state`
- `related_ids`

Safety note:

Raw excerpts should be small and safe. Evidence with `unknown` or `failed` redaction must not expose sensitive values.
