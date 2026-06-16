# Runtime V2 Framework Permission Intelligence

Runtime V2 permissions mode repairs framework-owned writable runtime paths from an explicit local policy. It is intended to turn permission drift into a bounded product capability instead of a generic manual `chmod` recommendation.

## Current Capability

Framework permission policies cover Laravel, Rails, Express, Flask, FastAPI, Go, Java, and lightweight Ruby services. Auto-detection uses local app markers such as `artisan` plus `composer.json`, Rails `Gemfile` or `config/application.rb`, Express `package.json`, Python dependency manifests, `go.mod`, Java build files, and Ruby `Gemfile`.

Each inferred path must stay inside the app root, must match its declared kind, and must use a non-world-writable expected mode. Runtime V2 records UID, GID, mode, and write-probe evidence before planning a repair.

The readiness resolver also uses this framework policy engine for permission-drift policies that omit explicit `permission_targets` and `allowed_paths`. Explicit policy targets remain authoritative; inference is only the fallback for targetless policies.

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
- Policy paths with the wrong kind are blocked.
- `0777` is forbidden by policy.
- Apply mode writes a rollback manifest before making changes.
- If access probes or HTTP verification fail after a repair, Runtime V2 attempts to restore recorded modes.
- Docker readiness remediation repairs only inferred or explicitly allowlisted paths with bounded owner/group/mode values and records rollback evidence.

## Follow-Up Work

- Add framework policy refresh/research tooling that can update local policies without widening runtime write authority.
- Expand framework-specific policies when real framework documentation or black-box failures justify additional paths.
- Add stronger runtime user detection for stacks where `app:app` is not the correct service identity.
