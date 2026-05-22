# ai-logfixer

`ai-logfixer` provides contracts and future engine components for standalone AI-assisted investigation, diagnosis, and guarded remediation.

The product should also expose a modular, composable React UI so the native AI LogFixer app and external integrations can render the same investigation and remediation workflows from the public contracts.

Contracts separate outside-system links from knowledge graph links:

- `external_refs` points to external systems such as GitHub, CI/CD, SIEM, Slack, or ControlOne records.
- `knowledge_refs` points to graph nodes in either an AI LogFixer-owned knowledge graph or a central shared graph used across products.

## Phase 1 contracts

The v1 contracts live in `contracts/v1`.

```text
------------------+
| JSON Schema v1  |
+--------+---------+
         |
         +--> Go structs
         +--> examples
         +--> drift checks
         +--> future TypeScript types
```

Run the full test suite:

```bash
go test ./...
```
