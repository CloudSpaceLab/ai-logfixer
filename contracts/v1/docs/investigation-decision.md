# InvestigationDecision

`InvestigationDecision` records how AI LogFixer routed an incoming investigation request.

Supported decisions:

- `start_new`
- `attach_duplicate`
- `link_related`
- `queue`
- `reject`

Rejected decisions require an explanation. Queued decisions require capacity context.

Composable React UI components should render the decision, explanation, capacity snapshot, and next actions.
