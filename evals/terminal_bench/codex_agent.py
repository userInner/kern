"""Harbor Codex adapter that uploads a pinned static binary into the container."""

from __future__ import annotations

import os
import shlex
from pathlib import Path

from harbor.agents.installed.codex import Codex
from harbor.environments.base import BaseEnvironment


class StaticCodex(Codex):
    """Use Harbor's Codex runtime while avoiding per-task npm installation."""

    async def install(self, environment: BaseEnvironment) -> None:
        architecture = await environment.exec("uname -m", timeout_sec=10)
        if architecture.return_code != 0:
            raise RuntimeError(f"could not determine container architecture: {architecture.stderr}")
        machine = (architecture.stdout or "").strip()
        variable = {
            "x86_64": "CODEX_HARBOR_BINARY_AMD64",
            "amd64": "CODEX_HARBOR_BINARY_AMD64",
            "aarch64": "CODEX_HARBOR_BINARY_ARM64",
            "arm64": "CODEX_HARBOR_BINARY_ARM64",
        }.get(machine)
        if variable is None:
            raise RuntimeError(f"unsupported benchmark container architecture: {machine!r}")
        binary_value = self.extra_env.get(variable) or os.environ.get(variable)
        if not binary_value:
            raise RuntimeError(f"{variable} is required; run evals/terminal_bench/prepare.sh")
        binary = Path(binary_value).expanduser().resolve()
        if not binary.is_file():
            raise RuntimeError(f"Codex evaluation binary does not exist: {binary}")

        host_variable = variable.replace("CODEX_HARBOR_BINARY", "CODEX_HARBOR_CODE_MODE_HOST")
        host_value = self.extra_env.get(host_variable) or os.environ.get(host_variable)
        if not host_value:
            raise RuntimeError(f"{host_variable} is required; run evals/terminal_bench/prepare.sh")
        code_mode_host = Path(host_value).expanduser().resolve()
        if not code_mode_host.is_file():
            raise RuntimeError(f"Codex code-mode host does not exist: {code_mode_host}")

        remote_binary = "/tmp/codex"
        remote_code_mode_host = "/tmp/codex-code-mode-host"
        await environment.upload_file(binary, remote_binary)
        await environment.upload_file(code_mode_host, remote_code_mode_host)
        result = await self.exec_as_root(
            environment,
            command=(
                f"chmod 0755 {shlex.quote(remote_binary)} {shlex.quote(remote_code_mode_host)} && "
                f"ln -sf {shlex.quote(remote_binary)} /usr/local/bin/codex && "
                "codex --version"
            ),
        )
        if result.return_code != 0:
            raise RuntimeError(f"could not install static Codex binary: {result.stderr}")
