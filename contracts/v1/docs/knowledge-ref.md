# KnowledgeRef

`KnowledgeRef` links an AI LogFixer contract payload to a knowledge graph node.

Use `knowledge_refs` for graph-aware context such as services, dependencies, incidents, runbooks, DBMS objects, framework versions, CVEs, and known failure patterns.

Do not use `knowledge_refs` for normal outside-system links. Use `external_refs` for GitHub issues, CI runs, Slack threads, SIEM alerts, ControlOne records, or other platform URLs.

AI LogFixer can resolve `knowledge_refs` against either:

- an AI LogFixer-owned knowledge graph
- a central shared knowledge graph used by AI LogFixer, ControlOne, and other products

Required fields:

- `graph_id`
- `node_id`
- `node_type`
- `relationship`
- `confidence`
- `source`

Guidance:

- `graph_id` identifies the graph namespace, not the external product.
- `node_id` identifies the specific graph node.
- `node_type` describes what the node represents, such as `service`, `database_table`, `framework`, `runbook`, or `cve`.
- `relationship` explains why this payload references the node, such as `affects`, `depends_on`, `investigates`, `changes`, or `mitigates`.
- `confidence` must be between `0` and `1`.
- `source` records which AI LogFixer component or integration produced the link.
