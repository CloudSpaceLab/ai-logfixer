# Production Readiness Lab

This lab is the repeatable place to test whether AI LogFixer can investigate, patch, validate, and retain rollback data across multiple common application stacks at the same time.

It is intentionally separate from Control One. Control One can call AI LogFixer later, but this lab proves AI LogFixer itself.

## What the Docker Lab Proves

`labs/readiness/bin/run-docker-lab.sh` runs the full lab from a clean copied workspace:

1. Builds five application containers and three backing service containers.
2. Starts every service with an injected runtime fault.
3. Probes each public HTTP endpoint and requires the expected broken status.
4. Captures Docker logs and per-scenario trace files from the live containers.
5. Maps container source paths such as `/app/app/main.py` back to the copied host workspace.
6. Runs `cmd/ai-logfixer-readiness-lab` with five-way concurrency and Docker validation commands enabled.
7. Applies fixes in place inside the copied workspace, with snapshots and rollback metadata.
8. Rebuilds and restarts the application containers from the patched source.
9. Probes every endpoint again and requires HTTP `200` plus the expected fixed response body.
10. Writes a machine-readable report and probe artifacts under `tmp/readiness-lab/<timestamp>`.

The default agent command is `labs/readiness/bin/safe-runtime-fixer.sh`. It is a deterministic lab fixer for the injected faults. It is intentionally narrow, but it is not a marker-only dummy: it edits the same source files selected from live traces, and the Docker lab only passes when rebuilt containers serve the fixed endpoints.

Set `AI_LOGFIXER_AGENT_COMMAND` to test another compatible fixer:

```bash
AI_LOGFIXER_AGENT_COMMAND='opencode run --file {prompt_file}' \
  labs/readiness/bin/run-docker-lab.sh
```

Set `AI_LOGFIXER_KEEP_READINESS_LAB=1` to preserve the copied lab workspace and Docker Compose project for debugging.

## Scenario Matrix

| Scenario | Runtime | Framework | Backing services | Fault |
| --- | --- | --- | --- | --- |
| `php-laravel` | PHP 8.3 | Laravel | PostgreSQL | controller runtime failure |
| `python-fastapi` | Python 3.12 | FastAPI | PostgreSQL, Redis | endpoint runtime failure |
| `python-django` | Python 3.12 | Django | PostgreSQL | view runtime failure |
| `node-express` | Node 22 | Express | MySQL, Redis | route runtime failure |
| `ruby-rails` | Ruby 3.3 | Rails | PostgreSQL | controller runtime failure |

The services use these host ports by default:

| Service | Port |
| --- | --- |
| PHP/Laravel | `18081` |
| Python/FastAPI | `18082` |
| Python/Django | `18083` |
| Node/Express | `18084` |
| Ruby/Rails | `18085` |
| PostgreSQL | `15432` |
| MySQL | `13306` |
| Redis | `16379` |

## Requirements

- Docker Desktop or a compatible Docker daemon.
- Docker Compose v2 through the `docker compose` command.
- Go installed locally.
- Network access to pull base images the first time the lab runs.

## Fast Resolver Smoke Test

The deterministic marker agent is only for proving the readiness harness, sandbox validation, five-way concurrency, and rollback metadata.

```bash
labs/readiness/bin/run-local-smoke.sh
```

Expected result: a JSON report with `passed: 5`, `total: 5`, and `pass_rate: 1`.

## Full Docker Readiness Lab

Run the full lab:

```bash
labs/readiness/bin/run-docker-lab.sh
```

Expected result:

```text
Docker readiness lab passed.
Report: .../tmp/readiness-lab/<timestamp>/readiness-report.json
Broken probes: .../tmp/readiness-lab/<timestamp>/probes/broken.json
Fixed probes: .../tmp/readiness-lab/<timestamp>/probes/fixed.json
```

The report should show `passed: 5`, `total: 5`, and `pass_rate: 1`. Each result should identify an application-owned file, not only a framework bootstrap file:

| Scenario | Expected owner file family |
| --- | --- |
| `php-laravel` | `app/Http/Controllers/OrderController.php` |
| `python-fastapi` | `app/main.py` |
| `python-django` | `orders/views.py` |
| `node-express` | `src/server.js` |
| `ruby-rails` | `app/controllers/orders_controller.rb` |

## Manual Broken Service Probe

Start the app and DBMS containers:

```bash
docker compose -f labs/readiness/docker-compose.yml up --build
```

Probe the broken endpoints in a second shell:

```bash
labs/readiness/bin/probe-live.sh
```

Expected result: all five app services expose a failing `/orders/readiness` path while the backing services are available. This gives AI LogFixer realistic app, framework, DBMS, and log context to inspect.

## Local LLM Experiment

Ollama can be used as a local model-backed agent command:

```bash
OLLAMA_MODEL=qwen3:8b go run ./cmd/ai-logfixer-readiness-lab \
  -manifest labs/readiness/lab.json \
  -concurrency 5 \
  -timeout 5m \
  -agent-command "$PWD/labs/readiness/bin/ollama-agent.sh {prompt_file}"
```

The Ollama script expects a strict JSON edit response and applies exact text replacements inside the staging copy only. If the model cannot produce a safe exact edit, the scenario should fail instead of patching the target.
