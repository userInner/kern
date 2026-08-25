from __future__ import annotations

import asyncio
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from evals.terminal_bench.codex_agent import StaticCodex


class FakeEnvironment:
    def __init__(self):
        self.uploads: dict[str, bytes] = {}
        self.commands: list[str] = []

    async def exec(self, command: str, **kwargs):
        del kwargs
        self.commands.append(command)
        if command == "uname -m":
            return SimpleNamespace(return_code=0, stdout="x86_64\n", stderr="")
        return SimpleNamespace(return_code=0, stdout="codex-cli 0.149.1\n", stderr="")

    async def upload_file(self, source_path: Path, target_path: str):
        self.uploads[target_path] = Path(source_path).read_bytes()


class StaticCodexTest(unittest.TestCase):
    def test_install_uploads_matching_binary_and_code_mode_host(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "codex"
            code_mode_host = root / "codex-code-mode-host"
            binary.write_bytes(b"codex")
            code_mode_host.write_bytes(b"host")
            environment = FakeEnvironment()
            agent = StaticCodex(
                logs_dir=root / "logs",
                model_name="openai/gpt-5.6-terra",
                extra_env={
                    "CODEX_HARBOR_BINARY_AMD64": str(binary),
                    "CODEX_HARBOR_CODE_MODE_HOST_AMD64": str(code_mode_host),
                },
            )

            asyncio.run(agent.install(environment))

            self.assertEqual(environment.uploads["/tmp/codex"], b"codex")
            self.assertEqual(environment.uploads["/tmp/codex-code-mode-host"], b"host")
            self.assertIn("codex-code-mode-host", "\n".join(environment.commands))


if __name__ == "__main__":
    unittest.main()
