# InvestigationRequest

`InvestigationRequest` starts an investigation from an automatic detector, manual user action, or external integration.

Required fields include source metadata, service, symptom, time window, signal fingerprint, user-facing status, external references, and knowledge references.

Composable React UI components should use `display_status`, `user_message`, and `knowledge_refs` to explain why the investigation started and which graph context influenced it.
