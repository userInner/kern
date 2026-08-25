from __future__ import annotations

import asyncio
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from harbor.models.agent.context import AgentContext

from evals.terminal_bench.kern_agent import KernAgent, _container_base_url, _model_name


class FakeEnvironment:
    def __init__(self):
        self.environment_name = "test-task"
        self.task_env_config = SimpleNamespace(workdir="/app")
        self.uploads: dict[str, bytes] = {}
        self.commands: list[str] = []

    async def exec(self, command: str, **kwargs):
        self.commands.append(command)
        if command == "uname -m":
            return SimpleNamespace(return_code=0, stdout="x86_64\n", stderr="")
        return SimpleNamespace(return_code=0, stdout="", stderr="")

    async def upload_file(self, source_path: Path, target_path: str):
        self.uploads[target_path] = Path(source_path).read_bytes()

    async def download_file(self, source_path: str, target_path: Path):
        del source_path
        Path(target_path).write_text(
            """{
              "result": {
                "TaskID": "task-1",
                "TaskStatus": "completed",
                "Usage": {
                  "input_tokens": 120,
                  "output_tokens": 30,
                  "cost_micros": 2500,
                  "duration_ms": 900,
                  "tool_calls": 4,
                  "retries": 1,
                  "human_interventions": 0,
                  "safety_violations": 0
                }
              }
            }""",
            encoding="utf-8",
        )


class KernAgentTest(unittest.TestCase):
    def test_setup_and_run_populate_context_without_exposing_secret_in_command(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "kern-eval-agent"
            binary.write_bytes(b"test binary")
            environment = FakeEnvironment()
            agent = KernAgent(
                logs_dir=root / "logs",
                model_name="openai/gpt-5.6-terra",
                extra_env={
                    "KERN_HARBOR_BINARY_AMD64": str(binary),
                    "KERN_MODEL_BASE_URL": "https://models.example.test/",
                    "KERN_MODEL_API_KEY": "secret-test-key",
                },
            )
            agent.logs_dir.mkdir()
            agent.session_id = "session-1"
            context = AgentContext()

            asyncio.run(agent.setup(environment))
            asyncio.run(agent.run("repair the task", environment, context))

            self.assertEqual(context.n_input_tokens, 120)
            self.assertEqual(context.n_output_tokens, 30)
            self.assertEqual(context.cost_usd, 0.0025)
            self.assertEqual(context.metadata["tool_calls"], 4)
            self.assertIn(b"secret-test-key", environment.uploads["/tmp/kern-model.env"])
            self.assertNotIn("secret-test-key", "\n".join(environment.commands))

    def test_model_and_local_base_url_normalization(self):
        self.assertEqual(_model_name({}, "openai/gpt-5.6-terra"), "gpt-5.6-terra")
        self.assertEqual(
            _container_base_url("http://127.0.0.1:8788/"),
            "http://host.docker.internal:8788",
        )


if __name__ == "__main__":
    unittest.main()
