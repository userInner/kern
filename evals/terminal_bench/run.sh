#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DATA_DIR="${ROOT_DIR}/data/evals/harbor"
TASKS_DIR="${DATA_DIR}/terminal-bench-2"
JOBS_DIR="${DATA_DIR}/jobs"
MODEL="${KERN_EVAL_MODEL:-openai/gpt-5.6-terra}"
TARGET="${1:-both}"
HARBOR="${SCRIPT_DIR}/.venv/bin/harbor"

export PYTHONPATH="${ROOT_DIR}${PYTHONPATH:+:${PYTHONPATH}}"
export KERN_HARBOR_BINARY_AMD64="${DATA_DIR}/bin/kern-eval-agent-linux-amd64"
export KERN_HARBOR_BINARY_ARM64="${DATA_DIR}/bin/kern-eval-agent-linux-arm64"
export CODEX_HARBOR_BINARY_AMD64="${DATA_DIR}/bin/codex-0.149.1-linux-x86_64"
export CODEX_HARBOR_BINARY_ARM64="${DATA_DIR}/bin/codex-0.149.1-linux-aarch64"
export CODEX_HARBOR_CODE_MODE_HOST_AMD64="${DATA_DIR}/bin/codex-code-mode-host-0.149.1-linux-x86_64"
export CODEX_HARBOR_CODE_MODE_HOST_ARM64="${DATA_DIR}/bin/codex-code-mode-host-0.149.1-linux-aarch64"
export CODEX_FORCE_AUTH_JSON=1

if [[ ! -x "${HARBOR}" || ! -x "${KERN_HARBOR_BINARY_AMD64}" || ! -x "${KERN_HARBOR_BINARY_ARM64}" || ! -x "${CODEX_HARBOR_BINARY_AMD64}" || ! -x "${CODEX_HARBOR_BINARY_ARM64}" || ! -x "${CODEX_HARBOR_CODE_MODE_HOST_AMD64}" || ! -x "${CODEX_HARBOR_CODE_MODE_HOST_ARM64}" || ! -d "${TASKS_DIR}/.git" ]]; then
  echo "Benchmark assets are missing. Run evals/terminal_bench/prepare.sh first." >&2
  exit 1
fi
if [[ -z "${KERN_MODEL_BASE_URL:-}" || -z "${KERN_MODEL:-}" ]]; then
  echo "KERN_MODEL_BASE_URL and KERN_MODEL must be configured for Kern." >&2
  exit 1
fi
mkdir -p "${JOBS_DIR}"

run_kern() {
  "${HARBOR}" run \
    --path "${TASKS_DIR}" \
    --agent evals.terminal_bench.kern_agent:KernAgent \
    --model "${MODEL}" \
    --job-name kern-terminal-bench \
    --jobs-dir "${JOBS_DIR}" \
    --n-attempts 1 \
    --n-concurrent 1 \
    --max-retries 0
}

run_kern_each() {
  if [[ -z "${KERN_EVAL_JOB_PREFIX:-}" ]]; then
    echo "KERN_EVAL_JOB_PREFIX is required for kern-each." >&2
    exit 1
  fi
  if [[ ! "${KERN_EVAL_JOB_PREFIX}" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]]; then
    echo "KERN_EVAL_JOB_PREFIX must be a lowercase hyphenated identifier." >&2
    exit 1
  fi
  if [[ "${KERN_MODEL#openai/}" != "${MODEL#openai/}" ]]; then
    echo "KERN_MODEL and KERN_EVAL_MODEL must select the same model for kern-each." >&2
    exit 1
  fi

  local task
  local seen_tasks="|"
  local task_count=0
  while IFS= read -r task || [[ -n "${task}" ]]; do
    if [[ -z "${task}" || "${task}" == \#* ]]; then
      continue
    fi
    if [[ ! "${task}" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]]; then
      echo "Invalid task name in ${SCRIPT_DIR}/tasks.txt." >&2
      exit 1
    fi
    if [[ "${seen_tasks}" == *"|${task}|"* ]]; then
      echo "Duplicate task name in ${SCRIPT_DIR}/tasks.txt." >&2
      exit 1
    fi
    seen_tasks+="${task}|"
    task_count=$((task_count + 1))
  done < "${SCRIPT_DIR}/tasks.txt"

  if [[ "${task_count}" -ne 10 ]]; then
    echo "Expected exactly 10 tasks in ${SCRIPT_DIR}/tasks.txt." >&2
    exit 1
  fi

  while IFS= read -r task || [[ -n "${task}" ]]; do
    if [[ -z "${task}" || "${task}" == \#* ]]; then
      continue
    fi
    "${HARBOR}" run \
      --path "${TASKS_DIR}" \
      --include-task-name "${task}" \
      --agent evals.terminal_bench.kern_agent:KernAgent \
      --model "${MODEL}" \
      --job-name "${KERN_EVAL_JOB_PREFIX}-${task}" \
      --jobs-dir "${JOBS_DIR}" \
      --n-attempts 1 \
      --n-concurrent 1 \
      --max-retries 0
  done < "${SCRIPT_DIR}/tasks.txt"
}

run_codex() {
  "${HARBOR}" run \
    --path "${TASKS_DIR}" \
    --agent evals.terminal_bench.codex_agent:StaticCodex \
    --model "${MODEL}" \
    --job-name codex-terminal-bench \
    --jobs-dir "${JOBS_DIR}" \
    --n-attempts 1 \
    --n-concurrent 1 \
    --max-retries 0
}

case "${TARGET}" in
  kern) run_kern ;;
  kern-each) run_kern_each ;;
  codex) run_codex ;;
  both)
    run_kern
    run_codex
    ;;
  *)
    echo "Usage: $0 [kern|kern-each|codex|both]" >&2
    exit 2
    ;;
esac
