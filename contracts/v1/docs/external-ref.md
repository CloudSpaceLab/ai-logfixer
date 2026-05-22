# ExternalRef

`ExternalRef` links AI LogFixer records to external systems without coupling the product to those systems.

Examples include:

- ControlOne records
- Jira tickets
- Slack threads
- GitHub issues
- CI/CD runs
- SIEM alerts

Required fields:

- `system`
- `type`
- `id`
- `url`
- `metadata`

Use `KnowledgeRef` for knowledge graph node links. `ExternalRef` should remain a platform or system reference.
