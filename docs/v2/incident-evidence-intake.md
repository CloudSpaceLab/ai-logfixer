# Incident Evidence Intake

`internal/evidenceintake` is the Runtime V2 intake boundary for raw incident context. It normalizes operator- or adapter-supplied evidence into a small bundle that later resolvers can consume consistently.

The package is intentionally not a resolver. It does not infer root cause, patch files, run commands, or encode framework-specific answers.

## Inputs

An intake request can include:

- app root
- process metadata
- log samples
- probe results
- config snapshots
- package manifests
- permission and access state
- dependency state
- service state

Every request requires `AppRoot` and `CapturedAt`. Individual evidence entries validate their identifying fields before a bundle is returned.

## Output

`BuildBundle` returns a `Bundle` with:

- `SchemaVersion`: currently `incident-evidence-intake/v1`
- normalized app-root-relative paths
- one normalized `Item` per captured evidence source
- deterministic evidence IDs
- redacted excerpts for common secret patterns
- summary counts by evidence kind
- failing probes, unhealthy dependencies/services, and unwritable paths

The bundle and each item have `Validate` methods so resolvers can reject malformed intake before using it for diagnosis or remediation planning.

## Redaction

The v1 redactor covers common inline secret shapes:

- password, token, secret, API key, and app key assignments
- bearer and basic authorization values
- password segments in credentialed URLs

This is a safety baseline, not a complete data-loss-prevention system. Framework adapters should still avoid adding unnecessary raw secrets to the request.

## Example

```go
bundle, err := evidenceintake.BuildBundle(evidenceintake.Request{
    AppRoot:    "/srv/checkout",
    Source:     "runtime-v2",
    CapturedAt: now,
    Logs: []evidenceintake.LogSample{{
        Source: "storage/logs/app.log",
        Lines:  []string{"level=error route=/orders status=500 token=abc123"},
    }},
    Permissions: []evidenceintake.PermissionState{{
        Path:     "storage/logs",
        Owner:    "root",
        Group:    "root",
        Mode:     "0555",
        Readable: true,
        Writable: false,
    }},
})
if err != nil {
    return err
}
```

The resulting bundle keeps the route and permission context, redacts the token, and records `storage/logs` as an unwritable path for downstream analysis.
