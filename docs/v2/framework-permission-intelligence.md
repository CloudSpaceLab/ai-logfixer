# Runtime V2 Framework Permission Intelligence

Runtime V2 permissions mode repairs framework-owned writable runtime paths from an explicit local policy. It is intended to turn permission drift into a bounded product capability instead of a generic manual `chmod` recommendation.

## Current Capability

The first supported framework policy is Laravel. Auto-detection requires both:

- `artisan`
- `composer.json`

The Laravel policy checks these runtime directories:

- `storage`
- `storage/logs`
- `storage/framework/cache`
- `storage/framework/sessions`
- `storage/framework/views`
- `bootstrap/cache`

Each path must stay inside the app root, must be a directory, and must match mode `0775`. Runtime V2 also records UID, GID, mode, and write-probe evidence before planning a repair.

## Command

Dry run:

```bash
go run ./cmd/ai-logfixer-v2 \
  -mode permissions \
  -service orders-api \
  -target /srv/orders \
  -framework auto
```

Apply and verify:

```bash
go run ./cmd/ai-logfixer-v2 \
  -mode permissions \
  -service orders-api \
  -target /srv/orders \
  -framework auto \
  -verify-url http://127.0.0.1:8080/orders \
  -expected-status 200 \
  -apply
```

The command emits the normal AI LogFixer contracts plus permission-specific JSON fields:

- `framework`
- `permission_policy`
- `permission_findings`
- `permission_operations`
- `rollback_path`

## Safety Rules

- No path outside the app root can be repaired.
- Symlink escapes are blocked.
- Non-directory policy paths are blocked.
- `0777` is forbidden by policy.
- Apply mode writes a rollback manifest before making changes.
- If access probes or HTTP verification fail after a repair, Runtime V2 attempts to restore recorded modes.
- Ownership and ACL repair are intentionally blocked in this MVP unless a future policy can prove the runtime user and safe ownership target.

## Follow-Up Work

- Add ownership and ACL repair once runtime user detection is reliable.
- Add Rails, Express, Flask/FastAPI, Go, Ruby, and Java policies.
- Connect permissions mode to the Docker readiness candidate interface.
- Add framework policy refresh/research tooling that can update local policies without widening runtime write authority.
