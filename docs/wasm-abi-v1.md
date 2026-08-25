# Kern WASM Guest ABI v1

Status: stable for Kern Core `0.x` plugin prototypes.

This contract defines how an isolated WebAssembly plugin receives the same
versioned JSON-RPC envelope used by a subprocess plugin. It deliberately uses
WebAssembly Core 1.0 primitives rather than WASI, WIT or the Component Model so
the standalone host can grant no ambient filesystem, network, environment,
clock or random capability.

The ABI identifier carried by the Core adapter is `kern.plugin.abi/v1`.

## Required module shape

A v1 module must declare no imports and must export these host-visible members
with compatible Core WebAssembly signatures:

| Export | Kind | Signature | Purpose |
|---|---|---|---|
| `memory` | memory | one linear memory | Request and response transfer |
| `kern_alloc` | function | `(i32 length) -> i32 pointer` | Reserve guest memory for the request |
| `kern_invoke` | function | `(i32 request_pointer, i32 request_length) -> i64 result` | Process one request and locate the response |

Additional exports are allowed and ignored. An imported function, memory,
table or global is incompatible with v1. A module that needs mediated host
capabilities requires a future ABI version; it must not receive them by
accident through the v1 host.

Each invocation gets a fresh module instance. Guest state does not survive a
call, so v1 has no deallocation export.

## Invocation sequence

The sandbox adapter must perform these steps in order:

1. Check the WebAssembly magic/version and compile the module under the
   invocation context and memory ceiling.
2. Reject all imports and instantiate a fresh module without WASI.
3. Validate the required exports and exact function signatures.
4. Call `kern_alloc(request_length)`, then copy the complete JSON-RPC request
   into guest memory at the returned pointer. Address zero is valid; only an
   overflow-safe memory bounds check determines whether the range is valid.
5. Call `kern_invoke(request_pointer, request_length)`.
6. Interpret its `i64` result as unsigned bits:

   ```text
   response_pointer = result >> 32
   response_length  = result & 0xffffffff
   result           = (response_pointer << 32) | response_length
   ```

7. Reject a zero-length response, a response over `MaxOutputBytes`, arithmetic
   overflow, or a range outside the current guest memory.
8. Copy the response into host-owned memory before closing the instance.
9. Return the bytes to Core, which applies strict JSON-RPC decoding and output
   Schema validation.

The request and response are UTF-8 JSON. The request is the same envelope used
for subprocess tools:

```json
{
  "jsonrpc": "2.0",
  "id": "1",
  "method": "invoke",
  "params": {
    "schema_version": "1",
    "tool": "tool-id",
    "input": {}
  }
}
```

The guest must return either a JSON-RPC `result` or `error` response with the
same ID. It must not return both. A business or validation failure belongs in a
JSON-RPC error response; a panic or language trap remains a sandbox trap.

## Mandatory isolation and limits

An adapter is conformant only when it:

- performs a bounded, non-mutating readiness check;
- instantiates without WASI or any host import;
- enforces `Timeout`, `MaxMemoryBytes` and `MaxOutputBytes` from the invocation;
- stops execution when the context is cancelled or its deadline expires;
- uses a fresh instance for every call and closes it on every exit path;
- does not expose host environment variables, arguments, filesystem, network,
  time, randomness or secrets;
- treats module compilation/validation failures as `ErrWASMInvalidModule` or
  `ErrWASMIncompatibleABI`, limit violations as `ErrWASMLimitExceeded`, traps as
  `ErrWASMTrap`, and cancellation/deadline failures as their context errors;
- never logs the request or response body by default.

Core independently limits module and request size, verifies the installed
package before and after execution, validates the JSON-RPC response, and opens
the capability circuit after repeated failures. Those checks supplement the
sandbox boundary; they do not replace it.

## Compatibility

The three export names, signatures, packed result layout and JSON-RPC envelope
are frozen for v1. A future ABI must use a new identifier and explicit plugin
declaration. Core must reject an unknown ABI instead of guessing or silently
falling back.
