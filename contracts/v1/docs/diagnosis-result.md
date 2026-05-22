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
- `display_status`
- `user_message`
- `next_actions`
- `timeline_events`
- `external_refs`
- `knowledge_refs`
- `created_at`

Boundary rule:

`DiagnosisResult` describes what was found. It must not imply that any UI component can execute production changes directly.

Composable React UI components should render `display_status`, `user_message`, `next_actions`, `timeline_events`, `external_refs`, and `knowledge_refs` from this contract.

Reference rule:

- `external_refs` links to outside systems.
- `knowledge_refs` links to knowledge graph nodes in either an AI LogFixer-owned graph or a central shared graph.
