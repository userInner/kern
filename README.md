# Kern Core

[![CI](https://github.com/userInner/kern/actions/workflows/ci.yml/badge.svg)](https://github.com/userInner/kern/actions/workflows/ci.yml)
[![CodeQL](https://github.com/userInner/kern/actions/workflows/codeql.yml/badge.svg)](https://github.com/userInner/kern/actions/workflows/codeql.yml)
[![License](https://img.shields.io/github/license/userInner/kern)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/userInner/kern.svg)](https://pkg.go.dev/github.com/userInner/kern)

Kern is a reliable general-agent runtime. It is useful without plugins; plugins add domain depth, workflows, policy rules, and verifiers without unlocking otherwise-blocked task categories.

> **Project status:** Kern is a pre-1.0 developer preview. Its approvals,
> workspace confinement, and operation ledger reduce risk, but Kern is not an
> operating-system sandbox. Run it with a least-privileged account, use trusted
> workspaces, and review every requested side effect.

This repository currently contains the first runnable vertical slice:

- durable Task and Attempt lifecycle;
- complexity-aware durable plans with auditable step state and retry counts;
- SQLite WAL snapshots, append-only events, and versioned Goose migrations;
- bounded background execution with exclusive leases, heartbeats, and recovery attempts;
- pause, resume, cancel, retry, and durable checkpoints;
- idempotent Operation records plus approval requests and immutable receipts;
- deterministic operation policy plus exact-scope interactive approvals;
- content-addressed, integrity-checked task artifacts and downloads;
- hash-checked workspace create, edit, and non-overwriting move operations with bounded diff evidence;
- provenance-labelled durable context with bounded active windows and phase summaries;
- deterministic completion verification;
- an OpenAI-compatible model adapter and an explicit offline baseline;
- task-scoped multimodal image inputs backed by immutable artifacts;
- versioned declarative plugins with integrity checks, deterministic activation, and additive verifiers;
- the bundled Go Expert reference plugin;
- isolated Base/Plugin evaluation runs with deterministic graders and comparison reports;
- local HTTP/JSON API with authenticated SSE replay;
- a CLI and an embedded React Web workbench.

The requirement-by-requirement implementation evidence and remaining release
gates are maintained in [`docs/completion-audit.md`](docs/completion-audit.md).

The current slice proves the runtime-reliability and reference-client path. Model-backed tasks receive four bounded high-level tools: `inspect`, `change`, `execute`, and `capability`; public network reads are an audited `inspect` action rather than an unrestricted fifth tool. Policy automatically permits bounded reads, asks for exact-scope approval before writes, commands, or network access, and stores immutable decision receipts. Raw tool output and file diffs are stored as task-scoped, SHA-256-addressed artifacts; the Web task page renders diffs inline and exposes every artifact for download. Durable context records retain source and trust labels while the active model window is bounded and summarized without rewriting raw history. Completion is independently checked against the operation ledger, approval receipts, command exit codes, current workspace hashes, diff artifacts, requested JSON format, external side effects, and additive plugin verifiers. Plugins cannot alter Core policy, gain undeclared permissions, or block Kern's general fallback.

## Quick start

Requirements: Go 1.26.6+. Node.js 22+ is only required when rebuilding the Web
client.

```sh
git clone https://github.com/userInner/kern.git
cd kern
make build
./bin/kern web
```

The repository includes the generated Web assets used by the Go binary. To
change the React client, rebuild and verify those assets before compiling Core:

```sh
make web
make build
```

Kern opens at <http://127.0.0.1:8787>. Local state is stored in the operating system's user config directory. Use an isolated directory during development:

```sh
./bin/kern web --data-dir ./data --workspace . --open=false
```

Run one task entirely from the CLI:

```sh
./bin/kern run --data-dir ./data --workspace . "Inspect this project and explain what makes its task results verifiable"
```

Inspect the durable task ledger or diagnose a local installation without opening
the browser:

```sh
./bin/kern task list --data-dir ./data
./bin/kern task show --data-dir ./data TASK_ID
./bin/kern doctor --data-dir ./data --workspace . --output json
```

`doctor` keeps Core health (`status`) separate from optional production-adapter
readiness. Its JSON reports whether model execution is configured and whether a
writable system credential store and executable WASM runtime are actually
connected; missing adapters remain explicit warnings instead of being hidden
behind an overall healthy SQLite/workspace result.

Use one durable task as a multi-turn conversation. Every follow-up creates a
new Attempt, inherits the prior immutable context, and records the new message
with explicit `user_input` provenance:

```sh
./bin/kern chat --data-dir ./data --workspace .
./bin/kern chat --once "Summarize this repository"
```

## Configuration

Kern loads a strongly typed JSON configuration using the precedence
`flags > environment > config file > safe defaults`. Find and inspect it with:

```sh
./bin/kern config path
./bin/kern config get
./bin/kern config set runtime.max_active_tasks 4
./bin/kern config set runtime.task_timeout 20m
./bin/kern config set policy.profile read-only
./bin/kern config set storage.retention_days 30
./bin/kern config get --effective --output text runtime.task_timeout
```

Writes are validated, atomically replaced, and restricted to the local user.
`KERN_CONFIG` selects another file. Runtime settings also accept the matching
environment overrides such as `KERN_DATA_DIR`, `KERN_WORKSPACE`,
`KERN_MAX_TURNS`, `KERN_MAX_TOOL_CALLS`, `KERN_MAX_TOKENS`,
`KERN_MAX_COST_USD`, and `KERN_TASK_TIMEOUT`.

Every model-backed task has finite defaults for model turns, tool calls,
input/output tokens, provider-reported cost, and model execution time. Override
them for a run or Web session when needed:

```sh
./bin/kern run \
  --max-turns 16 \
  --max-tool-calls 48 \
  --max-tokens 250000 \
  --max-cost-usd 8 \
  --max-duration 15m \
  "Run the repository checks and repair deterministic failures"
```

Budget exhaustion is persisted as a task event and shown in the Web evidence
rail. Cost enforcement uses `cost_micros` reported by the configured provider;
providers that omit cost still receive turn, tool, token, and duration limits.

The Web **Settings** panel can update the same task budgets, timeout, retention
window, and one of three Core policy profiles for executions that start after
the save:

- `local-safe` automatically allows bounded workspace reads and asks before
  file changes, commands, network access, or higher-risk effects;
- `confirm-all` also asks before bounded reads;
- `read-only` denies changes, commands, network access, and other non-read
  effects.

Changing settings never rewrites a running Attempt or its approval history.
The panel previews expired terminal-task data before cleanup and requires an
explicit confirmation before deleting it. Cleanup only targets completed,
partially completed, failed, or cancelled tasks older than the configured
retention window; content-addressed artifact files remain while any surviving
task still references them.

Set `observability.metrics_enabled` to `true` (or
`KERN_METRICS_ENABLED=true`) to expose authenticated Prometheus metrics at
`GET /metrics`. Counts, latency histograms, Token and cost totals are rebuilt
from the durable SQLite ledger after restart; only process and connection-pool
gauges are live. Labels use fixed Core vocabularies and never contain Task IDs,
paths, URLs, prompts, or plugin IDs. Every Task and SSE event also carries a
stable, opaque Attempt `trace_id`; HTTP requests accept and return W3C
`Traceparent` headers, and JSON logs include request or Attempt trace identity.
The complete metric catalog and trace contract are documented in
[`docs/observability.md`](docs/observability.md).

Without model configuration Kern visibly runs in `offline-baseline` mode. This mode is a deterministic runtime probe, not a simulated LLM.

New tasks can include up to four PNG, JPEG, WebP, or GIF images from the Web
composer or either HTTP SDK. Core validates the declared MIME type against the
file signature, limits each decoded image to 8 MiB and the task total to 12
MiB, and stores the original bytes as task-scoped content-addressed artifacts.
Durable context contains only artifact references; model-backed execution
materializes bounded `data:` image blocks immediately before the provider call.
Follow-up Attempts can add new images while inheriting earlier artifact
references. In the Web workbench, attaching an image never changes whether the
selected action continues the current task or starts a new one.

## Plugins

Kern is fully usable without plugins. A plugin adds bounded knowledge,
professional workflows, quality/risk rules, and deterministic verification to
matching tasks. Install the bundled Go Expert locally:

```sh
./bin/kern plugin digest ./plugins/go-expert
./bin/kern plugin install --enable ./plugins/go-expert
./bin/kern plugin inspect --output json dev.kern.go-expert
./bin/kern run --plugin dev.kern.go-expert "Fix the failing Go tests"
```

The Web workbench provides the same install, enable, disable, and remove
lifecycle. Its three-state new-task selector can follow automatic signals,
force a plugin for one task, or explicitly suppress it. Every Attempt records
the selected plugin ID, exact version, package digest, activation reason, and
resource IDs. A missing, corrupt, incompatible, or conflicting plugin is
reported and safely skipped while the general Agent continues.

Plugin resources are activated by a Core-owned `prepare → execute → verify`
state machine. The model sees only the current phase's workflow steps and
rules: prepare exposes `inspect` and `capability`; execute exposes all four
Core tools; verify withholds file changes while retaining inspection, commands,
and audited capabilities. `capability get_bundle` is reconstructed from the
immutable phase audit rather than returning the complete installed package.
Phase-scoped plugin and tool-result context is excluded before context
budgeting and summarization, and is not inherited by a later Attempt.

Executable WASM plugins use the import-free, JSON-RPC-based
[`kern.plugin.abi/v1`](docs/wasm-abi-v1.md) guest contract. The standalone and
embedded builds execute it through a fresh, bounded wazero sandbox after a
startup probe; declarative plugins and Core's general fallback remain usable
when the adapter is unavailable.

`plugins/go-expert` supplies repository-aware repair, implementation, and
concurrency workflows; 15 Go lifecycle and security rules; and exact `go test`,
`go vet`, and race-detector evidence. Its command verifiers never execute by
themselves—they only accept matching successful operations that already passed
through Core's policy and approval ledger.

## Eval

Eval suites fix the fixture, prompt, variants, budgets, retry policy, allowed
grader commands, and deterministic graders. Every Case/Variant/Attempt receives
an independent copied workspace. Network operations and commands outside the
suite allowlist are denied and counted as safety violations.

```sh
./bin/kern eval validate ./evals/go
./bin/kern eval run --data-dir ./data ./evals/go
./bin/kern eval compare --data-dir ./data --variants general.base,expert.go ./evals/go
./bin/kern eval show --data-dir ./data RUN_ID
```

An optional authenticated Codex CLI baseline can be added without placing its
credentials in the suite:

```sh
./bin/kern eval compare --codex --data-dir ./data ./evals/go
```

This runs `codex exec` once per selected Case in a copied workspace, consumes
the signed-in Codex account's usage, records the exact CLI version, and uses
automatic approval review only inside Codex's `workspace-write` sandbox. Kern
never passes the dangerous sandbox-bypass flag. Authenticate beforehand with
`codex login`: the adapter passes only a small operating-system and persisted-
login environment allowlist, so ambient API keys and unrelated caller secrets
do not enter Codex or its model-generated child processes. Real coding agents
can consume substantial cached-input tokens, so the release suites use a
200,000-token per-case ceiling even when the deterministic grader is small.

`eval validate` does not call a model or create a run. It strictly checks the
Suite contract, every prompt and fixture, selected Variant IDs, and referenced
plugin package integrity, then prints the immutable configuration digest that
a later run must preserve.

Reports retain every attempt and summarize first-attempt success rate with a
95% Wilson interval, score, tokens, cost, duration, tool calls, retries, human
intervention, safety violations, and per-case improvements or regressions.
Because variants share Cases, each comparison also records an exact two-sided
McNemar p-value over first-attempt paired outcomes and marks a success-rate
improvement significant only when the candidate has more wins and `p < 0.05`. The
canonical v1 suite file is `suite.json`; fixture paths resolve beneath the
suite directory and v1 fixtures are directories rather than mutable shared
workspaces.

CI runs `evals/smoke` in both Base and Go Expert modes without network access.
This one-case suite verifies the runner, isolated workspace, bundled-plugin,
grader, and comparison-report path; it is not evidence of Expert quality. The
30-case `evals/go` suite with a fixed real model is the release-quality
comparison.

Long evaluation runs can be paused, durably resumed, or cancelled from the Web
workbench and HTTP API. Resume refuses to mix results when the suite, prompt,
fixture, plugin, model, or grader identity changed. Suites may also configure a
non-required OpenAI-compatible model grader for bounded supplemental judgment;
every model-graded case must retain a required deterministic grader, and only
explicit evidence paths enter the judge request.

## Connect a model

Kern accepts an OpenAI-compatible chat endpoint:

```sh
export KERN_MODEL_BASE_URL=http://127.0.0.1:11434
export KERN_MODEL=qwen3
export KERN_MODEL_API_KEY=
./bin/kern web
```

The adapter calls `${KERN_MODEL_BASE_URL}/v1/chat/completions`. Secrets are read
from the environment and are not exposed through the task API, events, or Web
client. If a broken or malicious provider echoes its own API key, the adapter
replaces that exact known value before normalized text, reasoning, tool-call
arguments, request metadata, or provider errors can enter Kern. Streaming
redaction also recognizes a key split across arbitrary provider deltas.

The Web workbench can also save multiple OpenAI-compatible or Ollama
connections and select one per task. SQLite stores the endpoint, model, and an
opaque credential reference. `env:NAME` remains available everywhere; the
standalone and embedded builds hand a write-only API key directly to Keychain,
Credential Manager or Secret Service and SQLite receives only its `keyring:`
reference. The Web disables the raw-key field when the native service is not
ready. Every Attempt
stores a secret-free snapshot of the provider, endpoint, model, and execution
parameters so later configuration changes do not rewrite its audit history.

## Task control

The Web workbench exposes actions valid for the selected task state. API clients can use the same durable controls:

```text
POST /api/v1/tasks/{task_id}/pause
POST /api/v1/tasks/{task_id}/resume
POST /api/v1/tasks/{task_id}/cancel
POST /api/v1/tasks/{task_id}/retry
POST /api/v1/tasks/{task_id}/messages
GET  /api/v1/tasks/{task_id}/artifacts
GET  /api/v1/tasks/{task_id}/artifacts/{artifact_id}
GET  /api/v1/tasks/{task_id}/verifications
```

Each verifier stores a summary and durable evidence references, followed by a
`core.aggregate` conclusion. Required failures fail the task; incomplete or
manual-only coverage produces `partially_completed`; only independently
verified coverage produces `completed`. The Web workbench renders every check
instead of treating the Agent's final prose as proof of success.

Resume and retry always create a new Attempt. On startup, Kern detects tasks whose execution lease expired and marks interrupted tool operations as `unknown`. Atomic file writes persist their pre-write and target hashes before execution: recovery marks the operation succeeded when the current hash matches the target, safely retries through a new Attempt when it still matches the pre-write state, and pauses on any third hash. File moves persist the normalized source, destination, and approved source hash; recovery distinguishes the untouched state from the completed move and treats every duplicate, missing, or hash-divergent layout as a conflict. Other uncertain side effects remain durably paused instead of being replayed. The Web recovery card shows the exact tool input and idempotency key; the local user must confirm either that the operation succeeded or that it never executed. Kern stores automatic and human conclusions as immutable resolution receipts and refuses to resume while any uncertain operation remains. Network writes, destructive tools, and tools without an explicit reconciler are never automatically retried.

## Development

```sh
make test
make race
make check
FUZZTIME=30s make fuzz
```

Pull requests run formatting, dependency-integrity, static-analysis, unit,
race-detector, reproducible-build, Web/SDK, and Linux/macOS/Windows smoke gates.
CodeQL scans both Go and JavaScript/TypeScript. Version tags produce six
cross-platform archives plus checksums, an SPDX SBOM, and repository-bound
artifact attestations; see [`docs/releasing.md`](docs/releasing.md).

Data upgrades create a consistent pre-migration backup as described in
[`docs/data-migrations.md`](docs/data-migrations.md). Public boundary guarantees
are listed in [`docs/compatibility.md`](docs/compatibility.md), and the additive
plugin contract is documented in
[`docs/plugin-authoring.md`](docs/plugin-authoring.md).
The current implementation evidence and unresolved security gaps are tracked in
[`docs/security-audit.md`](docs/security-audit.md). Public release candidates
must also complete the three-platform evidence matrix in
[`docs/manual-validation.md`](docs/manual-validation.md).

## SDKs

The dependency-free Go client lives at `sdk/kern` and is imported as
`github.com/userInner/kern/sdk/kern`. It covers task submission and control,
plans, approvals, uncertain-operation recovery, artifacts, verification,
model connections, runtime settings, retention cleanup, plugin and evaluation
lifecycle, health probes, metrics, and replayable SSE event streams.

Go applications can also host Core in their own process with `sdk/embedded`.
The package owns the durable runtime and an authenticated ephemeral loopback
transport, then returns the same typed client used by a standalone server. This
keeps embedded and remote integrations on one public contract without exposing
internal storage or engine types:

```go
runtime, err := embedded.Open(ctx, embedded.Config{
    DataDir:      "./kern-data",
    WorkspaceDir: ".",
})
if err != nil {
    return err
}
defer runtime.Close()

task, err := runtime.Client().CreateTask(ctx, kern.CreateTaskInput{
    Goal: "Inspect this repository and report verifiable risks",
})
```

Cancelling the host context closes the embedded server and Core. `Errors()`
reports unexpected transport failure; normal shutdown closes it without a
value. The listener is always IPv4 loopback on an ephemeral port, and its
random bearer token is retained only by the returned client.

Embedded hosts may register a `ModelProvider` (and optionally the streaming
interface) using Kern's provider-neutral messages, tool calls, usage, and
classified errors. Trusted host integrations can also register capabilities
under the reserved `host.` namespace. They remain behind the single
model-facing `capability` tool: JSON input is schema-validated, the declared
effect is persisted, and Core policy plus exact-scope approval runs before host
code is invoked. Registered capabilities therefore do not add an unrestricted
fifth tool or bypass the operation ledger.

The TypeScript package lives at `sdk/typescript` and exposes the same public
HTTP surface through `KernClient`, including an async event iterator that
reconnects from the last observed event ID. Both clients expose cleanup as an
explicit `CleanupExpired` / `cleanupExpired` operation; callers should show
their own confirmation UI after first calling the non-mutating preview.

```sh
make sdk
```

The Vite development server proxies `/api` to a Kern server on port 8787:

```sh
go run ./cmd/kern web --open=false
cd web && npm run dev
```

## Architecture

```text
CLI / Web / SDK
      │
HTTP commands + persisted SSE events
      │
Task engine ── Processor (offline or model)
      │
SQLite snapshot + event log
      │
Deterministic verifier
```

The dependency direction is `transport/adapters → application → domain`. Interfaces are defined at their consumers and the composition root uses explicit constructor injection.

## Security defaults

- Web binds to a loopback address only.
- A random session token is stored in an HttpOnly, SameSite=Strict cookie.
- Browser mutations require a matching Origin; API clients can use a bearer token.
- Request and model-response bodies have hard size limits.
- CSP, frame, referrer, and MIME-sniffing protections are set on all responses.
- Database calls carry context and all SQL values are parameterized.

## Project documentation

The implemented scope and remaining release evidence are tracked in
[`docs/completion-audit.md`](docs/completion-audit.md). Security boundaries and
known gaps are documented in [`docs/security-audit.md`](docs/security-audit.md),
while public compatibility commitments live in
[`docs/compatibility.md`](docs/compatibility.md). The HTTP contract is defined
in [`api/openapi.yaml`](api/openapi.yaml).

## Contributing

Bug reports, focused fixes, tests, evaluation cases, SDK improvements, and
bounded plugins are welcome. Start with
[`CONTRIBUTING.md`](CONTRIBUTING.md) and keep Core's policy and evidence
boundaries explicit in every change.

## License

Kern is licensed under the [Apache License 2.0](LICENSE). Third-party
attributions for distributed components are recorded in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
