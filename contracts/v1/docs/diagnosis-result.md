# DiagnosisResult

`DiagnosisResult` is the top-level v1 payload produced by `ai-logfixer`.

It describes the suspected issue, confidence, affected services, evidence, recommendations, patch preview, rollback preview, and safety classification.

Required fields:

- `id`
- `contract_version`
- `schema_url`
- `status`
- `summary`
- `confidence`
- `suspected_root_cause`
- `affected_services`
- `evidence_items`
- `recommendations`
- `patch_plan`
- `rollback_plan`
- `safety_classification`
- `created_at`

Boundary rule:

`DiagnosisResult` describes what was found. It must not imply that `ai-logfixer-ui` can execute production changes.
