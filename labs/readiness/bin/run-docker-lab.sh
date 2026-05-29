#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
timestamp="$(date -u +%Y%m%d%H%M%S)"
project="${AI_LOGFIXER_READINESS_PROJECT:-ai-logfixer-readiness-${timestamp}-$$}"
lab_root="${AI_LOGFIXER_READINESS_WORKDIR:-$(mktemp -d -t ai-logfixer-readiness-lab.XXXXXX)}"
artifacts="${AI_LOGFIXER_READINESS_ARTIFACTS:-$repo_root/tmp/readiness-lab/$timestamp}"
agent_command="${AI_LOGFIXER_AGENT_COMMAND:-$repo_root/labs/readiness/bin/safe-runtime-fixer.sh}"
keep_lab="${AI_LOGFIXER_KEEP_READINESS_LAB:-0}"

mkdir -p "$artifacts/logs" "$artifacts/live-traces" "$artifacts/probes"
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

python3 - "$lab_root/lab.json" "$artifacts/probes/broken.json" broken <<'PY'
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
deadline = time.time() + 180
results = []

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

for scenario in manifest["scenarios"]:
    expected = scenario["expected_broken_status"] if mode == "broken" else scenario["expected_fixed_status"]
    final_status = 0
    final_body = ""
    while time.time() < deadline:
        final_status, final_body = fetch(scenario["live_probe_url"])
        if final_status == expected:
            break
        time.sleep(2)
    if final_status != expected:
        raise SystemExit(
            f"{scenario['id']} expected HTTP {expected} in {mode} mode, got {final_status}: {final_body[:300]}"
        )
    for _ in range(5):
        final_status, final_body = fetch(scenario["live_probe_url"])
    results.append({
        "id": scenario["id"],
        "url": scenario["live_probe_url"],
        "status": final_status,
        "body": final_body[:500],
    })

output.write_text(json.dumps(results, indent=2))
PY

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

python3 - "$lab_root/lab.json" "$lab_root" "$artifacts/logs" "$artifacts/live-traces" <<'PY'
from pathlib import Path
import json
import sys

manifest = json.loads(Path(sys.argv[1]).read_text())
lab_root = Path(sys.argv[2]).resolve()
logs_dir = Path(sys.argv[3])
trace_dir = Path(sys.argv[4])
trace_dir.mkdir(parents=True, exist_ok=True)

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
    raw = (logs_dir / f"{scenario['id']}.log").read_text()
    app_dir = str((lab_root / scenario["app_dir"]).resolve())
    container_dir = scenario.get("container_app_dir", "/app").rstrip("/")
    mapped = map_container_paths(strip_compose_prefix(raw), container_dir, app_dir)
    (trace_dir / f"{scenario['id']}.log").write_text(mapped)
PY

(
  cd "$repo_root"
  go run ./cmd/ai-logfixer-readiness-lab \
    -manifest "$lab_root/lab.json" \
    -trace-dir "$artifacts/live-traces" \
    -in-place \
    -use-docker-validations \
    -concurrency 5 \
    -timeout 10m \
    -agent-command "$agent_command" \
    > "$artifacts/readiness-report.json"
)

"${compose[@]}" up -d --build

python3 - "$lab_root/lab.json" "$artifacts/probes/fixed.json" fixed <<'PY'
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
deadline = time.time() + 180
results = []

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

for scenario in manifest["scenarios"]:
    expected = scenario["expected_fixed_status"]
    expected_body = scenario["fixed_body_contains"]
    final_status = 0
    final_body = ""
    while time.time() < deadline:
        final_status, final_body = fetch(scenario["live_probe_url"])
        if final_status == expected and expected_body in final_body:
            break
        time.sleep(2)
    if final_status != expected or expected_body not in final_body:
        raise SystemExit(
            f"{scenario['id']} expected HTTP {expected} containing {expected_body!r}, got {final_status}: {final_body[:300]}"
        )
    results.append({
        "id": scenario["id"],
        "url": scenario["live_probe_url"],
        "status": final_status,
        "body": final_body[:500],
    })

output.write_text(json.dumps(results, indent=2))
PY

echo "Docker readiness lab passed."
echo "Report: $artifacts/readiness-report.json"
echo "Broken probes: $artifacts/probes/broken.json"
echo "Fixed probes: $artifacts/probes/fixed.json"
