# MVP security audit

This is an implementation evidence review, not an independent penetration
test. It records what the current tree proves, what is intentionally deferred,
and what still blocks a public security claim.

## Enforced boundaries

| Boundary | Enforcement | Automated evidence |
|---|---|---|
| Local HTTP session | Random token, constant-time comparison, loopback-only first-release binding | `TestServerRejectsUnauthenticatedAPI`, CLI unsafe-listener rejection tests |
| Browser mutation requests | Strict same-site HttpOnly cookie plus Origin validation; bearer clients authenticate explicitly | HTTP mutation and origin tests |
| Web rendering | React text rendering, strict CSP, no external script/style origins, framing denied | Web build, unit tests, HTTP header tests |
| Workspace | Root-relative handles, traversal/symlink/sensitive-path rejection, atomic writes and expected-hash checks | workspace confinement and atomic-write tests |
| Tool authorization | Core classifies effect/risk, persists the exact input hash, waits for a scoped approval receipt, and rechecks before execution | authorization manager, approval receipt, expiry, cancellation, and unapproved-side-effect tests |
| Crash recovery | Side effects are pre-recorded; file writes reconcile pre/target hashes; uncertain external effects pause for a durable human resolution | runtime recovery, hash-conflict, unknown-operation, and idempotency tests |
| Commands | No shell expansion by Core, bounded argv/output/time, process-tree cancellation, isolated HOME/cache/temp, no inherited caller secrets | execute, timeout/descendant, isolated Go build, and secret non-inheritance tests |
| Network tool | HTTP(S) only, standard ports, no URL credentials, local/private targets rejected, bounded response | network target and effect-classification tests |
| Plugin packages | Strict manifest/resources, package digest, size/path/symlink limits, declared executable permissions, immutable installed copy | manifest, bundle, tamper, lifecycle, capability, subprocess, timeout, and circuit-breaker tests |
| Context and prompt injection | Explicit source/trust labels; plugin/tool context phase scoping; raw durable history is not rewritten by summaries | context provenance, compaction, phase grouping, and hidden-plugin-record tests |
| Model credentials | SQLite stores only opaque references; raw-key mutation input is write-only and handed directly to Keychain, Credential Manager or Secret Service through the production system Vault, with bounded readiness/reads, cancellation-independent failed-create compensation and replacement/delete cleanup; values are resolved only at the final adapter; public JSON omits references; an echoed exact key is redacted before normalized text, events, tool arguments, metadata or provider errors | reusable Vault contract, model credential lifecycle including cancellation, HTTP write-only flow, readiness failure, config/runtime selection and compatible-adapter complete/split-stream/tool-call/metadata/error redaction tests |
| WASM plugins | `kern.plugin.abi/v1` modules are import-free, instantiated fresh without WASI, bounded by context, memory pages and output size, and decoded through strict JSON-RPC after package-integrity checks | real wazero guest tests for ABI exchange, import rejection, fresh state, initial/growing memory, request/response ranges, output limits, traps, infinite-loop cancellation and parent Runtime integration |
| Artifact evidence | Task-scoped references, content address, digest verification, bounded storage, attachment response | artifact deduplication/scope/integrity and HTTP download tests |
| Multimodal task input | Four-file/12 MiB aggregate bounds per submission, safe names, MIME allowlist plus signature match, immutable artifact persistence, inherited follow-up references, provider-only materialization | runtime spoof rejection, durable-reference, follow-up inheritance and materialized-image regression tests |
| Resource exhaustion | Task, model, tool, request, event, output, plugin, context, worker, and connection limits | budget, size, timeout, long-event, and bounded-runner tests |
| Parser robustness | Model stream redaction, JSON Schema, plugin manifests, and workspace path confinement have native Go fuzz targets and retained regression seeds | local fuzz campaign plus scheduled `Fuzz` workflow |

CI additionally runs `go vet`, Staticcheck, Govulncheck, race detection, npm
audits, CodeQL, three-OS smoke tests, a scheduled four-target fuzz campaign,
and an offline Base/Go Expert Eval smoke.
Release Actions are pinned to immutable commits.

The 2026-08-24 local fuzz campaign exercised more than seven million generated
inputs across four trust boundaries. It found and retained regressions for a
NUL-containing workspace path and a short secret colliding with the visible
redaction marker; both were fixed before the campaign completed successfully.

## Open security gaps

These items remain incomplete and must not be described as implemented:

1. **Native credential evidence.** The standalone and embedded builds now wire
   the production `go-keyring` Vault for macOS Keychain, Windows Credential
   Manager and Linux Secret Service. Its deterministic backend tests pass the
   reusable `internal/secret/vaulttest` contract, but native write/read/delete
   runs remain gated behind `KERN_TEST_SYSTEM_VAULT=1` and still need retained
   evidence on all three supported operating systems. Headless Linux must prove
   fail-closed unavailability rather than skip the behavior entirely.
2. **WASM platform evidence.** The production wazero adapter and real malicious
   guest fixtures pass locally, including import denial, memory growth, trap and
   infinite-loop cancellation. Hosted Linux, macOS and Windows runs are still
   required before the public release. Instruction-level fuel metering is not
   claimed in v1; cancellation-aware wall-clock enforcement is the boundary.
3. **Process network isolation.** `execute` is an explicitly approved process
   boundary, not an OS sandbox. Kern does not currently prevent an approved
   executable from opening sockets. Users should run Core under a restricted OS
   account; a future runner may add platform sandbox profiles.
4. **Publisher identity.** Local package integrity proves installed bytes have
   not changed; it does not authenticate a publisher. Signing, transparency,
   revocation distribution, and permission-diff approval belong to the plugin
   ecosystem phase.
5. **External review.** The in-repository fuzz campaign is maintainer-run, not
   independent assurance. No independent audit or supported-OS penetration
   test has been completed, and the scheduled hosted campaign cannot be
   evidenced until the repository is pushed.

## Public-release gates

Before calling the MVP security-complete:

- run the native system credential contract on macOS, Windows and desktop
  Linux, plus the unavailable-store case on headless Linux;
- run the real wazero guest isolation suite on hosted macOS, Windows and Linux;
- run hosted CodeQL/Govulncheck/npm workflows from a clean Git repository;
- complete a manual approval/recovery/browser test on macOS, Linux, and Windows;
- triage every high or critical finding before tagging the release.

Publisher signatures and OS-level process network isolation may remain
post-MVP only if their limitations stay explicit in the product and security
documentation.
