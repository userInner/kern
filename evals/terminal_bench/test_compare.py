from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from typing import Any

from evals.terminal_bench.compare import (
    ComparisonError,
    build_comparison,
    main,
    render_json,
    render_markdown,
    write_outputs,
)


TASKS = [f"fixture-task-{index}" for index in range(10)]
KERN_AGENT_VERSION = "sha256:57acc3c399d3b7c5"
KERN_JOB_PREFIX = "kern-policy-fixture"
SECRETS = {
    "prompt-secret",
    "argument-secret",
    "output-secret",
    "traceback-secret",
    "config-secret",
}


class ComparisonTest(unittest.TestCase):
    def test_builds_deterministic_sanitized_report_and_scores_timeout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _make_fixture(root)

            first = _build_comparison(root)
            second = _build_comparison(root)
            json_report = render_json(first)
            markdown_report = render_markdown(first)

            self.assertEqual(first, second)
            self.assertEqual(json_report, render_json(second))
            self.assertEqual(markdown_report, render_markdown(second))
            self.assertEqual(first["task_count"], 10)
            self.assertEqual(first["codex_agent_version"], "0.149.1")
            self.assertEqual(first["codex_job_name"], "codex-terminal-bench")
            self.assertEqual(first["model"], {"provider": "openai", "name": "fixture-model"})
            self.assertEqual(first["summary"]["codex"]["scored"], 10)
            self.assertEqual(first["summary"]["codex"]["passed"], 9)
            self.assertEqual(first["summary"]["codex"]["tool_calls"], 20)
            self.assertIsNone(first["summary"]["kern"]["cache_tokens"])

            timeout = first["tasks"][0]["codex"]
            self.assertEqual(timeout["reward"], 0.0)
            self.assertTrue(timeout["scored"])
            self.assertEqual(timeout["exception"], "AgentTimeoutError")
            self.assertEqual(timeout["agent_duration_ms"], 8_000)
            self.assertEqual(timeout["wall_duration_ms"], 10_000)
            self.assertEqual(timeout["tool_calls"], 2)
            self.assertEqual(first["tasks"][0]["kern"]["safety_violations"], 0)

            serialized = json_report + markdown_report
            for secret in SECRETS:
                self.assertNotIn(secret, serialized)
            self.assertTrue(
                _all_keys(first).isdisjoint(
                    {"prompt", "arguments", "output", "traceback", "config", "api_key"}
                )
            )

            json_path = root / "reports" / "comparison.json"
            markdown_path = root / "reports" / "comparison.md"
            write_outputs(first, json_output=json_path, markdown_output=markdown_path)
            self.assertEqual(json_path.read_text(encoding="utf-8"), json_report)
            self.assertEqual(markdown_path.read_text(encoding="utf-8"), markdown_report)

    def test_cli_writes_both_requested_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _make_fixture(root)
            json_path = root / "cli" / "comparison.json"
            markdown_path = root / "cli" / "comparison.md"

            status = main(
                [
                    "--repo-root",
                    str(root),
                    "--json-output",
                    str(json_path),
                    "--markdown-output",
                    str(markdown_path),
                    "--kern-agent-version",
                    KERN_AGENT_VERSION,
                    "--kern-job-prefix",
                    KERN_JOB_PREFIX,
                ]
            )

            self.assertEqual(status, 0)
            self.assertTrue(json_path.is_file())
            self.assertTrue(markdown_path.is_file())

    def test_non_timeout_exception_with_reward_remains_scored(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _make_fixture(root)
            path = _trial_path(root, "codex", TASKS[1])
            result = _read_json(path)
            result["exception_info"] = {
                "exception_type": "VerifierWarning",
                "exception_message": "output-secret",
                "exception_traceback": "traceback-secret",
            }
            _write_json(path, result)

            report = _build_comparison(root)

            trial = report["tasks"][1]["codex"]
            self.assertTrue(trial["scored"])
            self.assertEqual(trial["reward"], 1.0)
            self.assertEqual(trial["exception"], "VerifierWarning")

    def test_timeout_exception_does_not_override_passing_reward(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _make_fixture(root)
            path = _trial_path(root, "codex", TASKS[0])
            result = _read_json(path)
            result["verifier_result"]["rewards"]["reward"] = 1.0
            _write_json(path, result)

            report = _build_comparison(root)

            trial = report["tasks"][0]["codex"]
            self.assertTrue(trial["scored"])
            self.assertEqual(trial["reward"], 1.0)
            self.assertEqual(trial["exception"], "AgentTimeoutError")
            self.assertEqual(report["summary"]["codex"]["passed"], 10)

    def test_exception_without_verifier_reward_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _make_fixture(root)
            path = _trial_path(root, "kern", TASKS[2])
            result = _read_json(path)
            result["exception_info"] = {
                "exception_type": "EnvironmentError",
                "exception_message": "config-secret",
                "exception_traceback": "traceback-secret",
            }
            result["verifier_result"] = None
            result["agent_result"] = None
            result["agent_execution"] = None
            _write_json(path, result)

            with self.assertRaises(ComparisonError):
                _build_comparison(root)

    def test_codex_tool_count_is_none_when_trajectory_is_not_structurally_safe(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _make_fixture(root)
            trajectory = _trial_path(root, "codex", TASKS[3]).parent / "agent" / "trajectory.json"
            trajectory.write_text('{"steps": [{"tool_calls": "not-an-array"}]}', encoding="utf-8")

            report = _build_comparison(root)

            self.assertIsNone(report["tasks"][3]["codex"]["tool_calls"])

    def test_fails_closed_on_incomplete_or_inconsistent_inputs(self):
        cases = {
            "missing Codex result": _remove_codex_result,
            "running Kern job": _mark_kern_running,
            "duplicate Codex task": _duplicate_codex_task,
            "wrong Kern task": _set_wrong_kern_task,
            "checksum mismatch": _mismatch_checksum,
            "model mismatch": _mismatch_model,
            "agent duration outside trial": _move_agent_outside_trial,
            "duplicate Kern result": _add_second_kern_result,
            "wrong Kern version": _set_wrong_kern_version,
            "duplicate JSON key": _add_duplicate_json_key,
        }
        for name, mutate in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                _make_fixture(root)
                mutate(root)
                with self.assertRaises(ComparisonError):
                    _build_comparison(root)


def _make_fixture(root: Path) -> None:
    tasks_path = root / "evals" / "terminal_bench" / "tasks.txt"
    tasks_path.parent.mkdir(parents=True)
    tasks_path.write_text("\n".join(TASKS) + "\n", encoding="utf-8")

    jobs = root / "data" / "evals" / "harbor" / "jobs"
    codex_job = jobs / "codex-terminal-bench"
    _write_job_result(codex_job, 10)

    for index, task in enumerate(TASKS):
        checksum = hashlib.sha256(task.encode()).hexdigest()
        exception = "AgentTimeoutError" if index == 0 else None
        reward = 0.0 if index == 0 else 1.0
        codex_path = codex_job / f"{task}__codex" / "result.json"
        _write_json(
            codex_path,
            _trial_result(
                task,
                checksum,
                agent="codex",
                version="0.149.1",
                reward=reward,
                exception=exception,
            ),
        )
        _write_json(
            codex_path.parent / "agent" / "trajectory.json",
            {
                "schema_version": "ATIF-v1.7",
                "steps": [
                    {
                        "source": "agent",
                        "message": "prompt-secret",
                        "tool_calls": [
                            {
                                "tool_call_id": "call-1",
                                "function_name": "shell",
                                "arguments": {"command": "argument-secret"},
                            },
                            {
                                "tool_call_id": "call-2",
                                "function_name": "editor",
                                "arguments": {"patch": "argument-secret"},
                            },
                        ],
                        "observation": {"content": "output-secret"},
                    }
                ],
            },
        )

        kern_job = jobs / f"{KERN_JOB_PREFIX}-{task}"
        _write_job_result(kern_job, 1)
        _write_json(
            kern_job / f"{task}__kern" / "result.json",
            _trial_result(
                task,
                checksum,
                agent="kern",
                version=KERN_AGENT_VERSION,
                reward=1.0,
                exception=None,
            ),
        )


def _write_job_result(job_dir: Path, trials: int) -> None:
    _write_json(
        job_dir / "result.json",
        {
            "started_at": "2026-08-25T00:00:00Z",
            "finished_at": "2026-08-25T00:01:00Z",
            "n_total_trials": trials,
            "stats": {
                "n_completed_trials": trials,
                "n_running_trials": 0,
                "n_pending_trials": 0,
                "n_cancelled_trials": 0,
            },
        },
    )


def _trial_result(
    task: str,
    checksum: str,
    *,
    agent: str,
    version: str,
    reward: float,
    exception: str | None,
) -> dict[str, Any]:
    metadata = (
        {"tool_calls": 3, "safety_violations": 0, "duration_ms": 7_500}
        if agent == "kern"
        else {}
    )
    return {
        "task_name": f"terminal-bench/{task}",
        "task_checksum": checksum,
        "started_at": "2026-08-25T00:00:00Z",
        "finished_at": "2026-08-25T00:00:10Z",
        "agent_execution": {
            "started_at": "2026-08-25T00:00:01Z",
            "finished_at": "2026-08-25T00:00:09Z",
        },
        "agent_info": {
            "name": agent,
            "version": version,
            "model_info": {"name": "fixture-model", "provider": "openai"},
        },
        "agent_result": {
            "n_input_tokens": 100,
            "n_cache_tokens": None if agent == "kern" else 5,
            "n_output_tokens": 10,
            "cost_usd": 0.01,
            "metadata": metadata,
            "rollout_details": {"prompt": "prompt-secret", "output": "output-secret"},
        },
        "verifier_result": {"rewards": {"reward": reward}},
        "exception_info": (
            {
                "exception_type": exception,
                "exception_message": "output-secret",
                "exception_traceback": "traceback-secret",
            }
            if exception
            else None
        ),
        "config": {"api_key": "config-secret"},
        "command": {"arguments": "argument-secret"},
        "captured_output": "output-secret",
    }


def _trial_path(root: Path, agent: str, task: str) -> Path:
    jobs = root / "data" / "evals" / "harbor" / "jobs"
    if agent == "codex":
        return jobs / "codex-terminal-bench" / f"{task}__codex" / "result.json"
    return jobs / f"{KERN_JOB_PREFIX}-{task}" / f"{task}__kern" / "result.json"


def _build_comparison(root: Path) -> dict[str, Any]:
    return build_comparison(
        root,
        kern_agent_version=KERN_AGENT_VERSION,
        kern_job_prefix=KERN_JOB_PREFIX,
    )


def _remove_codex_result(root: Path) -> None:
    _trial_path(root, "codex", TASKS[-1]).unlink()


def _mark_kern_running(root: Path) -> None:
    path = _trial_path(root, "kern", TASKS[0]).parents[1] / "result.json"
    result = _read_json(path)
    result["finished_at"] = None
    result["stats"]["n_completed_trials"] = 0
    result["stats"]["n_running_trials"] = 1
    _write_json(path, result)


def _duplicate_codex_task(root: Path) -> None:
    path = _trial_path(root, "codex", TASKS[-1])
    duplicate_dir = path.parents[1] / f"{TASKS[0]}__duplicate"
    duplicate_dir.mkdir()
    path.rename(duplicate_dir / "result.json")
    result = _read_json(duplicate_dir / "result.json")
    result["task_name"] = f"terminal-bench/{TASKS[0]}"
    _write_json(duplicate_dir / "result.json", result)


def _set_wrong_kern_task(root: Path) -> None:
    path = _trial_path(root, "kern", TASKS[0])
    result = _read_json(path)
    result["task_name"] = "terminal-bench/not-an-expected-task"
    _write_json(path, result)


def _mismatch_checksum(root: Path) -> None:
    path = _trial_path(root, "kern", TASKS[0])
    result = _read_json(path)
    result["task_checksum"] = "f" * 64
    _write_json(path, result)


def _mismatch_model(root: Path) -> None:
    path = _trial_path(root, "kern", TASKS[0])
    result = _read_json(path)
    result["agent_info"]["model_info"]["name"] = "different-model"
    _write_json(path, result)


def _move_agent_outside_trial(root: Path) -> None:
    path = _trial_path(root, "codex", TASKS[0])
    result = _read_json(path)
    result["agent_execution"]["started_at"] = "2026-08-24T23:59:59Z"
    _write_json(path, result)


def _add_second_kern_result(root: Path) -> None:
    original = _trial_path(root, "kern", TASKS[0])
    result = _read_json(original)
    _write_json(original.parents[1] / f"{TASKS[0]}__second" / "result.json", result)


def _set_wrong_kern_version(root: Path) -> None:
    path = _trial_path(root, "kern", TASKS[0])
    result = _read_json(path)
    result["agent_info"]["version"] = "sha256:0000000000000000"
    _write_json(path, result)


def _add_duplicate_json_key(root: Path) -> None:
    path = _trial_path(root, "codex", TASKS[0])
    content = path.read_text(encoding="utf-8")
    content = content.replace(
        '"task_name":',
        f'"task_name": "terminal-bench/{TASKS[0]}", "task_name":',
        1,
    )
    path.write_text(content, encoding="utf-8")


def _write_json(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def _read_json(path: Path) -> dict[str, Any]:
    return json.loads(path.read_text(encoding="utf-8"))


def _all_keys(value: Any) -> set[str]:
    if isinstance(value, dict):
        return set(value) | set().union(*(_all_keys(item) for item in value.values()))
    if isinstance(value, list):
        return set().union(*(_all_keys(item) for item in value))
    return set()


if __name__ == "__main__":
    unittest.main()
