#!/usr/bin/env bash
set -euo pipefail

staging_dir="${AI_LOGFIXER_STAGING_DIR:?AI_LOGFIXER_STAGING_DIR is required}"

python3 - "$staging_dir" <<'PY'
from pathlib import Path
import re
import sys

root = Path(sys.argv[1]).resolve()
changes = []

replacements = {
    "app/Http/Controllers/OrderController.php": [
        (
            re.compile(
                r"\n\s*if \(getenv\('FAULT_MODE'\) === 'runtime_error'\) \{\n"
                r"\s*throw new \\RuntimeException\('database unavailable'\);\n"
                r"\s*\}\n",
                re.M,
            ),
            "\n",
        )
    ],
    "app/main.py": [
        (
            re.compile(
                r'(?m)^    if os\.getenv\("FAULT_MODE"\) == "runtime_error":\n'
                r'        raise RuntimeError\("database unavailable"\)\n'
            ),
            "",
        )
    ],
    "orders/views.py": [
        (
            re.compile(
                r'(?m)^    if os\.environ\.get\("FAULT_MODE"\) == "runtime_error":\n'
                r'        raise RuntimeError\("database unavailable"\)\n'
            ),
            "",
        )
    ],
    "src/server.js": [
        (
            re.compile(
                r"\n  if \(process\.env\.FAULT_MODE === 'runtime_error'\) \{\n"
                r"    throw new Error\('database unavailable'\);\n"
                r"  \}\n"
            ),
            "\n",
        )
    ],
    "app/controllers/orders_controller.rb": [
        (
            re.compile(r'(?m)^    raise "database unavailable" if ENV\["FAULT_MODE"\] == "runtime_error"\n'),
            '    # Runtime fault removed by AI LogFixer readiness lab.\n',
        )
    ],
}

for rel, rules in replacements.items():
    path = root / rel
    if not path.exists():
        continue
    content = path.read_text()
    next_content = content
    for pattern, replacement in rules:
        next_content = pattern.sub(replacement, next_content)
    next_content = next_content.replace("BROKEN", "FIXED")
    if next_content != content:
        path.write_text(next_content)
        changes.append(rel)

if not changes:
    raise SystemExit("safe runtime fixer found no supported injected runtime fault")

print("safe-runtime-fixer patched:")
for change in changes:
    print(f"- {change}")
PY
