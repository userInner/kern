#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DATA_DIR="${ROOT_DIR}/data/evals/harbor"
BIN_DIR="${DATA_DIR}/bin"
TASKS_DIR="${DATA_DIR}/terminal-bench-2"
COMMIT="2fd12b88aafdd04a52c298e3940bcb189f9766d6"
CODEX_VERSION="0.149.1"

mkdir -p "${BIN_DIR}"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -o "${BIN_DIR}/kern-eval-agent-linux-amd64" ./cmd/kern-eval-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
  -trimpath -o "${BIN_DIR}/kern-eval-agent-linux-arm64" ./cmd/kern-eval-agent

download_codex() {
  local target="$1"
  local archive_name="codex-package-${target}-unknown-linux-musl.tar.gz"
  local metadata archive_url expected_digest actual_digest temporary_dir binary code_mode_host
  metadata="$(curl -fsSL --max-time 30 "https://releases.openai.com/codex/releases/${CODEX_VERSION}/release.json")"
  archive_url="$(printf '%s' "${metadata}" | jq -r --arg name "${archive_name}" '.assets[] | select(.name == $name) | .browser_download_url')"
  expected_digest="$(printf '%s' "${metadata}" | jq -r --arg name "${archive_name}" '.assets[] | select(.name == $name) | .digest' | sed 's/^sha256://')"
  if [[ -z "${archive_url}" || -z "${expected_digest}" ]]; then
    echo "Could not resolve Codex ${CODEX_VERSION} asset ${archive_name}." >&2
    exit 1
  fi
  temporary_dir="$(mktemp -d)"
  curl -fsSL --max-time 300 "${archive_url}" -o "${temporary_dir}/${archive_name}"
  actual_digest="$(shasum -a 256 "${temporary_dir}/${archive_name}" | awk '{print $1}')"
  if [[ "${actual_digest}" != "${expected_digest}" ]]; then
    echo "Codex archive digest mismatch for ${archive_name}." >&2
    rm -rf -- "${temporary_dir}"
    exit 1
  fi
  tar -xzf "${temporary_dir}/${archive_name}" -C "${temporary_dir}"
  binary="$(find "${temporary_dir}" -type f -name codex -perm -u+x | head -1)"
  code_mode_host="$(find "${temporary_dir}" -type f -name codex-code-mode-host -perm -u+x | head -1)"
  if [[ -z "${binary}" || -z "${code_mode_host}" ]]; then
    echo "Codex archive ${archive_name} did not contain the required executables." >&2
    rm -rf -- "${temporary_dir}"
    exit 1
  fi
  install -m 0700 "${binary}" "${BIN_DIR}/codex-${CODEX_VERSION}-linux-${target}"
  install -m 0700 "${code_mode_host}" "${BIN_DIR}/codex-code-mode-host-${CODEX_VERSION}-linux-${target}"
  rm -rf -- "${temporary_dir}"
}

if [[ ! -x "${BIN_DIR}/codex-${CODEX_VERSION}-linux-x86_64" || ! -x "${BIN_DIR}/codex-code-mode-host-${CODEX_VERSION}-linux-x86_64" ]]; then
  download_codex x86_64
fi
if [[ ! -x "${BIN_DIR}/codex-${CODEX_VERSION}-linux-aarch64" || ! -x "${BIN_DIR}/codex-code-mode-host-${CODEX_VERSION}-linux-aarch64" ]]; then
  download_codex aarch64
fi

if [[ ! -d "${TASKS_DIR}/.git" ]]; then
  git clone --filter=blob:none --no-checkout \
    https://github.com/harbor-framework/terminal-bench-2.git "${TASKS_DIR}"
  git -C "${TASKS_DIR}" sparse-checkout init --cone
  TASKS=()
  while IFS= read -r task; do
    [[ -n "${task}" ]] && TASKS+=("${task}")
  done < "${SCRIPT_DIR}/tasks.txt"
  git -C "${TASKS_DIR}" sparse-checkout set "${TASKS[@]}"
  git -C "${TASKS_DIR}" checkout --detach "${COMMIT}"
else
  CURRENT="$(git -C "${TASKS_DIR}" rev-parse HEAD)"
  if [[ "${CURRENT}" != "${COMMIT}" ]]; then
    echo "Terminal-Bench checkout is ${CURRENT}; expected ${COMMIT}." >&2
    echo "Use a separate clean checkout instead of mutating this benchmark snapshot." >&2
    exit 1
  fi
fi

uv sync --project "${SCRIPT_DIR}"

printf 'Prepared Kern/Codex binaries and 10 pinned Terminal-Bench tasks in %s\n' "${DATA_DIR}"
