# Runtime V2 Environment Variable Diagnostics

Environment variable diagnostics detects missing required variables from an explicit policy.

The MVP is intentionally conservative:

- secret variables are never generated or written
- missing secrets produce blocked/escalated contracts with manual next actions
- non-secret defaults can be written only when the policy explicitly allows it
- writes require an explicit env file path
- apply mode writes a rollback manifest before changing the env file

Supported package:

```go
internal/runtime/envvars
```

The resolver is designed to be called by future product commands and resolver orchestration. It does not infer secrets, inspect secret managers, or mutate a running process environment.
