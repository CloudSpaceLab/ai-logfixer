#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  labs/readiness/bin/run-docker-lab.sh --mode fixture-health
  labs/readiness/bin/run-docker-lab.sh --mode benchmark

Modes:
  fixture-health  Build the lab, confirm all fixtures are broken, capture evidence, and exit 0.
  benchmark       Run AI_LOGFIXER_CANDIDATE_COMMAND if set, then verify recovery. Expected current state: benchmark-fails-without-candidate.
USAGE
}

mode="benchmark"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      mode="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$mode" != "fixture-health" && "$mode" != "benchmark" ]]; then
  echo "invalid mode: $mode" >&2
  usage >&2
  exit 2
fi

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
timestamp="$(date -u +%Y%m%d%H%M%S)"
project="${AI_LOGFIXER_READINESS_PROJECT:-ai-logfixer-operational-${timestamp}-$$}"
lab_root="${AI_LOGFIXER_READINESS_WORKDIR:-$(mktemp -d -t ai-logfixer-operational-lab.XXXXXX)}"
artifacts="${AI_LOGFIXER_READINESS_ARTIFACTS:-$repo_root/tmp/readiness-lab/$timestamp}"
candidate_command="${AI_LOGFIXER_CANDIDATE_COMMAND:-}"
keep_lab="${AI_LOGFIXER_KEEP_READINESS_LAB:-0}"
broken_timeout="${AI_LOGFIXER_BROKEN_PROBE_TIMEOUT_SECONDS:-180}"
fixed_timeout="${AI_LOGFIXER_FIXED_PROBE_TIMEOUT_SECONDS:-15}"

mkdir -p \
  "$artifacts/logs" \
  "$artifacts/live-traces" \
  "$artifacts/probes" \
  "$artifacts/candidate-inputs" \
  "$artifacts/candidate-logs" \
  "$artifacts/inventory"

cp -R "$repo_root/labs/readiness/." "$lab_root/"

compose=(docker compose -f "$lab_root/docker-compose.yml" -p "$project")

cleanup() {
  if [[ "$keep_lab" != "1" ]]; then
    "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$lab_root"
  else
    printf "Keeping lab root: %s\n" "$lab_root"
    printf "Keeping compose project: %s\n" "$project"
  fi
}
trap cleanup EXIT

echo "readiness lab mode: $mode"
echo "readiness lab root: $lab_root"
echo "readiness artifacts: $artifacts"
echo "compose project: $project"

"${compose[@]}" up -d --build

probe_manifest() {
  local probe_mode="$1"
  local output="$2"
  local timeout_seconds="$3"
  python3 - "$lab_root/lab.json" "$output" "$probe_mode" "$timeout_seconds" <<'PY'
from pathlib import Path
from http.client import RemoteDisconnected
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
import json
import sys
import time

manifest = json.loads(Path(sys.argv[1]).read_text())
output = Path(sys.argv[2])
probe_mode = sys.argv[3]
timeout_seconds = int(sys.argv[4])
deadline = time.time() + timeout_seconds
latest = {}
pending = {scenario["id"] for scenario in manifest["scenarios"]}

def fetch(url):
    request = Request(url, headers={"Accept": "application/json"})
    try:
        with urlopen(request, timeout=5) as response:
            return response.status, response.read().decode("utf-8", "replace")
    except HTTPError as error:
        return error.code, error.read().decode("utf-8", "replace")
    except URLError as error:
        return 0, str(error)
    except (RemoteDisconnected, TimeoutError, ConnectionResetError) as error:
        return 0, str(error)

def expected_for(scenario):
    if probe_mode == "broken":
        return scenario["expected_broken_status"], None
    return scenario["expected_fixed_status"], scenario["fixed_body_contains"]

def is_pass(scenario, status, body):
    expected_status, expected_body = expected_for(scenario)
    if status != expected_status:
        return False
    return expected_body is None or expected_body in body

while time.time() < deadline and pending:
    for scenario in manifest["scenarios"]:
        if scenario["id"] not in pending:
            continue
        status, body = fetch(scenario["live_probe_url"])
        latest[scenario["id"]] = {
            "id": scenario["id"],
            "operational_lane": scenario["operational_lane"],
            "url": scenario["live_probe_url"],
            "status": status,
            "body": body[:500],
        }
        if is_pass(scenario, status, body):
            pending.remove(scenario["id"])
    if pending:
        time.sleep(2)

results = []
for scenario in manifest["scenarios"]:
    record = latest.get(scenario["id"])
    if record is None:
        status, body = fetch(scenario["live_probe_url"])
        record = {
            "id": scenario["id"],
            "operational_lane": scenario["operational_lane"],
            "url": scenario["live_probe_url"],
            "status": status,
            "body": body[:500],
        }
    expected_status, expected_body = expected_for(scenario)
    record["expected_status"] = expected_status
    if expected_body is not None:
        record["expected_body_contains"] = expected_body
    record["passed"] = is_pass(scenario, record["status"], record["body"])
    results.append(record)

payload = {
    "mode": probe_mode,
    "passed": sum(1 for result in results if result["passed"]),
    "total": len(results),
    "results": results,
}
output.write_text(json.dumps(payload, indent=2))
print(json.dumps({"mode": probe_mode, "passed": payload["passed"], "total": payload["total"]}, indent=2))
PY
}

capture_evidence() {
  for row in $(python3 - "$lab_root/lab.json" <<'PY'
from pathlib import Path
import json
import sys
manifest = json.loads(Path(sys.argv[1]).read_text())
for scenario in manifest["scenarios"]:
    print(f"{scenario['id']}:{scenario['docker_service']}")
PY
  ); do
    scenario_id="${row%%:*}"
    service="${row#*:}"
    "${compose[@]}" logs --no-color "$service" > "$artifacts/logs/$scenario_id.log"
  done

  for service in permission-drift-api restart-reload-api; do
    "${compose[@]}" exec -T "$service" sh -lc 'id; find /app -maxdepth 3 -type d -name logs -exec ls -ld {} \; 2>/dev/null || true; ps 2>/dev/null || true' \
      > "$artifacts/inventory/$service.txt" 2>&1 || true
  done

  python3 - "$lab_root/lab.json" "$lab_root" "$artifacts/logs" "$artifacts/live-traces" "$artifacts/candidate-inputs" "$artifacts/inventory" "$project" "$lab_root/docker-compose.yml" <<'PY'
from pathlib import Path
import json
import sys

manifest = json.loads(Path(sys.argv[1]).read_text())
lab_root = Path(sys.argv[2]).resolve()
logs_dir = Path(sys.argv[3])
trace_dir = Path(sys.argv[4])
inputs_dir = Path(sys.argv[5])
inventory_dir = Path(sys.argv[6])
compose_project = sys.argv[7]
compose_file = sys.argv[8]
trace_dir.mkdir(parents=True, exist_ok=True)
inputs_dir.mkdir(parents=True, exist_ok=True)

def strip_compose_prefix(raw):
    lines = []
    for line in raw.splitlines():
        if " | " in line:
            before, after = line.split(" | ", 1)
            if "-" in before:
                line = after
        lines.append(line)
    return "\n".join(lines) + ("\n" if raw.endswith("\n") else "")

for scenario in manifest["scenarios"]:
    app_dir = str((lab_root / scenario["app_dir"]).resolve())
    policy_file = str((lab_root / scenario["policy_file"]).resolve())
    raw_log = (logs_dir / f"{scenario['id']}.log").read_text()
    trace_file = trace_dir / f"{scenario['id']}.log"
    trace_file.write_text(strip_compose_prefix(raw_log))
    candidate_input = {
        "scenario_id": scenario["id"],
        "operational_lane": scenario["operational_lane"],
        "runtime": scenario["runtime"],
        "app_carrier": scenario["app_carrier"],
        "service_name": scenario["service_name"],
        "docker_service": scenario["docker_service"],
        "app_dir": app_dir,
        "policy_file": policy_file,
        "trace_file": str(trace_file),
        "inventory_dir": str(inventory_dir),
        "compose_file": compose_file,
        "compose_project": compose_project,
        "live_probe_url": scenario["live_probe_url"],
        "expected_fixed_status": scenario["expected_fixed_status"],
        "fixed_body_contains": scenario["fixed_body_contains"],
        "safe_action": scenario["safe_action"],
    }
    (inputs_dir / f"{scenario['id']}.json").write_text(json.dumps(candidate_input, indent=2))
PY
}

write_report() {
  local report_mode="$1"
  local output="$2"
  python3 - "$lab_root/lab.json" "$artifacts/probes/broken.json" "$artifacts/probes/fixed.json" "$artifacts/candidate-results.ndjson" "$output" "$report_mode" <<'PY'
from pathlib import Path
import json
import sys

manifest = json.loads(Path(sys.argv[1]).read_text())
broken = json.loads(Path(sys.argv[2]).read_text())
fixed_path = Path(sys.argv[3])
candidate_path = Path(sys.argv[4])
output = Path(sys.argv[5])
report_mode = sys.argv[6]
fixed = json.loads(fixed_path.read_text()) if fixed_path.exists() else None
candidate_results = [
    json.loads(line)
    for line in candidate_path.read_text().splitlines()
    if line.strip()
] if candidate_path.exists() else []

if report_mode == "fixture-health":
    passed = broken["passed"]
    total = broken["total"]
    ready = passed == total
else:
    passed = fixed["passed"] if fixed else 0
    total = fixed["total"] if fixed else len(manifest["scenarios"])
    ready = passed == total

lanes = {}
source = fixed if report_mode == "benchmark" and fixed else broken
for result in source["results"]:
    lane = result["operational_lane"]
    lane_summary = lanes.setdefault(lane, {"passed": 0, "total": 0})
    lane_summary["total"] += 1
    if result["passed"]:
        lane_summary["passed"] += 1

report = {
    "schema_version": "operational-drift-readiness-report/v1",
    "issue": 25,
    "mode": report_mode,
    "expected_current_mode": manifest["expected_current_mode"],
    "summary": {
        "ready": ready,
        "passed": passed,
        "total": total,
        "pass_rate": passed / total if total else 0,
        "candidate_configured": any(item.get("configured") for item in candidate_results),
    },
    "lane_summary": lanes,
    "scenario_matrix": [
        {
            "id": scenario["id"],
            "operational_lane": scenario["operational_lane"],
            "runtime": scenario["runtime"],
            "app_carrier": scenario["app_carrier"],
            "safe_action": scenario["safe_action"],
        }
        for scenario in manifest["scenarios"]
    ],
    "broken_probe": broken,
    "fixed_probe": fixed,
    "candidate_results": candidate_results,
}
output.write_text(json.dumps(report, indent=2))
print(json.dumps(report["summary"], indent=2))
raise SystemExit(0 if ready else 1)
PY
}

probe_manifest broken "$artifacts/probes/broken.json" "$broken_timeout"
capture_evidence

if [[ "$mode" == "fixture-health" ]]; then
  : > "$artifacts/candidate-results.ndjson"
  write_report fixture-health "$artifacts/readiness-report.json"
  echo "Fixture health passed."
  echo "Report: $artifacts/readiness-report.json"
  exit 0
fi

candidate_results="$artifacts/candidate-results.ndjson"
: > "$candidate_results"

if [[ -z "$candidate_command" ]]; then
  python3 - "$candidate_results" <<'PY'
from pathlib import Path
import json
import sys
Path(sys.argv[1]).write_text(json.dumps({
    "configured": False,
    "message": "AI_LOGFIXER_CANDIDATE_COMMAND is not set, so the benchmark did not apply remediation.",
}) + "\n")
PY
else
  pids_file="$artifacts/candidate-pids.tsv"
  : > "$pids_file"
  while IFS=$'\t' read -r scenario_id lane runtime app_carrier app_dir docker_service policy_file live_probe_url; do
    candidate_input="$artifacts/candidate-inputs/$scenario_id.json"
    trace_file="$artifacts/live-traces/$scenario_id.log"
    candidate_log="$artifacts/candidate-logs/$scenario_id.log"
    (
      cd "$repo_root"
      AI_LOGFIXER_LAB_ROOT="$lab_root" \
      AI_LOGFIXER_ARTIFACTS_DIR="$artifacts" \
      AI_LOGFIXER_SCENARIO_ID="$scenario_id" \
      AI_LOGFIXER_OPERATIONAL_LANE="$lane" \
      AI_LOGFIXER_RUNTIME="$runtime" \
      AI_LOGFIXER_APP_CARRIER="$app_carrier" \
      AI_LOGFIXER_APP_DIR="$app_dir" \
      AI_LOGFIXER_DOCKER_SERVICE="$docker_service" \
      AI_LOGFIXER_POLICY_FILE="$policy_file" \
      AI_LOGFIXER_TRACE_FILE="$trace_file" \
      AI_LOGFIXER_CANDIDATE_INPUT="$candidate_input" \
      AI_LOGFIXER_COMPOSE_FILE="$lab_root/docker-compose.yml" \
      AI_LOGFIXER_COMPOSE_PROJECT="$project" \
      AI_LOGFIXER_LIVE_PROBE_URL="$live_probe_url" \
      bash -lc "$candidate_command"
    ) > "$candidate_log" 2>&1 &
    printf "%s\t%s\t%s\n" "$scenario_id" "$!" "$candidate_log" >> "$pids_file"
  done < <(python3 - "$lab_root/lab.json" "$lab_root" <<'PY'
from pathlib import Path
import json
import sys
manifest = json.loads(Path(sys.argv[1]).read_text())
lab_root = Path(sys.argv[2]).resolve()
for scenario in manifest["scenarios"]:
    values = [
        scenario["id"],
        scenario["operational_lane"],
        scenario["runtime"],
        scenario["app_carrier"],
        str((lab_root / scenario["app_dir"]).resolve()),
        scenario["docker_service"],
        str((lab_root / scenario["policy_file"]).resolve()),
        scenario["live_probe_url"],
    ]
    print("\t".join(values))
PY
)

  while IFS=$'\t' read -r scenario_id pid candidate_log; do
    set +e
    wait "$pid"
    status=$?
    set -e
    python3 - "$candidate_results" "$scenario_id" "$status" "$candidate_log" <<'PY'
from pathlib import Path
import json
import sys
path = Path(sys.argv[1])
payload = {
    "configured": True,
    "scenario_id": sys.argv[2],
    "exit_code": int(sys.argv[3]),
    "log": sys.argv[4],
}
with path.open("a") as handle:
    handle.write(json.dumps(payload) + "\n")
PY
  done < "$pids_file"
fi

probe_manifest fixed "$artifacts/probes/fixed.json" "$fixed_timeout"

set +e
write_report benchmark "$artifacts/readiness-report.json"
summary_status=$?
set -e

if [[ "$summary_status" -ne 0 ]]; then
  echo "Docker operational benchmark failed."
  echo "Report: $artifacts/readiness-report.json"
  echo "Broken probes: $artifacts/probes/broken.json"
  echo "Fixed probes: $artifacts/probes/fixed.json"
  exit "$summary_status"
fi

echo "Docker operational benchmark passed."
echo "Report: $artifacts/readiness-report.json"
