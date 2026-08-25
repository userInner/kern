# Kern Core completion audit

Last local audit: 2026-08-24.

This document maps the MVP product requirements to authoritative implementation
and verification evidence. A green test alone is not treated as proof unless it
exercises the named requirement. Hosted and manual gates remain incomplete
until their evidence exists outside the local worktree.

## Core requirement evidence

| Requirement | Status | Implementation evidence | Verification evidence |
|---|---|---|---|
| Task and Attempt lifecycle | Complete locally | `internal/task`, `internal/engine`, `internal/store/sqlite` | state-transition, pause/resume/cancel/retry, follow-up Attempt and recovery tests |
| SQLite durability | Complete locally | WAL store, leases, heartbeats, checkpoints and versioned migrations in `internal/store/sqlite` | migration, concurrency, lease expiry, backup and reopen tests |
| Durable events and SSE replay | Complete locally | event ledger and bounded authenticated SSE in `internal/store/sqlite` and `internal/transport/httpapi` | replay cursor, long event, connection limit, shutdown and cross-restart tests |
| Model gateway | Complete locally | `internal/model`, OpenAI-compatible adapter and offline baseline | streaming text/reasoning/tool calls, usage/cost, multimodal and split-secret redaction tests |
| General Agent loop | Complete locally | `internal/agent`, `internal/planner`, `internal/executionphase` | multi-turn tool loop, dynamic phase/plan, budget exhaustion and deterministic fake-model end-to-end tests |
| Core tools | Complete locally | `inspect`, `change`, `execute`, `capability` in `internal/tool/builtin` | workspace/network/command/plugin action tests including bounded output and cancellation |
| Policy and approval | Complete locally | `internal/policy`, `internal/authorization`, approval store | effect/risk classification, exact input hash, expiry, deny/approve and immutable receipt tests |
| Side-effect reliability | Complete locally | operation pre-recording, idempotency and reconciliation in app/store layers | crash boundary, unknown effect, deterministic hash recovery, conflict and human resolution tests |
| Workspace safety | Complete locally | root-relative handles, atomic writes/moves, expected hashes and diffs in `internal/workspace` | traversal, symlink, sensitive path, stale hash, atomicity and fuzz regressions |
| Artifact store | Complete locally | content-addressed task artifacts in `internal/artifact` | deduplication, scope, integrity, download, cleanup-reference and attachment tests |
| Context builder | Complete locally | durable provenance records, bounded windows and summaries in `internal/contextbuilder` | trust/source retention, compaction, phase isolation and Attempt inheritance tests |
| Verification | Complete locally | Core and plugin verifier suites in `internal/verifier` and `internal/pluginverifier` | command, file hash, diff, JSON Schema, external effect, plugin and failure-result tests |
| Web Console | Complete locally | embedded React workbench in `web` and HTTP API | Web unit tests plus task, approval, recovery, models, settings, plugin, eval and artifact HTTP tests |
| CLI | Complete locally | `kern run/chat/task/plugin/eval/config/doctor/web` in `internal/transport/cli` | command flow, durable chat, configuration, diagnostics and shutdown tests |
| Declarative plugin lifecycle | Complete locally | manifest, digest, install/remove, activation and phase resources in plugin packages | tamper, compatibility, conflict, fallback, permission and lifecycle tests |
| Go Expert | Complete locally | `plugins/go-expert` rules, workflows and verifiers | package integrity validation and offline Base/Expert smoke suite |
| Eval system | Complete locally | suite/case/variant runner, pause/resume, Codex adapter and reports | isolation, deterministic graders, retry, safety, resume digest and exact McNemar tests |
| Go and TypeScript SDKs | Complete locally | `sdk/kern`, `sdk/embedded`, `sdk/typescript` | Go package tests and TypeScript HTTP contract tests |
| Observability | Complete locally | durable metrics, trace context and structured logs | restart reconstruction, bounded labels, W3C propagation and metrics endpoint tests |

`make check` proves formatting, dependency integrity, Go vet, Staticcheck,
Govulncheck, all Go tests, Eval asset validation, Web build/tests/npm audit and
TypeScript SDK build/tests/npm audit. `make race` separately proves the current
Go test suite under the race detector. Both passed in the local audit above.

## Incomplete production and release evidence

| Gate | Current evidence | Missing proof |
|---|---|---|
| System credential store | Production `go-keyring` Vault is wired into standalone/embedded Core; startup readiness, fail-closed resolution, lifecycle compensation and deterministic `internal/secret/vaulttest` contract are green | Native contract evidence on Keychain, Credential Manager and desktop Secret Service; headless Linux unavailable-store evidence |
| WASM sandbox | Production wazero host is wired; real `kern.plugin.abi/v1` guests prove import denial, fresh instances, memory/time/output enforcement, trap handling, cancellation and parent Runtime integration | Hosted macOS/Linux/Windows test evidence and one signed-plugin user-flow record |
| Go Expert quality claim | Offline one-case Base/Expert smoke proves the runner path | Fixed real-model 30-case run with paired report and retained model identity/cost evidence |
| Hosted CI and security | Workflows for CI, CodeQL, fuzzing and releases exist locally | Initial Git history/remote push and green hosted runs from a clean repository |
| Supported platforms | Six cross-build targets and local macOS automation pass | Manual macOS/Linux/Windows evidence matrix, including native credential services and browser behavior |
| Public release | Reproducible-build, SBOM, checksum and attestation workflow is defined | Signed semantic tag, six published archives, verified checksums/attestation and release notes |
| Independent assurance | Maintainer fuzz campaign and in-repository audit are recorded | Independent security review or penetration test; required before making an external assurance claim, not before an explicitly limited MVP preview |

## Explicitly post-MVP

Multi-Agent orchestration, self-modifying evolution, plugin publisher identity,
transparency/revocation infrastructure, a plugin marketplace and OS-level
network isolation for approved child processes are product follow-ups. Their
absence must remain visible, but they do not redefine the single-Agent,
local-first Core MVP acceptance criteria.
