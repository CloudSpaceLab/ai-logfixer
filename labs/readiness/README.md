# Operational Drift Docker Readiness Lab

This lab implements issue #25. It is organized around the operational failures Control One is most likely to detect and ask AI LogFixer to remediate:

- bad configs
- broken packages
- bad permissions
- restart/reload needed

The lab intentionally treats frameworks as app carriers, not as the main readiness target.

## Modes

Run fixture health:

```bash
labs/readiness/bin/run-docker-lab.sh --mode fixture-health
labs/readiness/bin/run-docker-lab.sh --mode fixture-health --lane permission-drift
```

Fixture health is expected to pass. It proves the Docker apps build, the operational failures are present, and evidence artifacts are captured.

Run the product benchmark:

```bash
labs/readiness/bin/run-docker-lab.sh --mode benchmark
labs/readiness/bin/run-docker-lab.sh --mode benchmark --lane permission-drift
```

Benchmark mode is expected to fail today without `AI_LOGFIXER_CANDIDATE_COMMAND`. That is intentional. It only passes when a real candidate fixes every scenario and verification succeeds.

## Scenarios

| Scenario | Lane | App carrier | Failure | Safe remediation target |
| --- | --- | --- | --- | --- |
| `config-drift-api` | config drift | Python HTTP | invalid upstream config URL | patch allowlisted config key from last-known-good evidence |
| `package-regression-api` | package regression | Express | bad local package version throws at runtime | rollback package to last-known-good pinned version |
| `permission-drift-go-http` | permission drift | Go `net/http` | `data` is not writable by app user | repair allowlisted owner/mode |
| `permission-drift-node-express` | permission drift | Node/Express | `uploads` is not writable by app user | repair allowlisted owner/mode |
| `permission-drift-python-flask` | permission drift | Python Flask | `instance` is not writable by app user | repair allowlisted owner/mode |
| `permission-drift-php-laravel-style` | permission drift | PHP Laravel-style | `storage/logs` is not writable by app user | repair allowlisted owner/mode |
| `permission-drift-ruby-lightweight` | permission drift | Ruby lightweight HTTP | `storage` is not writable by app user | repair allowlisted owner/mode |
| `permission-drift-java-lightweight` | permission drift | Java lightweight HTTP | `logs` is not writable by app user | repair allowlisted owner/mode |
| `restart-reload-api` | restart/reload | Go net/http | process is serving stale runtime state | restart the allowlisted service only |

## Candidate Interface

Set `AI_LOGFIXER_CANDIDATE_COMMAND` to run a real resolver. The lab runs it concurrently once per scenario.

```bash
mkdir -p tmp
go build -o tmp/ai-logfixer-readiness-resolve ./cmd/ai-logfixer-readiness-resolve

AI_LOGFIXER_CANDIDATE_COMMAND='./tmp/ai-logfixer-readiness-resolve --input "$AI_LOGFIXER_CANDIDATE_INPUT"' \
  labs/readiness/bin/run-docker-lab.sh --mode benchmark
```

Run the permission-drift endurance loop:

```bash
labs/readiness/bin/run-permission-endurance.py \
  --cycles 10 \
  --seed permission-regression-001 \
  --candidate-command './tmp/ai-logfixer-readiness-resolve --input "$AI_LOGFIXER_CANDIDATE_INPUT"'
```

Use `--duration-seconds 21600` for a six-hour run. Add `--cycles` only when you also want a hard cycle cap. The endurance runner calls the normal Docker lab with `--lane permission-drift`, writes each cycle under `tmp/permission-endurance/<timestamp>/cycle-XXXX`, and writes a top-level `endurance-report.json`.

Each candidate process receives:

| Variable | Meaning |
| --- | --- |
| `AI_LOGFIXER_SCENARIO_ID` | Scenario id |
| `AI_LOGFIXER_OPERATIONAL_LANE` | `config-drift`, `package-regression`, `permission-drift`, or `restart-reload` |
| `AI_LOGFIXER_RUNTIME` | Runtime language |
| `AI_LOGFIXER_APP_CARRIER` | App shape used by the fixture |
| `AI_LOGFIXER_APP_DIR` | Copied app workspace the candidate may inspect or modify |
| `AI_LOGFIXER_POLICY_FILE` | Safety policy for the scenario |
| `AI_LOGFIXER_TRACE_FILE` | Captured live logs |
| `AI_LOGFIXER_CANDIDATE_INPUT` | JSON input with scenario metadata |
| `AI_LOGFIXER_COMPOSE_FILE` | Docker Compose file for the copied lab |
| `AI_LOGFIXER_COMPOSE_PROJECT` | Compose project name |
| `AI_LOGFIXER_DOCKER_SERVICE` | Service name for the scenario |
| `AI_LOGFIXER_LIVE_PROBE_URL` | Endpoint that must recover |

The candidate must remediate the copied lab workspace or running copied containers. It must not modify the source fixtures in the repository.

`ai-logfixer-readiness-resolve` currently supports `config-drift` by routing the candidate input through Runtime V2 config remediation. It reads the scenario policy, patches only the first allowlisted config key from the first trusted source, verifies the live probe, and emits structured JSON. Other lanes emit a structured `unsupported` response and do not attempt remediation on `main`.

## Artifacts

Each run writes artifacts under `tmp/readiness-lab/<timestamp>`:

| Path | Contents |
| --- | --- |
| `readiness-report.json` | Final readiness report |
| `probes/broken.json` | Broken probe results |
| `probes/fixed.json` | Post-candidate probe results |
| `logs/*.log` | Raw Docker logs per scenario |
| `live-traces/*.log` | Candidate-readable logs |
| `candidate-inputs/*.json` | Per-scenario candidate input |
| `candidate-logs/*.log` | Candidate stdout/stderr |
| `inventory/*.txt` | Runtime inventory snippets |

Set `AI_LOGFIXER_KEEP_READINESS_LAB=1` to keep the copied workspace and Compose project for debugging.

## Ports

| Service | Port |
| --- | --- |
| Config drift API | `18081` |
| Package regression API | `18082` |
| Permission drift Go HTTP | `18083` |
| Restart/reload API | `18084` |
| Permission drift Node/Express | `18085` |
| Permission drift Python Flask | `18086` |
| Permission drift PHP Laravel-style | `18087` |
| Permission drift Ruby lightweight | `18088` |
| Permission drift Java lightweight | `18089` |

## Manual Probe

With the lab running, inspect current service status:

```bash
labs/readiness/bin/probe-live.sh labs/readiness/lab.json
```
