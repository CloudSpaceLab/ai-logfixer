# v1 Contract Fixtures

These fixtures are small, standalone examples that prove the public v1 contracts can represent realistic AI LogFixer workflows without live credentials or customer data.

## Scenario Coverage

| Scenario | Fixture files |
| --- | --- |
| Automatic 503 investigation | `valid/auto-503-investigation-request.json`, `valid/cluster-go-api-linked-branches.json`, `valid/minimal-diagnosis-result.json`, `valid/remediation-plan-requires-approval.json` |
| Manual 403 linked to active 503 cluster | `valid/manual-403-linked-investigation-decision.json`, `valid/cluster-go-api-linked-branches.json` |
| Database connection exhaustion | `valid/db-connection-exhaustion-diagnosis.json`, `valid/remediation-attempt-failed-rolled-back.json`, `valid/receipt-remediation-rollback.json` |
| Vulnerable dependency remediation | `valid/vulnerable-dependency-remediation-plan.json` |
| Approved remediation success | `valid/approval-request-approved.json`, `valid/remediation-attempt-success.json`, `valid/receipt-approved-remediation-success.json` |
| Remediation failure with rollback | `valid/remediation-attempt-failed-rolled-back.json`, `valid/remediation-event-monitoring-failed.json`, `valid/receipt-remediation-rollback.json` |

## Fixture Rules

- Fixtures must validate against the JSON schemas in `contracts/v1/schemas`.
- Fixtures that have `contract_version` and `schema_url` must round-trip through the matching Go structs.
- Legacy root contracts that intentionally do not include `schema_url`, such as `Receipt`, are inferred by drift tests from their required fields.
- Raw excerpts must be short and safe. Do not add live secrets, personal data, private keys, full tokens, customer payloads, or production URLs.
- External systems must be linked through `external_refs`; product knowledge graph references belong in `knowledge_refs`.
