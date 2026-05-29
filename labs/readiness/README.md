# Docker Readiness Lab

This lab is a benchmark for the "human can rest safely" goal. It runs real broken web applications in Docker, captures live container logs, and checks whether a candidate AI LogFixer implementation can repair every app.

The expected current result is failure. That is intentional. A passing result means the candidate fixer repaired all five copied applications, rebuilt the containers, and made every endpoint return the expected healthy response.

## What It Tests

The lab starts five application pipelines at the same time:

| Scenario | Runtime | Framework | Backing services | Injected failure |
| --- | --- | --- | --- | --- |
| `php-laravel` | PHP 8.3 | Laravel | PostgreSQL | controller runtime error |
| `python-fastapi` | Python 3.12 | FastAPI | PostgreSQL, Redis | endpoint runtime error |
| `python-django` | Python 3.12 | Django | PostgreSQL | view runtime error |
| `node-express` | Node 22 | Express | MySQL, Redis | route runtime error |
| `ruby-rails` | Ruby 3.3 | Rails | PostgreSQL | controller runtime error |

Each app exposes `/orders/readiness`, starts broken, and must be fixed to return `200` with `FIXED` in the response body.

## Requirements

- Docker Desktop or a compatible Docker daemon.
- Docker Compose v2 through the `docker compose` command.
- Python 3 for the lab driver.
- Network access to pull base images the first time the lab runs.

## Run the Lab

Run the benchmark without a candidate fixer:

```bash
labs/readiness/bin/run-docker-lab.sh
```

Expected current result:

```text
{
  "ready": false,
  "passed": 0,
  "total": 5,
  "pass_rate": 0,
  "candidate_configured": false
}
```

The script exits non-zero because no real resolver is wired in yet. This is the correct signal today.

## Test a Candidate Fixer

Set `AI_LOGFIXER_CANDIDATE_COMMAND` to a command that can repair one scenario. The lab invokes it concurrently once per scenario.

```bash
AI_LOGFIXER_CANDIDATE_COMMAND='your-fixer-command' \
  labs/readiness/bin/run-docker-lab.sh
```

The command receives these environment variables:

| Variable | Meaning |
| --- | --- |
| `AI_LOGFIXER_SCENARIO_ID` | Scenario id such as `python-fastapi` |
| `AI_LOGFIXER_SERVICE_NAME` | Logical service name |
| `AI_LOGFIXER_LANGUAGE` | Runtime language |
| `AI_LOGFIXER_FRAMEWORK` | Framework name |
| `AI_LOGFIXER_APP_DIR` | Copied app directory to inspect and modify |
| `AI_LOGFIXER_TRACE_FILE` | Live trace/log file captured from Docker |
| `AI_LOGFIXER_CANDIDATE_INPUT` | JSON input file with scenario metadata |
| `AI_LOGFIXER_EXPECTED_OWNER_SUFFIX` | Expected application-owned source file family |
| `AI_LOGFIXER_LIVE_PROBE_URL` | Endpoint that must recover |

The candidate must edit files under `AI_LOGFIXER_APP_DIR`. After all candidate processes finish, the lab rebuilds the app containers and probes every endpoint again.

## Artifacts

Each run writes artifacts under `tmp/readiness-lab/<timestamp>`:

| Path | Contents |
| --- | --- |
| `readiness-report.json` | Final pass/fail report |
| `probes/broken.json` | Broken endpoint probe results |
| `probes/fixed.json` | Post-candidate probe results |
| `logs/*.log` | Raw Docker logs per service |
| `live-traces/*.log` | Logs with container paths mapped to host app paths |
| `candidate-inputs/*.json` | Per-scenario input passed to candidate fixers |
| `candidate-logs/*.log` | Per-scenario candidate command output |

Set `AI_LOGFIXER_KEEP_READINESS_LAB=1` to keep the copied app workspace and Docker Compose project for debugging.

## Ports

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

## Manual Broken Probe

Start the containers:

```bash
docker compose -f labs/readiness/docker-compose.yml up --build
```

Probe the broken endpoints:

```bash
labs/readiness/bin/probe-live.sh
```

All five app endpoints should fail before a candidate fixer runs.
