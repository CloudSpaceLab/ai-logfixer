#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-labs/readiness/lab.json}"

python3 - "$manifest" <<'PY'
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
import json
import sys

manifest = json.loads(Path(sys.argv[1]).read_text())
for scenario in manifest["scenarios"]:
    request = Request(scenario["live_probe_url"], headers={"Accept": "application/json"})
    try:
        with urlopen(request, timeout=5) as response:
            status = response.status
            body = response.read().decode("utf-8", "replace")
    except HTTPError as error:
        status = error.code
        body = error.read().decode("utf-8", "replace")
    except URLError as error:
        status = 0
        body = str(error)
    print(f"{scenario['id']} lane={scenario['operational_lane']} status={status} body={body[:160]}")
PY
