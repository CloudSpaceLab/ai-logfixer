# ai-logfixer Contracts v1 Changelog

## v1

- Added `DiagnosisResult` as the top-level diagnosis payload.
- Added `EvidenceItem`, `RunbookRecommendation`, `PatchPlan`, `RollbackPlan`, and `Receipt` contracts.
- Added `contract_version` and `schema_url` requirements for top-level diagnosis payloads.
- Added safety, redaction, status, patch target, and rollback type enums.
- Added minimal valid and invalid examples for schema, validation, and drift checks.
