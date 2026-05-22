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
- `ui_hints`
- `external_refs`
- `knowledge_refs`

Safety note:

Raw excerpts should be small and safe. Evidence with `unknown` or `failed` redaction must not expose sensitive values.

Reference rule:

- `external_refs` links evidence to outside systems.
- `knowledge_refs` links evidence to graph nodes such as services, traces, runbooks, DBMS objects, framework versions, CVEs, or known failure patterns.
