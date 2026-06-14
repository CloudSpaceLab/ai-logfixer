#!/usr/bin/env python3
"""Run repeated black-box permission-drift readiness benchmarks."""

from __future__ import annotations

import argparse
import json
import os
import random
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path


def repo_root() -> Path:
    return Path(__file__).resolve().parents[3]


def parse_args() -> argparse.Namespace:
    root = repo_root()
    parser = argparse.ArgumentParser(
        description=(
            "Run a long-running permission-drift black-box endurance loop and "
            "write endurance-report.json."
        )
    )
    parser.add_argument(
        "--cycles",
        type=int,
        default=0,
        help="Maximum benchmark cycles to run. Zero means no cycle cap.",
    )
    parser.add_argument(
        "--duration-seconds",
        type=int,
        default=0,
        help="Optional wall-clock duration limit. Zero means use --cycles only.",
    )
    parser.add_argument(
        "--candidate-command",
        default=os.environ.get("AI_LOGFIXER_CANDIDATE_COMMAND", ""),
        help="AI LogFixer candidate command. Defaults to AI_LOGFIXER_CANDIDATE_COMMAND.",
    )
    parser.add_argument("--seed", default="", help="Seed recorded for reproducible endurance runs.")
    parser.add_argument(
        "--variants",
        default="mode-strict,missing,parent-no-exec,owner-root",
        help="Comma-separated permission-drift variants to sample. Supported: mode-strict,missing,parent-no-exec,owner-root.",
    )
    parser.add_argument(
        "--artifacts",
        default="",
        help="Artifact root. Defaults to tmp/permission-endurance/<timestamp>.",
    )
    parser.add_argument(
        "--lab-script",
        default=str(root / "labs" / "readiness" / "bin" / "run-docker-lab.sh"),
        help="Path to the readiness lab script.",
    )
    parser.add_argument(
        "--broken-timeout-seconds",
        type=int,
        default=180,
        help="Broken-probe timeout forwarded to the readiness lab.",
    )
    parser.add_argument(
        "--fixed-timeout-seconds",
        type=int,
        default=15,
        help="Fixed-probe timeout forwarded to the readiness lab.",
    )
    parser.add_argument(
        "--fail-fast",
        action="store_true",
        help="Stop after the first failed permission-drift benchmark cycle.",
    )
    return parser.parse_args()


def default_artifacts(root: Path) -> Path:
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%d%H%M%S")
    return root / "tmp" / "permission-endurance" / timestamp


def read_report(path: Path) -> dict:
    if not path.exists():
        return {}
    return json.loads(path.read_text())


def permission_summary(report: dict) -> dict:
    return report.get("lane_summary", {}).get("permission-drift", {"passed": 0, "total": 0})


def failed_permission_scenarios(report: dict) -> list[str]:
    fixed_probe = report.get("fixed_probe") or {}
    failed = []
    for result in fixed_probe.get("results", []):
        if result.get("operational_lane") == "permission-drift" and not result.get("passed"):
            failed.append(result.get("id", "unknown"))
    return failed


def write_endurance_report(artifacts: Path, payload: dict) -> None:
    artifacts.mkdir(parents=True, exist_ok=True)
    (artifacts / "endurance-report.json").write_text(json.dumps(payload, indent=2) + "\n")


def should_continue(start: float, cycles_completed: int, cycles: int, duration_seconds: int) -> bool:
    if cycles > 0 and cycles_completed >= cycles:
        return False
    if duration_seconds > 0 and cycles_completed > 0:
        return time.monotonic() - start < duration_seconds
    return True


def main() -> int:
    args = parse_args()
    root = repo_root()
    artifacts = Path(args.artifacts).resolve() if args.artifacts else default_artifacts(root)
    candidate_command = args.candidate_command.strip()
    if not candidate_command:
        print("permission endurance requires --candidate-command or AI_LOGFIXER_CANDIDATE_COMMAND", file=sys.stderr)
        return 2
    if args.cycles < 0:
        print("--cycles cannot be negative", file=sys.stderr)
        return 2
    if args.duration_seconds < 0:
        print("--duration-seconds cannot be negative", file=sys.stderr)
        return 2
    if args.cycles == 0 and args.duration_seconds == 0:
        print("permission endurance requires --cycles or --duration-seconds", file=sys.stderr)
        return 2
    variants = [variant.strip() for variant in args.variants.split(",") if variant.strip()]
    supported_variants = {"mode-strict", "missing", "parent-no-exec", "owner-root"}
    unsupported = sorted(set(variants) - supported_variants)
    if not variants:
        print("--variants must include at least one permission-drift variant", file=sys.stderr)
        return 2
    if unsupported:
        print(f"unsupported permission-drift variants: {', '.join(unsupported)}", file=sys.stderr)
        return 2
    rng = random.Random(args.seed)

    run_id = artifacts.name
    start = time.monotonic()
    cycles: list[dict] = []
    payload = {
        "schema_version": "permission-drift-endurance-report/v1",
        "focus": "permission-drift",
        "seed": args.seed,
        "variants": variants,
        "candidate_configured": True,
        "summary": {
            "ready": False,
            "cycles": 0,
            "passed_cycles": 0,
            "failed_cycles": 0,
            "permission_passed": 0,
            "permission_total": 0,
        },
        "cycles": cycles,
    }

    while should_continue(start, len(cycles), args.cycles, args.duration_seconds):
        cycle_number = len(cycles) + 1
        variant = rng.choice(variants)
        cycle_artifacts = artifacts / f"cycle-{cycle_number:04d}"
        env = os.environ.copy()
        env.update(
            {
                "AI_LOGFIXER_CANDIDATE_COMMAND": candidate_command,
                "AI_LOGFIXER_READINESS_ARTIFACTS": str(cycle_artifacts),
                "AI_LOGFIXER_READINESS_PROJECT": f"ai-logfixer-permission-endurance-{run_id}-{cycle_number}",
                "AI_LOGFIXER_LANE_FILTER": "permission-drift",
                "AI_LOGFIXER_PERMISSION_DRIFT_VARIANT": variant,
                "AI_LOGFIXER_BROKEN_PROBE_TIMEOUT_SECONDS": str(args.broken_timeout_seconds),
                "AI_LOGFIXER_FIXED_PROBE_TIMEOUT_SECONDS": str(args.fixed_timeout_seconds),
            }
        )
        command = [args.lab_script, "--mode", "benchmark", "--lane", "permission-drift"]
        print(f"permission endurance cycle {cycle_number} ({variant}): {' '.join(command)}", flush=True)
        result = subprocess.run(command, cwd=root, env=env)
        report_path = cycle_artifacts / "readiness-report.json"
        report = read_report(report_path)
        lane = permission_summary(report)
        failed = failed_permission_scenarios(report)
        cycle = {
            "cycle": cycle_number,
            "variant": variant,
            "exit_code": result.returncode,
            "report": str(report_path),
            "permission_summary": lane,
            "failed_permission_scenarios": failed,
            "ready": result.returncode == 0 and lane.get("passed") == lane.get("total") and lane.get("total", 0) > 0,
        }
        cycles.append(cycle)
        passed_cycles = sum(1 for item in cycles if item["ready"])
        permission_passed = sum(item["permission_summary"].get("passed", 0) for item in cycles)
        permission_total = sum(item["permission_summary"].get("total", 0) for item in cycles)
        payload["summary"] = {
            "ready": passed_cycles == len(cycles) and bool(cycles),
            "cycles": len(cycles),
            "passed_cycles": passed_cycles,
            "failed_cycles": len(cycles) - passed_cycles,
            "permission_passed": permission_passed,
            "permission_total": permission_total,
        }
        write_endurance_report(artifacts, payload)
        if not cycle["ready"] and args.fail_fast:
            break

    print(json.dumps(payload["summary"], indent=2))
    print(f"Endurance report: {artifacts / 'endurance-report.json'}")
    return 0 if payload["summary"]["ready"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
