#!/usr/bin/env bash
set -euo pipefail

model="${OLLAMA_MODEL:-qwen3:8b}"
prompt_file="${1:-${AI_LOGFIXER_PROMPT_FILE:?AI_LOGFIXER_PROMPT_FILE is required}}"
staging_dir="${AI_LOGFIXER_STAGING_DIR:?AI_LOGFIXER_STAGING_DIR is required}"
response_file="$(mktemp -t ai-logfixer-ollama-response.XXXXXX.json)"

{
cat <<'PROMPT'
You are editing a staging copy of an application.
Return only JSON with this exact shape:
{"edits":[{"file":"relative/path/from/project/root","search":"exact text to replace","replace":"replacement text"}]}
Use small, exact replacements. Do not include markdown fences.
PROMPT
echo
cat "$prompt_file"
} | ollama run "$model" > "$response_file"

python3 - "$staging_dir" "$response_file" <<'PY'
from pathlib import Path
import json
import re
import sys

root = Path(sys.argv[1]).resolve()
raw = Path(sys.argv[2]).read_text()
match = re.search(r"\{.*\}", raw, re.S)
if not match:
    raise SystemExit("ollama did not return a JSON object")

payload = json.loads(match.group(0))
edits = payload.get("edits", [])
if not isinstance(edits, list):
    raise SystemExit("ollama response must contain an edits list")

applied = []
for edit in edits:
    rel = edit.get("file", "")
    search = edit.get("search", "")
    replace = edit.get("replace", "")
    if not rel or not search:
        continue
    target = (root / rel).resolve()
    if root not in target.parents and target != root:
        raise SystemExit(f"refusing path outside staging dir: {rel}")
    content = target.read_text()
    if search not in content:
        raise SystemExit(f"search text not found in {rel}")
    target.write_text(content.replace(search, replace, 1))
    applied.append(rel)

print("ollama-applied-edits")
for rel in applied:
    print(f"- {rel}")
PY
