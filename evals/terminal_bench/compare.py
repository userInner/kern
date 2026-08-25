"""Build a strict, sanitized Codex-versus-Kern Terminal-Bench report.

Only an explicit allowlist of scalar measurements is copied from Harbor's raw
artifacts. In particular, prompts, tool inputs and outputs, exception messages
and tracebacks, and embedded trial configuration are never placed in either
output format.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import tempfile
from collections.abc import Iterable
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any


EXPECTED_TASK_COUNT = 10
TASK_NAMESPACE = "terminal-bench"

_TASK_RE = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*\Z")
_CHECKSUM_RE = re.compile(r"[0-9a-f]{64}\Z")
_EXCEPTION_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_.]{0,127}\Z")
_KERN_VERSION_RE = re.compile(r"sha256:[0-9a-f]{16,64}\Z")
_AGENT_VERSION_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:+-]{0,127}\Z")


class ComparisonError(RuntimeError):
    """Raised when benchmark artifacts are incomplete or inconsistent."""


def build_comparison(
    repo_root: Path,
    *,
    kern_agent_version: str,
    kern_job_prefix: str,
) -> dict[str, Any]:
    """Load, validate, and sanitize the pinned Terminal-Bench comparison."""

    repo_root = Path(repo_root)
    if not _KERN_VERSION_RE.fullmatch(kern_agent_version):
        raise ComparisonError("the expected Kern agent version is not a valid digest identifier")
    if not _TASK_RE.fullmatch(kern_job_prefix):
        raise ComparisonError("the Kern job prefix is not a safe prefix identifier")

    tasks = _load_tasks(repo_root / "evals" / "terminal_bench" / "tasks.txt")
    jobs_dir = repo_root / "data" / "evals" / "harbor" / "jobs"
    codex_job = jobs_dir / "codex-terminal-bench"

    _validate_finished_job(codex_job, expected_trials=len(tasks), label="Codex")
    codex_paths = sorted(codex_job.glob("*/result.json"))
    if len(codex_paths) != EXPECTED_TASK_COUNT:
        raise ComparisonError(
            f"Codex must have exactly {EXPECTED_TASK_COUNT} child result.json files"
        )
    codex_results = _load_codex_results(codex_paths, tasks)
    codex_versions = {
        _validate_agent(result, expected_name="codex")
        for result, _ in codex_results.values()
    }
    if len(codex_versions) != 1:
        raise ComparisonError("Codex agent version differs across benchmark tasks")
    codex_agent_version = codex_versions.pop()

    kern_results: dict[str, tuple[dict[str, Any], Path]] = {}
    for task in tasks:
        job_dir = jobs_dir / f"{kern_job_prefix}-{task}"
        _validate_finished_job(job_dir, expected_trials=1, label=f"Kern task {task}")
        result_paths = sorted(job_dir.glob("*/result.json"))
        if len(result_paths) != 1:
            raise ComparisonError(
                f"Kern task {task} must have exactly one child result.json file"
            )
        result_path = result_paths[0]
        result = _read_json_object(result_path, f"Kern task {task} result")
        actual_task = _validated_task_name(result, result_path, {task}, "Kern")
        if actual_task in kern_results:
            raise ComparisonError(f"Kern has a duplicate result for task {task}")
        _validate_agent(result, expected_name="kern", expected_version=kern_agent_version)
        kern_results[actual_task] = (result, result_path)

    rows: list[dict[str, Any]] = []
    comparison_model: tuple[str, str] | None = None
    for task in tasks:
        codex_result, codex_path = codex_results[task]
        kern_result, kern_path = kern_results[task]
        codex_checksum = _validated_checksum(codex_result, f"Codex task {task}")
        kern_checksum = _validated_checksum(kern_result, f"Kern task {task}")
        if codex_checksum != kern_checksum:
            raise ComparisonError(f"task checksum mismatch for {task}")
        codex_model = _validated_model(codex_result, f"Codex task {task}")
        kern_model = _validated_model(kern_result, f"Kern task {task}")
        if codex_model != kern_model:
            raise ComparisonError(f"model identity mismatch for {task}")
        if comparison_model is None:
            comparison_model = codex_model
        elif codex_model != comparison_model:
            raise ComparisonError("model identity differs across benchmark tasks")

        rows.append(
            {
                "task": task,
                "task_checksum": codex_checksum,
                "codex": _extract_trial(codex_result, codex_path, agent="codex"),
                "kern": _extract_trial(kern_result, kern_path, agent="kern"),
            }
        )

    return {
        "schema_version": 1,
        "codex_agent_version": codex_agent_version,
        "codex_job_name": codex_job.name,
        "kern_agent_version": kern_agent_version,
        "kern_job_prefix": kern_job_prefix,
        "model": {"provider": comparison_model[0], "name": comparison_model[1]},
        "task_count": len(rows),
        "summary": {
            "codex": _summarize(row["codex"] for row in rows),
            "kern": _summarize(row["kern"] for row in rows),
        },
        "tasks": rows,
    }


def render_json(report: dict[str, Any]) -> str:
    """Render the allowlisted report deterministically as JSON."""

    return json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"


def render_markdown(report: dict[str, Any]) -> str:
    """Render the allowlisted report as a deterministic Markdown summary."""

    lines = [
        "# Terminal-Bench comparison",
        "",
        f"Model: `{report['model']['provider']}/{report['model']['name']}`",
        f"Codex agent version: `{report['codex_agent_version']}`",
        f"Codex job: `{report['codex_job_name']}`",
        f"Kern agent version: `{report['kern_agent_version']}`",
        f"Kern job prefix: `{report['kern_job_prefix']}`",
        "",
        "## Summary",
        "",
        (
            "| Agent | Trials | Scored | Unscored | Passed | "
            "Pass rate | Mean reward | Agent duration (ms) | Wall duration (ms) | "
            "Input tokens | Cache tokens | Output tokens | Provider-reported cost (USD) | Tool calls | "
            "Safety violations |"
        ),
        (
            "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | "
            "---: | ---: | ---: | ---: | ---: | ---: |"
        ),
    ]
    for agent in ("codex", "kern"):
        summary = report["summary"][agent]
        lines.append(
            "| "
            + " | ".join(
                [
                    agent.capitalize(),
                    _markdown_value(summary["trials"]),
                    _markdown_value(summary["scored"]),
                    _markdown_value(summary["unscored"]),
                    _markdown_value(summary["passed"]),
                    _markdown_percent(summary["pass_rate"]),
                    _markdown_float(summary["mean_reward"]),
                    _markdown_value(summary["agent_duration_ms"]),
                    _markdown_value(summary["wall_duration_ms"]),
                    _markdown_value(summary["input_tokens"]),
                    _markdown_value(summary["cache_tokens"]),
                    _markdown_value(summary["output_tokens"]),
                    _markdown_float(summary["cost_usd"], places=6),
                    _markdown_value(summary["tool_calls"]),
                    _markdown_value(summary["safety_violations"]),
                ]
            )
            + " |"
        )

    lines.extend(
        [
            "",
            (
                "Token and cost values are reported by each agent client/provider. "
                "Cached input is already included in input tokens where available; "
                "a reported cost of zero can mean pricing metadata was unavailable "
                "and is not evidence that the run was free."
            ),
            "",
            "## Scores",
            "",
            (
                "| Task | Task checksum | Codex reward | Codex scored | Codex exception | "
                "Kern reward | Kern scored | Kern exception |"
            ),
            "| --- | --- | ---: | :---: | --- | ---: | :---: | --- |",
        ]
    )
    for row in report["tasks"]:
        codex = row["codex"]
        kern = row["kern"]
        lines.append(
            "| "
            + " | ".join(
                [
                    row["task"],
                    f"`{row['task_checksum']}`",
                    _markdown_float(codex["reward"]),
                    _markdown_bool(codex["scored"]),
                    _markdown_value(codex["exception"]),
                    _markdown_float(kern["reward"]),
                    _markdown_bool(kern["scored"]),
                    _markdown_value(kern["exception"]),
                ]
            )
            + " |"
        )

    lines.extend(["", "## Trial measurements", ""])
    for agent in ("codex", "kern"):
        lines.extend(
            [
                f"### {agent.capitalize()}",
                "",
                (
                    "| Task | Agent duration (ms) | Wall duration (ms) | Input tokens | "
                    "Cache tokens | Output tokens | Provider-reported cost (USD) | Tool calls | Safety violations |"
                ),
                "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
            ]
        )
        for row in report["tasks"]:
            trial = row[agent]
            lines.append(
                "| "
                + " | ".join(
                    [
                        row["task"],
                        _markdown_value(trial["agent_duration_ms"]),
                        _markdown_value(trial["wall_duration_ms"]),
                        _markdown_value(trial["input_tokens"]),
                        _markdown_value(trial["cache_tokens"]),
                        _markdown_value(trial["output_tokens"]),
                        _markdown_float(trial["cost_usd"], places=6),
                        _markdown_value(trial["tool_calls"]),
                        _markdown_value(trial["safety_violations"]),
                    ]
                )
                + " |"
            )
        lines.append("")

    return "\n".join(lines).rstrip() + "\n"


def write_outputs(
    report: dict[str, Any],
    *,
    json_output: Path,
    markdown_output: Path,
) -> None:
    """Stage both rendered reports before replacing either destination."""

    json_output = Path(json_output)
    markdown_output = Path(markdown_output)
    if json_output.absolute() == markdown_output.absolute():
        raise ComparisonError("JSON and Markdown outputs must use different paths")

    rendered = (
        (json_output, render_json(report)),
        (markdown_output, render_markdown(report)),
    )
    staged: list[tuple[Path, Path]] = []
    try:
        for destination, content in rendered:
            destination.parent.mkdir(parents=True, exist_ok=True)
            with tempfile.NamedTemporaryFile(
                mode="w",
                encoding="utf-8",
                newline="\n",
                dir=destination.parent,
                prefix=f".{destination.name}.",
                delete=False,
            ) as handle:
                handle.write(content)
                handle.flush()
                os.fsync(handle.fileno())
                staged.append((Path(handle.name), destination))
        for temporary, destination in staged:
            os.replace(temporary, destination)
    finally:
        for temporary, _ in staged:
            temporary.unlink(missing_ok=True)


def _load_tasks(tasks_path: Path) -> list[str]:
    try:
        raw_lines = tasks_path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        raise ComparisonError("the Terminal-Bench task list is missing or unreadable") from error

    tasks = [
        line.strip()
        for line in raw_lines
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if len(tasks) != EXPECTED_TASK_COUNT:
        raise ComparisonError(f"tasks.txt must contain exactly {EXPECTED_TASK_COUNT} task names")
    if len(set(tasks)) != len(tasks):
        raise ComparisonError("tasks.txt contains a duplicate task name")
    if any(not _TASK_RE.fullmatch(task) for task in tasks):
        raise ComparisonError("tasks.txt contains an invalid task name")
    return tasks


def _validate_finished_job(job_dir: Path, *, expected_trials: int, label: str) -> None:
    if not job_dir.is_dir():
        raise ComparisonError(f"{label} job directory is missing")
    result = _read_json_object(job_dir / "result.json", f"{label} job result")
    if not isinstance(result.get("finished_at"), str) or not result["finished_at"]:
        raise ComparisonError(f"{label} job is still running")

    total_trials = result.get("n_total_trials")
    stats = result.get("stats")
    if not _is_nonnegative_int(total_trials) or not isinstance(stats, dict):
        raise ComparisonError(f"{label} job summary is malformed")
    if total_trials != expected_trials:
        raise ComparisonError(f"{label} job has the wrong trial count")

    counts: dict[str, int] = {}
    for key in (
        "n_completed_trials",
        "n_running_trials",
        "n_pending_trials",
        "n_cancelled_trials",
    ):
        value = stats.get(key)
        if not _is_nonnegative_int(value):
            raise ComparisonError(f"{label} job summary is malformed")
        counts[key] = value

    if counts["n_running_trials"] or counts["n_pending_trials"]:
        raise ComparisonError(f"{label} job is still running")
    if counts["n_cancelled_trials"] or counts["n_completed_trials"] != expected_trials:
        raise ComparisonError(f"{label} job is incomplete")


def _load_codex_results(
    paths: list[Path], tasks: list[str]
) -> dict[str, tuple[dict[str, Any], Path]]:
    expected = set(tasks)
    results: dict[str, tuple[dict[str, Any], Path]] = {}
    for path in paths:
        result = _read_json_object(path, "Codex child result")
        task = _validated_task_name(result, path, expected, "Codex")
        if task in results:
            raise ComparisonError(f"Codex has a duplicate result for task {task}")
        _validate_agent(result, expected_name="codex")
        results[task] = (result, path)
    missing = [task for task in tasks if task not in results]
    if missing:
        raise ComparisonError("Codex is missing one or more expected task results")
    return results


def _validated_task_name(
    result: dict[str, Any],
    result_path: Path,
    expected: set[str],
    agent: str,
) -> str:
    raw_name = result.get("task_name")
    prefix = f"{TASK_NAMESPACE}/"
    if not isinstance(raw_name, str) or not raw_name.startswith(prefix):
        raise ComparisonError(f"{agent} result has the wrong task name")
    task = raw_name.removeprefix(prefix)
    if task not in expected:
        raise ComparisonError(f"{agent} result has an unexpected task name")
    if not result_path.parent.name.startswith(f"{task}__"):
        raise ComparisonError(f"{agent} result is stored under the wrong task directory")
    return task


def _validate_agent(
    result: dict[str, Any],
    *,
    expected_name: str,
    expected_version: str | None = None,
) -> str:
    agent_info = result.get("agent_info")
    if not isinstance(agent_info, dict) or agent_info.get("name") != expected_name:
        raise ComparisonError(f"{expected_name.capitalize()} result has the wrong agent identity")
    version = agent_info.get("version")
    if not isinstance(version, str) or not _AGENT_VERSION_RE.fullmatch(version):
        raise ComparisonError(f"{expected_name.capitalize()} result has an invalid agent version")
    if expected_version is not None and version != expected_version:
        raise ComparisonError("Kern result has the wrong agent version")
    return version


def _validated_checksum(result: dict[str, Any], label: str) -> str:
    checksum = result.get("task_checksum")
    if not isinstance(checksum, str) or not _CHECKSUM_RE.fullmatch(checksum):
        raise ComparisonError(f"{label} has a missing or invalid task checksum")
    return checksum


def _validated_model(result: dict[str, Any], label: str) -> tuple[str, str]:
    agent_info = result.get("agent_info")
    model_info = agent_info.get("model_info") if isinstance(agent_info, dict) else None
    if not isinstance(model_info, dict):
        raise ComparisonError(f"{label} has missing or malformed model identity")
    name = model_info.get("name")
    provider = model_info.get("provider")
    if not isinstance(name, str) or not name or not isinstance(provider, str) or not provider:
        raise ComparisonError(f"{label} has missing or malformed model identity")
    return provider, name


def _extract_trial(result: dict[str, Any], result_path: Path, *, agent: str) -> dict[str, Any]:
    exception = _exception_type(result)
    reward = _reward(result)
    if reward is None:
        raise ComparisonError(f"{agent.capitalize()} completed result has no verifier reward")
    # Score availability and exception diagnostics are deliberately independent.
    # In particular, Harbor records a timed-out agent with both reward 0 and an
    # AgentTimeoutError; that remains a scored benchmark failure.
    scored = True

    trial_started_at, trial_finished_at, wall_duration_ms = _required_interval(
        result.get("started_at"),
        result.get("finished_at"),
        label=f"{agent.capitalize()} trial",
    )
    execution = result.get("agent_execution")
    if execution is None:
        agent_duration_ms = None
    elif isinstance(execution, dict):
        agent_interval = _optional_interval(
            execution.get("started_at"),
            execution.get("finished_at"),
            label=f"{agent.capitalize()} agent execution",
        )
        if agent_interval is None:
            agent_duration_ms = None
        else:
            agent_started_at, agent_finished_at, agent_duration_ms = agent_interval
            if agent_started_at < trial_started_at or agent_finished_at > trial_finished_at:
                raise ComparisonError(
                    f"{agent.capitalize()} agent execution falls outside the trial interval"
                )
    else:
        raise ComparisonError(f"{agent.capitalize()} agent execution is malformed")
    if scored and agent_duration_ms is None:
        raise ComparisonError(f"{agent.capitalize()} scored result has no agent duration")

    agent_result = result.get("agent_result")
    if agent_result is None:
        if scored:
            raise ComparisonError(f"{agent.capitalize()} scored result has no agent metrics")
        agent_result = {}
    elif not isinstance(agent_result, dict):
        raise ComparisonError(f"{agent.capitalize()} agent metrics are malformed")

    input_tokens = _optional_nonnegative_int(agent_result, "n_input_tokens", agent)
    cache_tokens = _optional_nonnegative_int(agent_result, "n_cache_tokens", agent)
    output_tokens = _optional_nonnegative_int(agent_result, "n_output_tokens", agent)
    cost_usd = _optional_nonnegative_number(agent_result, "cost_usd", agent)

    if agent == "kern":
        metadata = agent_result.get("metadata")
        if metadata is None:
            metadata = {}
        elif not isinstance(metadata, dict):
            raise ComparisonError("Kern agent metadata is malformed")
        tool_calls = _optional_nonnegative_int(metadata, "tool_calls", "kern")
        safety_violations = _optional_nonnegative_int(metadata, "safety_violations", "kern")
        if scored and (tool_calls is None or safety_violations is None):
            raise ComparisonError("Kern scored result is missing tool or safety measurements")
    else:
        tool_calls = _count_codex_tool_calls(result_path.parent / "agent" / "trajectory.json")
        safety_violations = None

    return {
        "reward": reward,
        "scored": scored,
        "exception": exception,
        "agent_duration_ms": agent_duration_ms,
        "wall_duration_ms": wall_duration_ms,
        "input_tokens": input_tokens,
        "cache_tokens": cache_tokens,
        "output_tokens": output_tokens,
        "cost_usd": cost_usd,
        "tool_calls": tool_calls,
        "safety_violations": safety_violations,
    }


def _exception_type(result: dict[str, Any]) -> str | None:
    exception_info = result.get("exception_info")
    if exception_info is None:
        return None
    if not isinstance(exception_info, dict):
        raise ComparisonError("trial exception metadata is malformed")
    exception = exception_info.get("exception_type")
    if not isinstance(exception, str) or not _EXCEPTION_RE.fullmatch(exception):
        raise ComparisonError("trial exception type is missing or invalid")
    return exception


def _reward(result: dict[str, Any]) -> float | None:
    verifier_result = result.get("verifier_result")
    if verifier_result is None:
        return None
    if not isinstance(verifier_result, dict):
        raise ComparisonError("trial verifier result is malformed")
    rewards = verifier_result.get("rewards")
    if rewards is None:
        return None
    if not isinstance(rewards, dict):
        raise ComparisonError("trial rewards are malformed")
    reward = rewards.get("reward")
    if reward is None:
        return None
    if isinstance(reward, bool) or not isinstance(reward, (int, float)):
        raise ComparisonError("trial reward is not numeric")
    value = float(reward)
    if not math.isfinite(value) or not 0.0 <= value <= 1.0:
        raise ComparisonError("trial reward is outside the expected range")
    return value


def _count_codex_tool_calls(trajectory_path: Path) -> int | None:
    """Count only structurally valid ATIF tool-call arrays.

    A missing or non-ATIF trajectory yields ``None`` instead of guessing from
    free-form logs, where textual mentions could produce misleading counts.
    """

    try:
        trajectory = json.loads(
            trajectory_path.read_text(encoding="utf-8"),
            object_pairs_hook=_object_without_duplicate_keys,
            parse_constant=_reject_nonfinite_json_number,
        )
    except (OSError, UnicodeError, ValueError):
        return None
    if (
        not isinstance(trajectory, dict)
        or not isinstance(trajectory.get("schema_version"), str)
        or not trajectory["schema_version"].startswith("ATIF-v1.")
        or not isinstance(trajectory.get("steps"), list)
    ):
        return None

    count = 0
    call_ids: set[str] = set()
    for step in trajectory["steps"]:
        if not isinstance(step, dict):
            return None
        tool_calls = step.get("tool_calls")
        if tool_calls is None:
            continue
        if not isinstance(tool_calls, list) or step.get("source") != "agent":
            return None
        for call in tool_calls:
            if not isinstance(call, dict):
                return None
            call_id = call.get("tool_call_id")
            function_name = call.get("function_name")
            if (
                not isinstance(call_id, str)
                or not call_id
                or call_id in call_ids
                or not isinstance(function_name, str)
                or not function_name
            ):
                return None
            call_ids.add(call_id)
            count += 1
    return count


def _summarize(trials: Iterable[dict[str, Any]]) -> dict[str, Any]:
    rows = list(trials)
    scored = [row for row in rows if row["scored"]]
    passed = [row for row in scored if row["reward"] == 1.0]
    rewards = [row["reward"] for row in scored if row["reward"] is not None]
    return {
        "trials": len(rows),
        "scored": len(scored),
        "unscored": len(rows) - len(scored),
        "passed": len(passed),
        "pass_rate": len(passed) / len(scored) if scored else None,
        "mean_reward": math.fsum(rewards) / len(rewards) if rewards else None,
        "agent_duration_ms": _sum_optional(rows, "agent_duration_ms"),
        "wall_duration_ms": _sum_optional(rows, "wall_duration_ms"),
        "input_tokens": _sum_optional(rows, "input_tokens"),
        "cache_tokens": _sum_optional(rows, "cache_tokens"),
        "output_tokens": _sum_optional(rows, "output_tokens"),
        "cost_usd": _sum_optional(rows, "cost_usd", floating=True),
        "tool_calls": _sum_optional(rows, "tool_calls"),
        "safety_violations": _sum_optional(rows, "safety_violations"),
    }


def _sum_optional(
    rows: list[dict[str, Any]], key: str, *, floating: bool = False
) -> int | float | None:
    values = [row[key] for row in rows]
    if not values or any(value is None for value in values):
        return None
    if floating:
        return math.fsum(values)
    return sum(values)


def _optional_nonnegative_int(mapping: dict[str, Any], key: str, label: str) -> int | None:
    value = mapping.get(key)
    if value is None:
        return None
    if not _is_nonnegative_int(value):
        raise ComparisonError(f"{label.capitalize()} {key} measurement is malformed")
    return value


def _optional_nonnegative_number(
    mapping: dict[str, Any], key: str, label: str
) -> float | None:
    value = mapping.get(key)
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ComparisonError(f"{label.capitalize()} {key} measurement is malformed")
    number = float(value)
    if not math.isfinite(number) or number < 0:
        raise ComparisonError(f"{label.capitalize()} {key} measurement is malformed")
    return number


def _is_nonnegative_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


def _required_interval(
    started: Any, finished: Any, *, label: str
) -> tuple[datetime, datetime, int]:
    if finished is None:
        raise ComparisonError(f"{label} is still running")
    if started is None:
        raise ComparisonError(f"{label} timestamp is missing")
    return _interval(started, finished, label=label)


def _optional_interval(
    started: Any, finished: Any, *, label: str
) -> tuple[datetime, datetime, int] | None:
    if started is None and finished is None:
        return None
    if started is None or finished is None:
        raise ComparisonError(f"{label} timestamps are incomplete")
    return _interval(started, finished, label=label)


def _interval(started: Any, finished: Any, *, label: str) -> tuple[datetime, datetime, int]:
    start = _parse_timestamp(started, label)
    end = _parse_timestamp(finished, label)
    if end < start:
        raise ComparisonError(f"{label} has a negative duration")
    duration_ms = (end - start) // timedelta(milliseconds=1)
    return start, end, duration_ms


def _parse_timestamp(value: Any, label: str) -> datetime:
    if not isinstance(value, str) or not value:
        raise ComparisonError(f"{label} timestamp is missing or invalid")
    try:
        timestamp = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ComparisonError(f"{label} timestamp is invalid") from error
    if timestamp.tzinfo is None:
        raise ComparisonError(f"{label} timestamp has no timezone")
    return timestamp


def _read_json_object(path: Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(
            path.read_text(encoding="utf-8"),
            object_pairs_hook=_object_without_duplicate_keys,
            parse_constant=_reject_nonfinite_json_number,
        )
    except (OSError, UnicodeError, ValueError) as error:
        raise ComparisonError(f"{label} is missing or is not valid JSON") from error
    if not isinstance(value, dict):
        raise ComparisonError(f"{label} is not a JSON object")
    return value


def _object_without_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    value: dict[str, Any] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON object key")
        value[key] = item
    return value


def _reject_nonfinite_json_number(value: str) -> None:
    del value
    raise ValueError("non-finite JSON number")


def _markdown_value(value: Any) -> str:
    return "—" if value is None else str(value)


def _markdown_bool(value: bool) -> str:
    return "yes" if value else "no"


def _markdown_percent(value: float | None) -> str:
    return "—" if value is None else f"{value:.1%}"


def _markdown_float(value: float | None, *, places: int = 3) -> str:
    if value is None:
        return "—"
    return f"{value:.{places}f}"


def _default_repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Create strict sanitized JSON and Markdown Terminal-Bench comparisons."
    )
    parser.add_argument(
        "--json-output",
        "--json-out",
        required=True,
        type=Path,
        help="destination for the sanitized deterministic JSON report",
    )
    parser.add_argument(
        "--markdown-output",
        "--markdown-out",
        required=True,
        type=Path,
        help="destination for the human-readable Markdown report",
    )
    parser.add_argument(
        "--kern-agent-version",
        required=True,
        help="exact Kern agent_info.version required for every trial",
    )
    parser.add_argument(
        "--kern-job-prefix",
        required=True,
        help="Kern job-name portion before the final '-<task>' suffix",
    )
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=_default_repo_root(),
        help=argparse.SUPPRESS,
    )
    arguments = parser.parse_args(argv)

    try:
        report = build_comparison(
            arguments.repo_root,
            kern_agent_version=arguments.kern_agent_version,
            kern_job_prefix=arguments.kern_job_prefix,
        )
        write_outputs(
            report,
            json_output=arguments.json_output,
            markdown_output=arguments.markdown_output,
        )
    except ComparisonError as error:
        parser.exit(1, f"comparison failed: {error}\n")
    except OSError:
        parser.exit(1, "comparison failed: could not write report outputs\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
