# Goravel Real-Framework Validation

This runbook turns issue #11 into a reproducible Phase 1 validation exercise. It is intentionally operator-driven: AI LogFixer can analyze and execute the narrow source patch, but the real app process is still owned by the local test environment.

## Goal

Prove this flow against a real Goravel app:

```text
repeated framework route failure
        |
        v
access log grouping
        |
        v
route-to-controller mapping
        |
        v
source evidence + PatchPlan
        |
        v
approved source patch + backup
        |
        v
restart/reload callback
        |
        v
route verification + Receipt
```

## Setup

Create a local Goravel app with the official installer:

```bash
go install github.com/goravel/installer/goravel@latest
goravel new real-goravel-app
```

Confirm the app has the route shape observed in issue #11:

```go
userController := controllers.NewUserController()
facades.Route().Get("/users", userController.Index)
```

The first adapter supports that direct controller-variable style and maps it to:

```text
app/http/controllers/user_controller.go
```

## Introduce The Fault

Insert a route-local runtime fault in the controller:

```go
func (r *UserController) Index(ctx http.Context) http.Response {
    panic("random framework test fault: user controller crashed before response")

    return ctx.Response().Success().Json(http.Json{
        "Hello": "Goravel",
    })
}
```

Start or restart the Goravel app with the local workflow you normally use. Capture stdout/stderr to a file because Goravel access lines may be emitted to process output while `storage/logs` contains application error stack traces.

Example PowerShell startup:

```powershell
go build -o real-goravel-app.exe .
Start-Process `
  -FilePath (Join-Path (Get-Location) "real-goravel-app.exe") `
  -WorkingDirectory (Get-Location) `
  -RedirectStandardOutput app.stdout.log `
  -RedirectStandardError app.stderr.log `
  -WindowStyle Hidden
```

Then generate repeated failures:

```bash
for i in 1 2 3 4 5; do
  curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:3000/users
done
```

The expected signal is repeated `500` responses in the Goravel access log.

## Dry Run

From the AI LogFixer repo, run:

```bash
go run ./cmd/ai-logfixer-goravel \
  -target /tmp/ai-logfixer-framework-test/real-goravel-app \
  -access-log app.stdout.log \
  -service real-goravel-app \
  -threshold 3
```

Expected output:

```text
investigation_request
diagnosis
remediation_plan
failure
route
```

The remediation plan should be awaiting approval and should identify a source-file patch against `UserController.Index`.

## Approved Apply

Run apply mode only after reviewing the diff preview:

```bash
go run ./cmd/ai-logfixer-goravel \
  -target /tmp/ai-logfixer-framework-test/real-goravel-app \
  -access-log app.stdout.log \
  -service real-goravel-app \
  -apply=true \
  -approve-source-patch=true \
  -restart-command "<restart or reload command for the local Goravel app>" \
  -verify-command "curl -fsS http://127.0.0.1:3000/users"
```

Expected output adds:

```text
attempt
receipt
source_file
```

If restart or verification fails, AI LogFixer restores the source-file snapshot and returns a rolled-back attempt.

## Current Limits

- The adapter supports the direct `controllers.NewXController()` plus `facades.Route().Get(..., controller.Method)` route shape first.
- The execution path only removes an identified `panic(...)` line.
- The CLI does not supervise the Goravel process; restart/reload remains an external command.
- Durable approvals, leases, audit tables, and workflow state still belong in the planned database-backed runtime.

## 2026-05-25 Live Validation Notes

On Windows with Go `1.25.2`, the official installer prompted for project type and was not suitable for noninteractive smoke validation. A direct shallow clone of `https://github.com/goravel/goravel.git` produced the same route shape and built successfully after copying `.env.example` to `.env` and running:

```bash
go run . artisan key:generate
```

Observed live results:

```text
GET /users -> 500 repeated after panic injection
cmd/ai-logfixer-goravel dry run -> detected GET /users status_class=500 and mapped UserController.Index
approved apply -> removed panic line, snapshotted source, restarted app, verified GET /users -> 200
receipt outcome -> succeeded
```

Implementation note: live stdout included ANSI color sequences before the first `[HTTP]` line, so the adapter strips ANSI sequences before parsing access logs and evidence excerpts.
