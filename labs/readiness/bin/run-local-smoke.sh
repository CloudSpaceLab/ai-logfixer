#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$repo_root"

go run ./cmd/ai-logfixer-readiness-lab \
  -manifest labs/readiness/lab.json \
  -concurrency 5 \
  -agent-command "$repo_root/labs/readiness/bin/marker-agent.sh"
