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

## Phase 1 core-tech MVP

Phase 1 now includes a minimal end-to-end remediation loop:

```text
broken demo app config
        |
        v
repeated /orders 503 responses
        |
        v
AI LogFixer detects the threshold
        |
        v
AI LogFixer traces logs + config
        |
        v
AI LogFixer writes DiagnosisResult + RemediationPlan
        |
        v
AI LogFixer backs up the config, patches the upstream URL, verifies /orders, and records a Receipt
```

Run the demo app in a broken state:

```bash
go run ./cmd/demo-goravel-app \
  -addr 127.0.0.1:8090 \
  -config ./tmp/demo-goravel-app.json \
  -log ./tmp/demo-goravel-app.log \
  -init-broken=true
```

In another shell, generate repeated 503s:

```bash
for i in 1 2 3 4 5; do
  curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8090/orders
done
```

Run the MVP fixer:

```bash
go run ./cmd/ai-logfixer-mvp \
  -base-url http://127.0.0.1:8090 \
  -config ./tmp/demo-goravel-app.json \
  -log ./tmp/demo-goravel-app.log \
  -healthy-upstream http://127.0.0.1:8090/upstream/orders \
  -threshold 3
```

Verify the app recovered:

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8090/orders
```

Expected result:

```text
200
```
