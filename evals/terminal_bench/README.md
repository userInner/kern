# Terminal-Bench comparison

This directory connects Kern to Harbor without changing the official task
containers or verifiers. The benchmark snapshot, Harbor version, model, agent
binary digest, and raw trial artifacts remain available for audit.

The pinned ten-task pilot covers debugging, software engineering, security,
data processing, and system administration:

- `build-cython-ext`
- `cancel-async-tasks`
- `cobol-modernization`
- `filter-js-from-html`
- `fix-git`
- `git-leak-recovery`
- `kv-store-grpc`
- `multi-source-data-merger`
- `nginx-request-logging`
- `sqlite-db-truncate`

## Prepare

Docker, Go, Git, and `uv` are required. From the repository root:

```sh
./evals/terminal_bench/prepare.sh
```

Preparation builds Kern for both common Linux container architectures,
downloads and verifies the pinned OpenAI Codex and code-mode host binaries, creates a
sparse checkout of the pinned official task commit, and installs the pinned
Harbor dependency. Generated data stays under `data/evals/harbor` and is
ignored by Git.

## Run

Configure Kern's OpenAI-compatible endpoint as usual. Harbor receives only a
temporary environment file uploaded into the isolated task container; the API
key is not included in the command line or stored in the agent logs.

```sh
export KERN_MODEL_BASE_URL=https://example.test
export KERN_MODEL_API_KEY=...
export KERN_MODEL=gpt-5.6-terra
export KERN_EVAL_MODEL=openai/gpt-5.6-terra
export KERN_EVAL_JOB_PREFIX=kern-policy-current

./evals/terminal_bench/run.sh kern-each
./evals/terminal_bench/run.sh codex
```

`kern-each` reads `tasks.txt` and creates one single-task job named
`<KERN_EVAL_JOB_PREFIX>-<task>` for every task. Choose a new lowercase,
hyphenated prefix for each reproducible Kern run. The existing `kern` target
still creates one combined `kern-terminal-bench` job, and `both` still runs that
combined job followed by Codex; neither combined form is an input to the strict
comparison.

Concurrency, attempts, retries, dataset commit, and model identity are fixed so
that the agent implementation is the only intended variable. Codex uses
Harbor's clean remote `CODEX_HOME`; no local skills are copied into either
participant. `kern-each` also rejects a mismatch between `KERN_MODEL` and
`KERN_EVAL_MODEL` after normalizing the optional `openai/` provider prefix.

After Codex and all ten `<kern-job-prefix>-<task>` jobs have finished, generate
the strict comparison with explicit run identity and output destinations:

```sh
export KERN_EVAL_AGENT_VERSION=sha256:0000000000000000 # replace with the recorded value

uv run --project ./evals/terminal_bench python \
  ./evals/terminal_bench/compare.py \
  --kern-job-prefix "$KERN_EVAL_JOB_PREFIX" \
  --kern-agent-version "$KERN_EVAL_AGENT_VERSION" \
  --json-output ./outputs/terminal-bench-comparison.json \
  --markdown-output ./outputs/terminal-bench-comparison.md
```

Set `KERN_EVAL_AGENT_VERSION` to the exact `agent_info.version` recorded in a
completed Kern child `result.json`. The value is a `sha256:` identifier derived
from the evaluation binary; the comparator confirms that all ten Kern results
record the same value.

The comparison reads the task manifest from `tasks.txt`, exactly ten immediate
trial results from `codex-terminal-bench`, and exactly one immediate trial
result from each `<kern-job-prefix>-<task>` job. The required job prefix is the
portion of those job names before the final `-<task>` suffix. The required
agent version must exactly match every Kern trial's `agent_info.version`, so a
rebuilt binary cannot be compared under a stale version assumption.

The command exits nonzero and writes no report when a job is missing or
running, a task is missing, duplicated, or misidentified, a verifier reward is
missing, paired task checksums differ, or the recorded model identities differ
between agents or tasks.

Scores and exception diagnostics are independent. In particular, an
`AgentTimeoutError` that has verifier reward `0` remains a scored failure;
if the verifier instead records reward `1`, it remains a scored pass. The
exception type remains visible as an independent diagnostic. A completed trial
without a verifier reward is rejected rather than silently removed from the
denominator. Codex tool calls are counted only from structurally valid ATIF
trajectory entries, while Kern tool and safety counts come from its agent
metadata.

Both reports are deterministic and contain only task identity/checksum,
reward/scoring state, exception type, durations, token counts, cost, tool-call
counts, and safety-violation counts. They never copy prompts, tool or command
arguments, captured output, exception messages or tracebacks, trial
configuration, or credentials from the raw Harbor artifacts.

The auxiliary `kern-eval-agent` refuses to run unless `/.dockerenv` exists. It
auto-approves local writes and process commands only because every trial is an
expendable official benchmark container. This matches Codex's unattended
container permissions without weakening the normal Kern CLI approval behavior.
