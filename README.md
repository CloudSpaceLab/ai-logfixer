# ai-logfixer

`ai-logfixer` provides contracts and future engine components for AI-assisted log diagnosis and typed remediation planning.

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
