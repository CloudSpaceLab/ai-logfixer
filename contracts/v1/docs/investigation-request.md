# InvestigationRequest

`InvestigationRequest` starts an investigation from an automatic detector, manual user action, or external integration.

Required fields include source metadata, service, symptom, time window, signal fingerprint, user-facing status, and external references.

Composable React UI components should use `display_status` and `user_message` to explain why the investigation started.
