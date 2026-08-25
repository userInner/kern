# Kern Cross-domain Agent Benchmark

This suite compares general-purpose coding agents across nine isolated tasks:

- Frontend: streamed SSE decoding, optimistic chat reconciliation, and stale async search suppression.
- Backend: concurrent idempotent transfers, signed webhook verification, and request-coalescing cache behavior.
- Mathematics: exact monetary allocation, numerically stable probability functions, and cent-exact amortization.

Every case uses the same immutable fixture and prompt for every variant. A case passes only when:

1. Its deterministic behavior tests pass.
2. The patch stays inside the declared production source files.
The report separately records first-attempt success, mean grader score, elapsed time, tokens, cost, tool calls, retries, available safety telemetry, and paired improvements or regressions. Safety telemetry is not a cross-agent pass gate because external adapters do not yet expose equivalent denied-operation evidence. No model judge or subjective visual score is used in version 0.1.0.

Run Kern against the local Codex CLI:

```sh
./bin/kern eval compare \
  --codex \
  --variants general.base \
  --data-dir ./data \
  ./evals/cross-domain
```
