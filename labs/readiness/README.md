# Production Readiness Lab

This lab is the repeatable place to test whether AI LogFixer can investigate, patch, validate, and retain rollback data across multiple common application stacks at the same time.

It is intentionally separate from Control One. Control One can call AI LogFixer later, but this lab proves AI LogFixer itself.

## Scenario Matrix

| Scenario | Runtime | Framework | Backing services | Fault |
| --- | --- | --- | --- | --- |
| `php-laravel` | PHP 8.3 | Laravel-shaped app | PostgreSQL | controller runtime failure |
| `python-fastapi` | Python 3.12 | FastAPI | PostgreSQL, Redis | endpoint runtime failure |
| `python-django` | Python 3.12 | Django | PostgreSQL | view runtime failure |
| `node-express` | Node 22 | Express | MySQL, Redis | route runtime failure |
| `ruby-rails` | Ruby 3.3 | Rails-shaped app | PostgreSQL | controller runtime failure |

## Fast Resolver Smoke Test

The deterministic marker agent is only for proving the readiness harness, sandbox validation, five-way concurrency, and rollback metadata.

```bash
labs/readiness/bin/run-local-smoke.sh
```

Expected result: a JSON report with `passed: 5`, `total: 5`, and `pass_rate: 1`.

## Live Broken Service Lab

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
