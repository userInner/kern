from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


class RunScriptTest(unittest.TestCase):
    def test_kern_each_runs_one_pinned_job_per_manifest_task(self):
        with tempfile.TemporaryDirectory() as directory:
            root, script, log_path, environment = _make_run_fixture(Path(directory))

            result = subprocess.run(
                ["bash", str(script), "kern-each"],
                cwd=root,
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            calls = _read_calls(log_path)
            tasks = _tasks()
            self.assertEqual(len(calls), len(tasks))
            for call, task in zip(calls, tasks, strict=True):
                self.assertEqual(_argument_after(call, "--include-task-name"), task)
                self.assertEqual(
                    _argument_after(call, "--job-name"),
                    f"fixture-policy-{task}",
                )
                self.assertEqual(_argument_after(call, "--n-attempts"), "1")
                self.assertEqual(_argument_after(call, "--n-concurrent"), "1")
                self.assertEqual(_argument_after(call, "--max-retries"), "0")
                self.assertEqual(
                    _argument_after(call, "--path"),
                    str(root / "data" / "evals" / "harbor" / "terminal-bench-2"),
                )

    def test_kern_each_requires_nonempty_job_prefix(self):
        with tempfile.TemporaryDirectory() as directory:
            root, script, log_path, environment = _make_run_fixture(Path(directory))
            environment.pop("KERN_EVAL_JOB_PREFIX")

            result = subprocess.run(
                ["bash", str(script), "kern-each"],
                cwd=root,
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertEqual(result.returncode, 1)
            self.assertIn("KERN_EVAL_JOB_PREFIX is required", result.stderr)
            self.assertFalse(log_path.exists())

    def test_kern_each_rejects_model_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root, script, log_path, environment = _make_run_fixture(Path(directory))
            environment["KERN_EVAL_MODEL"] = "openai/different-model"

            result = subprocess.run(
                ["bash", str(script), "kern-each"],
                cwd=root,
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertEqual(result.returncode, 1)
            self.assertIn("must select the same model", result.stderr)
            self.assertFalse(log_path.exists())


def _make_run_fixture(root: Path) -> tuple[Path, Path, Path, dict[str, str]]:
    source_dir = Path(__file__).resolve().parent
    script_dir = root / "evals" / "terminal_bench"
    script_dir.mkdir(parents=True)
    script = script_dir / "run.sh"
    script.write_text((source_dir / "run.sh").read_text(encoding="utf-8"), encoding="utf-8")

    tasks_path = script_dir / "tasks.txt"
    tasks_path.write_text((source_dir / "tasks.txt").read_text(encoding="utf-8"), encoding="utf-8")

    data_dir = root / "data" / "evals" / "harbor"
    (data_dir / "terminal-bench-2" / ".git").mkdir(parents=True)
    binary_names = (
        "kern-eval-agent-linux-amd64",
        "kern-eval-agent-linux-arm64",
        "codex-0.149.1-linux-x86_64",
        "codex-0.149.1-linux-aarch64",
        "codex-code-mode-host-0.149.1-linux-x86_64",
        "codex-code-mode-host-0.149.1-linux-aarch64",
    )
    for name in binary_names:
        binary = data_dir / "bin" / name
        binary.parent.mkdir(parents=True, exist_ok=True)
        binary.write_bytes(b"fixture")
        binary.chmod(0o755)

    log_path = root / "harbor-calls.log"
    harbor = script_dir / ".venv" / "bin" / "harbor"
    harbor.parent.mkdir(parents=True)
    harbor.write_text(
        "#!/usr/bin/env bash\n"
        "printf 'CALL\\n' >> \"${HARBOR_ARGS_LOG}\"\n"
        "printf '%s\\n' \"$@\" >> \"${HARBOR_ARGS_LOG}\"\n"
        "printf 'END\\n' >> \"${HARBOR_ARGS_LOG}\"\n",
        encoding="utf-8",
    )
    harbor.chmod(0o755)

    environment = os.environ.copy()
    environment.update(
        {
            "HARBOR_ARGS_LOG": str(log_path),
            "KERN_EVAL_JOB_PREFIX": "fixture-policy",
            "KERN_EVAL_MODEL": "openai/fixture-model",
            "KERN_MODEL_BASE_URL": "https://models.example.test",
            "KERN_MODEL": "fixture-model",
        }
    )
    return root, script, log_path, environment


def _read_calls(path: Path) -> list[list[str]]:
    calls: list[list[str]] = []
    current: list[str] | None = None
    for line in path.read_text(encoding="utf-8").splitlines():
        if line == "CALL":
            current = []
        elif line == "END":
            if current is None:
                raise AssertionError("END without CALL")
            calls.append(current)
            current = None
        elif current is not None:
            current.append(line)
    if current is not None:
        raise AssertionError("unterminated Harbor call")
    return calls


def _argument_after(arguments: list[str], flag: str) -> str:
    return arguments[arguments.index(flag) + 1]


def _tasks() -> list[str]:
    return [
        line.strip()
        for line in Path(__file__).with_name("tasks.txt").read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]


if __name__ == "__main__":
    unittest.main()
