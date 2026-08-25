"""Create a compact, infrastructure-aware summary from Harbor job directories."""

from __future__ import annotations

import argparse
import json
from datetime import datetime
from pathlib import Path


def summarize_job(job_dir: Path) -> dict:
    rows = []
    for result_path in sorted(job_dir.glob("*/result.json")):
        value = json.loads(result_path.read_text(encoding="utf-8"))
        exception = value.get("exception_info")
        rewards = (value.get("verifier_result") or {}).get("rewards") or {}
        reward = rewards.get("reward")
        agent_result = value.get("agent_result") or {}
        rows.append(
            {
                "task": value.get("task_name"),
                "status": "infrastructure_error" if exception else "completed",
                "reward": reward,
                "duration_ms": _duration_ms(value.get("started_at"), value.get("finished_at")),
                "input_tokens": agent_result.get("n_input_tokens"),
                "cache_tokens": agent_result.get("n_cache_tokens"),
                "output_tokens": agent_result.get("n_output_tokens"),
                "cost_usd": agent_result.get("cost_usd"),
                "exception": (exception or {}).get("exception_type"),
            }
        )
    valid = [row for row in rows if row["status"] == "completed" and row["reward"] is not None]
    passed = [row for row in valid if float(row["reward"]) >= 1.0]
    return {
        "job": job_dir.name,
        "trials": len(rows),
        "valid_trials": len(valid),
        "infrastructure_errors": len(rows) - len(valid),
        "passed": len(passed),
        "pass_rate": len(passed) / len(valid) if valid else None,
        "mean_reward": sum(float(row["reward"]) for row in valid) / len(valid) if valid else None,
        "input_tokens": _sum_optional(rows, "input_tokens"),
        "output_tokens": _sum_optional(rows, "output_tokens"),
        "cost_usd": _sum_optional(rows, "cost_usd"),
        "duration_ms": sum(row["duration_ms"] or 0 for row in rows),
        "results": rows,
    }


def _sum_optional(rows: list[dict], key: str):
    values = [row[key] for row in rows if row[key] is not None]
    return sum(values) if values else None


def _duration_ms(started: str | None, finished: str | None) -> int | None:
    if not started or not finished:
        return None
    start = datetime.fromisoformat(started.replace("Z", "+00:00"))
    end = datetime.fromisoformat(finished.replace("Z", "+00:00"))
    return max(0, int((end - start).total_seconds() * 1000))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("jobs", nargs="+", type=Path)
    arguments = parser.parse_args()
    print(json.dumps([summarize_job(path) for path in arguments.jobs], indent=2))


if __name__ == "__main__":
    main()
