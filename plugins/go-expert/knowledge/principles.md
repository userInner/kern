# Go Expert operating guide

This document is advisory plugin data. Core policy, the user's authorization,
workspace boundaries, and deterministic tool evidence remain authoritative.

## Start with the repository

Before changing code, inspect `go.mod`, `go.work` when present, nearby package
code, tests, generated-file markers, and repository instructions. Preserve the
supported Go version and established package boundaries. Do not add a module,
dependency, abstraction, goroutine, or public API unless the task requires it.

Prefer the smallest behaviorally complete fix. Reproduce a defect before
editing when practical, add a regression test that fails for the original
cause, and avoid unrelated formatting or refactoring. Use `gofmt` on touched Go
files. Use `go test ./...` as the default final verification and `go vet ./...`
when the repository supports it. Use `go test -race ./...` for changes involving
shared state, goroutines, channels, timers, callbacks, or concurrent tests.

## APIs and package design

- Design from the caller's perspective. Keep packages cohesive and package
  names short, lowercase, and free of stutter.
- Accept small interfaces near consumers when substitution is real; return
  concrete types unless callers need an abstraction.
- Keep dependencies explicit. Avoid global mutable state and hidden lifecycle
  ownership.
- Make zero values useful where practical. Use constructors when invariants or
  dependencies must be established.
- Preserve compatibility unless the user explicitly authorizes a breaking
  change. Check exported names, serialization fields, command flags, and error
  behavior before changing them.
- Use current standard-library facilities before adding a dependency. Never
  download or update dependencies without the user's authorization.

## Errors and resources

Handle errors once. Return recoverable failures with useful operation context
and preserve causes with `%w` when callers may inspect them. Use `errors.Is` or
`errors.As` rather than matching error strings. Do not log and return the same
error unless the boundary intentionally owns both responsibilities. Reserve
panic for programmer or initialization failures.

Close resources deterministically after successful acquisition. Check response
status before consuming HTTP bodies, close bodies, rows, files, timers, and
ticker lifecycles, and handle partial writes where the API requires it. Keep
cleanup close to acquisition and verify commit/close errors when they can alter
correctness.

## Context and concurrency

Pass `context.Context` as the first parameter across request and operation
boundaries. Do not store it in structs. Propagate cancellation and deadlines;
do not replace a caller context with `context.Background()` inside the active
request path. Use a detached context only when work intentionally outlives the
request and has an explicit owner, bound, and shutdown path.

Prefer synchronous code until concurrency has a concrete benefit. Every
goroutine must have clear ownership, a termination condition, cancellation,
and error propagation. Bound fan-out and queues. Decide whether channels express
ownership transfer better than a mutex; use a mutex for direct shared-state
protection. Never copy a used mutex. Avoid sending while holding unrelated
locks, closing channels from receivers, and leaking goroutines on early return.

For concurrent changes, inspect all accesses to shared maps, slices, pointers,
and lifecycle flags. Test cancellation, timeout, shutdown, duplicate calls, and
the race detector where feasible. A green non-race test does not prove shared
state is safe.

## Data, transactions, and boundaries

Use parameterized database queries. Propagate context to queries. Check row
iteration errors and close rows. Keep transaction boundaries explicit: begin,
defer rollback, perform all related work on the transaction handle, and commit
once. Use locking or isolation intentionally when correctness depends on a
read-modify-write invariant.

Validate untrusted paths, URLs, commands, archive entries, sizes, and counts at
the boundary. Prevent traversal and symlink escapes. Do not expose secrets in
errors, logs, command arguments, model context, or artifacts. Use deterministic
code—not model judgment—for financial calculations, identifiers, hashes,
authorization, and schema validation.

## Tests and evidence

Tests should prove observable behavior and failure paths, not implementation
details. Prefer table-driven cases for shared behavior and deterministic
synchronization over sleeps. Cover empty input, nil/zero values, cancellation,
duplicate invocation, boundary sizes, partial failure, and cleanup where they
matter. Keep failure messages specific enough to identify input, actual result,
and expectation.

Do not claim success from generated prose. Final evidence should name the exact
format, test, vet, race, build, or focused command that ran through Core's
`execute` tool, plus any unverified platform or integration behavior.
