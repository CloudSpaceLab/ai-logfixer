#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
timestamp="$(date -u +%Y%m%d%H%M%S)"
project="${AI_LOGFIXER_READINESS_PROJECT:-ai-logfixer-readiness-${timestamp}-$$}"
lab_root="${AI_LOGFIXER_READINESS_WORKDIR:-$(mktemp -d -t ai-logfixer-readiness-lab.XXXXXX)}"
artifacts="${AI_LOGFIXER_READINESS_ARTIFACTS:-$repo_root/tmp/readiness-lab/$timestamp}"
candidate_command="${AI_LOGFIXER_CANDIDATE_COMMAND:-}"
keep_lab="${AI_LOGFIXER_KEEP_READINESS_LAB:-0}"

mkdir -p \
  "$artifacts/logs" \
  "$artifacts/live-traces" \
  "$artifacts/probes" \
  "$artifacts/candidate-inputs" \
  "$artifacts/candidate-logs"

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

echo "readiness lab root: $lab_root"
echo "readiness artifacts: $artifacts"
echo "compose project: $project"

"${compose[@]}" up -d --build

probe_manifest() {
  local mode="$1"
  local output="$2"
  local timeout_seconds="$3"
  python3 - "$lab_root/lab.json" "$output" "$mode" "$timeout_seconds" <<'PY'
from pathlib import Path
from http.client import RemoteDisconnected
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
import json
import sys
import time

manifest = json.loads(Path(sys.argv[1]).read_text())
output = Path(sys.argv[2])
mode = sys.argv[3]
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
    if mode == "broken":
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
            "url": scenario["live_probe_url"],
            "status": status,
            "body": body[:500],
        }
    record["passed"] = is_pass(scenario, record["status"], record["body"])
    expected_status, expected_body = expected_for(scenario)
    record["expected_status"] = expected_status
    if expected_body is not None:
        record["expected_body_contains"] = expected_body
    results.append(record)

payload = {
    "mode": mode,
    "passed": sum(1 for result in results if result["passed"]),
    "total": len(results),
    "results": results,
}
output.write_text(json.dumps(payload, indent=2))
if mode == "broken" and payload["passed"] != payload["total"]:
    raise SystemExit(1)
PY
}

probe_manifest broken "$artifacts/probes/broken.json" 180

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

python3 - "$lab_root/lab.json" "$lab_root" "$artifacts/logs" "$artifacts/live-traces" "$artifacts/candidate-inputs" <<'PY'
from pathlib import Path
import json
import sys

manifest = json.loads(Path(sys.argv[1]).read_text())
lab_root = Path(sys.argv[2]).resolve()
logs_dir = Path(sys.argv[3])
trace_dir = Path(sys.argv[4])
inputs_dir = Path(sys.argv[5])
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

def map_container_paths(raw, container_dir, host_dir):
    container_dir = container_dir.rstrip("/")
    if not container_dir:
        return raw
    out = []
    index = 0
    while index < len(raw):
        if raw.startswith(container_dir, index):
            next_index = index + len(container_dir)
            next_char = raw[next_index] if next_index < len(raw) else ""
            prev_char = raw[index - 1] if index > 0 else ""
            prefix_ok = index == 0 or prev_char in (" ", "\n", "\r", "\t", "\"", "'", "(", "[", "{")
            suffix_ok = next_char in ("", "/", ":", "\"", "'", ")", " ", "\n", "\r", "\t")
            if prefix_ok and suffix_ok:
                out.append(host_dir)
                index = next_index
                continue
        out.append(raw[index])
        index += 1
    return "".join(out)

for scenario in manifest["scenarios"]:
    app_dir = str((lab_root / scenario["app_dir"]).resolve())
    trace_file = trace_dir / f"{scenario['id']}.log"
    raw = (logs_dir / f"{scenario['id']}.log").read_text()
    container_dir = scenario.get("container_app_dir", "/app").rstrip("/")
    mapped = map_container_paths(strip_compose_prefix(raw), container_dir, app_dir)
    trace_file.write_text(mapped)
    candidate_input = {
        "scenario_id": scenario["id"],
        "service_name": scenario["service_name"],
        "language": scenario["language"],
        "framework": scenario["framework"],
        "app_dir": app_dir,
        "trace_file": str(trace_file),
        "message": scenario["message"],
        "expected_owner_suffix": scenario["expected_owner_suffix"],
        "live_probe_url": scenario["live_probe_url"],
        "expected_fixed_status": scenario["expected_fixed_status"],
        "fixed_body_contains": scenario["fixed_body_contains"],
    }
    (inputs_dir / f"{scenario['id']}.json").write_text(json.dumps(candidate_input, indent=2))
PY

candidate_results="$artifacts/candidate-results.ndjson"
: > "$candidate_results"

if [[ -z "$candidate_command" ]]; then
  python3 - "$candidate_results" <<'PY'
from pathlib import Path
import json
import sys
Path(sys.argv[1]).write_text(json.dumps({
    "configured": False,
    "message": "AI_LOGFIXER_CANDIDATE_COMMAND is not set, so the lab did not apply any fixes.",
}) + "\n")
PY
else
  pids_file="$artifacts/candidate-pids.tsv"
  : > "$pids_file"
  while IFS=$'\t' read -r scenario_id service_name language framework app_dir docker_service expected_owner_suffix live_probe_url; do
    trace_file="$artifacts/live-traces/$scenario_id.log"
    candidate_input="$artifacts/candidate-inputs/$scenario_id.json"
    candidate_log="$artifacts/candidate-logs/$scenario_id.log"
    (
      cd "$repo_root"
      AI_LOGFIXER_LAB_ROOT="$lab_root" \
      AI_LOGFIXER_ARTIFACTS_DIR="$artifacts" \
      AI_LOGFIXER_SCENARIO_ID="$scenario_id" \
      AI_LOGFIXER_SERVICE_NAME="$service_name" \
      AI_LOGFIXER_LANGUAGE="$language" \
      AI_LOGFIXER_FRAMEWORK="$framework" \
      AI_LOGFIXER_APP_DIR="$app_dir" \
      AI_LOGFIXER_TRACE_FILE="$trace_file" \
      AI_LOGFIXER_CANDIDATE_INPUT="$candidate_input" \
      AI_LOGFIXER_EXPECTED_OWNER_SUFFIX="$expected_owner_suffix" \
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
        scenario["service_name"],
        scenario["language"],
        scenario["framework"],
        str((lab_root / scenario["app_dir"]).resolve()),
        scenario["docker_service"],
        scenario["expected_owner_suffix"],
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

"${compose[@]}" up -d --build
probe_manifest fixed "$artifacts/probes/fixed.json" 90

set +e
summary_output="$(python3 - "$lab_root/lab.json" "$artifacts/probes/broken.json" "$artifacts/probes/fixed.json" "$candidate_results" "$artifacts/readiness-report.json" <<'PY'
from pathlib import Path
import json
import sys

manifest = json.loads(Path(sys.argv[1]).read_text())
broken = json.loads(Path(sys.argv[2]).read_text())
fixed = json.loads(Path(sys.argv[3]).read_text())
candidate_lines = [
    json.loads(line)
    for line in Path(sys.argv[4]).read_text().splitlines()
    if line.strip()
]
report_path = Path(sys.argv[5])
fixed_passed = fixed["passed"]
total = fixed["total"]
candidate_configured = any(item.get("configured") for item in candidate_lines)
ready = fixed_passed == total
report = {
    "schema_version": "docker-readiness-lab-report/v1",
    "expected_current_state": "fails until a production resolver can fix all five scenarios",
    "summary": {
        "ready": ready,
        "passed": fixed_passed,
        "total": total,
        "pass_rate": fixed_passed / total if total else 0,
        "candidate_configured": candidate_configured,
    },
    "scenario_matrix": [
        {
            "id": scenario["id"],
            "language": scenario["language"],
            "framework": scenario["framework"],
            "expected_owner_suffix": scenario["expected_owner_suffix"],
        }
        for scenario in manifest["scenarios"]
    ],
    "broken_probe": broken,
    "candidate_results": candidate_lines,
    "fixed_probe": fixed,
}
report_path.write_text(json.dumps(report, indent=2))
print(json.dumps(report["summary"], indent=2))
raise SystemExit(0 if ready else 1)
PY
)"
summary_status=$?
set -e

echo "$summary_output"
if [[ "$summary_status" -ne 0 ]]; then
  echo "Docker readiness lab failed."
  echo "Report: $artifacts/readiness-report.json"
  echo "Broken probes: $artifacts/probes/broken.json"
  echo "Fixed probes: $artifacts/probes/fixed.json"
  exit "$summary_status"
fi

echo "Docker readiness lab passed."
echo "Report: $artifacts/readiness-report.json"
echo "Broken probes: $artifacts/probes/broken.json"
echo "Fixed probes: $artifacts/probes/fixed.json"
