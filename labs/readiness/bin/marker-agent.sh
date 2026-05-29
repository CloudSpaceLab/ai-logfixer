#!/usr/bin/env bash
set -euo pipefail

staging_dir="${AI_LOGFIXER_STAGING_DIR:?AI_LOGFIXER_STAGING_DIR is required}"

python3 - "$staging_dir" <<'PY'
from pathlib import Path
import sys

root = Path(sys.argv[1]).resolve()
skip_dirs = {".git", ".ai-logfixer-backups", "node_modules", "vendor", ".venv", "__pycache__"}
patched = []

for path in root.rglob("*"):
    if any(part in skip_dirs for part in path.parts):
        continue
    if not path.is_file():
        continue
    try:
        content = path.read_text()
    except UnicodeDecodeError:
        continue
    next_content = content.replace("BROKEN", "FIXED")
    if next_content != content:
        path.write_text(next_content)
        patched.append(str(path.relative_to(root)))

print("patched marker files:")
for item in patched:
    print(f"- {item}")
PY
