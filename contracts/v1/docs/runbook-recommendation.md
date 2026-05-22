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

Recommendations suggest actions. AI LogFixer decides whether the action needs approval, and external platforms may submit approval decisions only through supported APIs.
