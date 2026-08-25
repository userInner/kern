"""Harbor custom agent that runs Kern inside an official task container."""

from __future__ import annotations

import hashlib
import json
import os
import shlex
import tempfile
from pathlib import Path

from harbor.agents.base import BaseAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext


_REMOTE_BINARY = "/tmp/kern-eval-agent"
_REMOTE_INSTRUCTION = "/tmp/kern-instruction.md"
_REMOTE_ENV = "/tmp/kern-model.env"
_REMOTE_RESULT = "/tmp/kern-result.json"


class KernAgent(BaseAgent):
    """Run the unmodified Kern Core loop in Harbor's isolated environment."""

    @staticmethod
    def name() -> str:
        return "kern"

    def __init__(self, logs_dir: Path, model_name: str | None = None, **kwargs):
        super().__init__(logs_dir=logs_dir, model_name=model_name, **kwargs)
        self._binary: Path | None = None
        self._version = "dev"

    def version(self) -> str:
        return self._version

    async def setup(self, environment: BaseEnvironment) -> None:
        architecture = await environment.exec("uname -m", timeout_sec=10)
        if architecture.return_code != 0:
            raise RuntimeError(f"could not determine container architecture: {architecture.stderr}")
        machine = (architecture.stdout or "").strip()
        variable = {
            "x86_64": "KERN_HARBOR_BINARY_AMD64",
            "amd64": "KERN_HARBOR_BINARY_AMD64",
            "aarch64": "KERN_HARBOR_BINARY_ARM64",
            "arm64": "KERN_HARBOR_BINARY_ARM64",
        }.get(machine)
        if variable is None:
            raise RuntimeError(f"unsupported benchmark container architecture: {machine!r}")
        binary_value = self.extra_env.get(variable) or os.environ.get(variable)
        if not binary_value:
            raise RuntimeError(f"{variable} is required; run evals/terminal_bench/prepare.sh")
        binary = Path(binary_value).expanduser().resolve()
        if not binary.is_file():
            raise RuntimeError(f"Kern evaluation binary does not exist: {binary}")
        self._binary = binary
        self._version = "sha256:" + _sha256(binary)[:16]
        await environment.upload_file(binary, _REMOTE_BINARY)
        result = await environment.exec(
            f"chmod 0700 {shlex.quote(_REMOTE_BINARY)}",
            timeout_sec=10,
        )
        if result.return_code != 0:
            raise RuntimeError(f"could not install Kern evaluation binary: {result.stderr}")

    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        if self._binary is None:
            raise RuntimeError("Kern agent setup did not complete")
        model = _model_name(self.extra_env, self.model_name)
        base_url = self.extra_env.get("KERN_MODEL_BASE_URL") or os.environ.get("KERN_MODEL_BASE_URL")
        if not base_url:
            raise RuntimeError("KERN_MODEL_BASE_URL is required")
        api_key = self.extra_env.get("KERN_MODEL_API_KEY")
        if api_key is None:
            api_key = os.environ.get("KERN_MODEL_API_KEY", "")

        instruction_path = self.logs_dir / "instruction.md"
        instruction_path.write_text(instruction, encoding="utf-8")
        await environment.upload_file(instruction_path, _REMOTE_INSTRUCTION)
        await self._upload_secret_environment(environment, base_url, api_key, model)

        workdir = environment.task_env_config.workdir or "/app"
        session_id = self.session_id or "harbor"
        command = " ".join(
            [
                "set -eu;",
                "set -a;",
                f". {shlex.quote(_REMOTE_ENV)};",
                "set +a;",
                f"rm -f {shlex.quote(_REMOTE_ENV)};",
                f"{shlex.quote(_REMOTE_BINARY)}",
                f"--workspace {shlex.quote(workdir)}",
                "--data-dir /tmp/kern-eval-data",
                f"--run-id {shlex.quote(session_id)}",
                f"--case-id {shlex.quote(environment.environment_name)}",
                f"--model {shlex.quote(model)}",
                f"< {shlex.quote(_REMOTE_INSTRUCTION)}",
                f"> {shlex.quote(_REMOTE_RESULT)}",
            ]
        )
        execution = await environment.exec(command, cwd=workdir, timeout_sec=1740)
        local_result = self.logs_dir / "kern-result.json"
        try:
            await environment.download_file(_REMOTE_RESULT, local_result)
            payload = json.loads(local_result.read_text(encoding="utf-8"))
        except Exception as error:
            detail = (execution.stderr or execution.stdout or "no output").strip()
            raise RuntimeError(f"Kern did not produce a valid result: {detail}") from error
        finally:
            await environment.exec(
                "rm -f "
                + " ".join(
                    shlex.quote(path)
                    for path in (_REMOTE_ENV, _REMOTE_INSTRUCTION, _REMOTE_RESULT)
                ),
                timeout_sec=10,
            )

        _populate_context(context, payload)
        if execution.return_code != 0 or payload.get("error"):
            detail = payload.get("error") or execution.stderr or execution.stdout or "unknown error"
            raise RuntimeError(f"Kern evaluation failed: {str(detail).strip()}")

    async def _upload_secret_environment(
        self,
        environment: BaseEnvironment,
        base_url: str,
        api_key: str,
        model: str,
    ) -> None:
        values = {
            "KERN_MODEL_BASE_URL": _container_base_url(base_url),
            "KERN_MODEL_API_KEY": api_key,
            "KERN_MODEL": model,
        }
        descriptor, temporary_name = tempfile.mkstemp(prefix="kern-harbor-env-")
        temporary_path = Path(temporary_name)
        try:
            with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
                for key, value in values.items():
                    stream.write(f"{key}={shlex.quote(value)}\n")
            temporary_path.chmod(0o600)
            await environment.upload_file(temporary_path, _REMOTE_ENV)
        finally:
            temporary_path.unlink(missing_ok=True)


def _model_name(extra_env: dict[str, str], configured: str | None) -> str:
    model = extra_env.get("KERN_MODEL") or os.environ.get("KERN_MODEL") or configured or ""
    model = model.strip()
    if model.startswith("openai/"):
        model = model.removeprefix("openai/")
    if not model:
        raise RuntimeError("KERN_MODEL or Harbor --model is required")
    return model


def _container_base_url(value: str) -> str:
    return (
        value.strip()
        .replace("http://127.0.0.1", "http://host.docker.internal")
        .replace("http://localhost", "http://host.docker.internal")
        .rstrip("/")
    )


def _populate_context(context: AgentContext, payload: dict) -> None:
    result = payload.get("result") or {}
    usage = result.get("Usage") or result.get("usage") or {}
    context.n_input_tokens = int(usage.get("input_tokens") or 0)
    context.n_output_tokens = int(usage.get("output_tokens") or 0)
    cost_micros = int(usage.get("cost_micros") or 0)
    context.cost_usd = cost_micros / 1_000_000
    context.metadata = {
        "kern_task_id": result.get("TaskID") or result.get("task_id"),
        "kern_task_status": result.get("TaskStatus") or result.get("task_status"),
        "duration_ms": int(usage.get("duration_ms") or 0),
        "tool_calls": int(usage.get("tool_calls") or 0),
        "retries": int(usage.get("retries") or 0),
        "human_interventions": int(usage.get("human_interventions") or 0),
        "safety_violations": int(usage.get("safety_violations") or 0),
    }


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()
