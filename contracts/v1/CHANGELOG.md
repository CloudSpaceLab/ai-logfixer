# ai-logfixer Contracts v1 Changelog

## v1

- Added `DiagnosisResult` as the top-level diagnosis payload.
- Added `EvidenceItem`, `RunbookRecommendation`, `PatchPlan`, `RollbackPlan`, and `Receipt` contracts.
- Added `contract_version` and `schema_url` requirements for top-level diagnosis payloads.
- Added safety, redaction, status, patch target, and rollback type enums.
- Added minimal valid and invalid examples for schema, validation, and drift checks.
- Added standalone investigation contracts for request intake and correlation decisions.
- Added remediation lifecycle contracts for plans, approval-ready state, execution attempts, and events.
- Added `external_refs` to avoid coupling contracts to any single external platform.
- Added UI-facing fields for composable React components, including display status, user messages, next actions, timeline events, and UI hints.
