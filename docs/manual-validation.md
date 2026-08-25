# Supported-platform manual validation

Complete this checklist on macOS, Linux, and Windows before tagging a public
release. Automated tests remain mandatory; this records the user-visible and
OS-integration evidence that CI cannot prove.

Use a fresh checkout of the exact release commit. Never reuse a production
workspace or credential. Record the OS version, architecture, Go/Node version,
commit SHA, model provider and model ID, but never record a secret value.

## Evidence record

For every platform retain:

- `kern version` and `kern doctor --output json` output;
- the result of `make check` and `make race`;
- screenshots of the task, approval, recovery, verification, plugin, eval and
  settings surfaces;
- the exported task and evaluation identifiers needed to reproduce the local
  SQLite evidence;
- failures, workarounds and the issue or commit that resolved each one.

Mark a platform complete only when every required row below passes on the same
release candidate.

## Matrix

| Area | Required check | Pass condition |
|---|---|---|
| Install | Build or unpack the release archive and run `kern version` | Version equals the immutable release tag |
| Diagnostics | Run `kern doctor --output json` with a fresh data directory | Store and workspace checks pass without exposing secrets; `production_adapters_ready` is true for a release candidate and every warning is explained or resolved |
| Web | Start `kern web`, open the loopback URL and exercise desktop plus narrow mobile layout | Task ledger, evidence rail and panels remain usable with no browser errors |
| Model | Save an OpenAI-compatible or Ollama connection by environment-variable reference and test it | Probe reports bounded metadata only; API and logs contain no key or secret reference |
| Multimodal | Attach a valid image to a new task, complete it, then continue the same task with a second image; also try a spoofed MIME type and an oversized file | The Web action remains “continue task”; both inherited and new images reach the next model request as immutable Artifacts, invalid inputs are rejected before a new Attempt, and no base64 payload enters durable context |
| Task | Run a read-only task, then a file-change task in a disposable workspace | Both reach a terminal state with an independent verification result |
| Approval | Approve one exact file write and deny a second request | Only the approved operation executes; both immutable receipts appear in evidence |
| Recovery | Interrupt a task at an operation boundary using the documented test harness and reopen the same data directory | Confirmed operations are not repeated; uncertain operations require an explicit resolution |
| Conflict | Modify a target file after interruption and before recovery | Kern reports the hash conflict and does not overwrite the user's version |
| Settings | Change budgets, policy and retention, restart Kern and reopen Settings | New executions use persisted values; the prior Attempt remains unchanged |
| Cleanup | Preview expired disposable tasks, confirm cleanup and reopen the ledger | Only eligible terminal tasks disappear; shared artifacts remain while referenced |
| Plugin | Install, enable, run and remove the bundled Go Expert | Exact plugin version/digest is recorded; removal does not rewrite historical evidence |
| Eval | Run the offline smoke comparison | Base and Go Expert jobs use isolated workspaces and produce a report |
| Shutdown | Stop the Web server and an embedded host with active SSE clients | Processes, child commands, listeners and event streams terminate within their bounds |

## Platform-specific secret store checks

The production system credential adapters are connected. Run:

```sh
KERN_TEST_SYSTEM_VAULT=1 go test ./internal/secret/systemvault -run NativeVaultContract -v
```

Before collecting platform evidence, run the adapter's Go integration test
against `internal/secret/vaulttest.Run`. Skipping it because the desktop secret
service is absent is acceptable only for the documented Linux headless case;
the corresponding unavailable-store behavior must then pass instead.

- macOS: save, resolve, replace and delete a test item in Keychain; confirm the
  value never enters SQLite, Web JSON, logs or events;
- Windows: repeat the lifecycle with Credential Manager under a non-admin user;
- Linux: repeat with Secret Service in a normal desktop session, and verify the
  documented unavailable-store error in a headless session; `doctor` and the
  Web model form must both report the backend as unavailable without disabling
  `env:NAME` references.

Use a randomly generated throwaway value and delete it after the evidence is
captured. Screenshots and logs must show only the reference and redacted state.

## WASM sandbox checks

The production wazero host is connected. Run the automated package first, then
repeat the user-visible plugin flow with a signed fixture:

```sh
go test ./internal/pluginruntime/wazerosandbox -v
```

Confirm that the fixtures:

- fail the adapter readiness probe and confirm `doctor`, plugin activation and
  the capability tool all report the runtime unavailable without invoking it;
- complete a bounded JSON-RPC request;
- loop forever, allocate beyond the memory limit, trap and return malformed
  output;
- attempt undeclared filesystem, environment, clock, random and network access;
- ignore cancellation and exceed wall-clock limits.

Only the bounded fixture may succeed. Every rejected fixture must produce a
classified, content-bounded failure without destabilizing Core or another task.

## Sign-off

The release owner links the three completed evidence records from the release
notes and confirms that every high or critical finding has been fixed or the
release has been stopped. Follow [`releasing.md`](releasing.md) only after this
matrix and [`security-audit.md`](security-audit.md) are green.
