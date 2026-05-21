# RunbookRecommendation

`RunbookRecommendation` describes the next action `ai-logfixer` recommends after a diagnosis.

Required fields:

- `id`
- `title`
- `reason`
- `confidence`
- `steps`
- `required_permissions`
- `estimated_risk`
- `requires_approval`

Boundary rule:

Recommendations suggest actions. ControlOne decides whether the user may approve or execute them.
