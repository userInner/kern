# Embedded Kern Core

`sdk/embedded` runs the durable Kern runtime inside a Go host process and
returns the same dependency-free `sdk/kern` client used for a standalone Core.

```go
runtime, err := embedded.Open(ctx, embedded.Config{
    DataDir:      "./kern-data",
    WorkspaceDir: ".",
})
if err != nil {
    return err
}
defer runtime.Close()

client := runtime.Client()
created, err := client.CreateTask(ctx, kern.CreateTaskInput{
    Goal: "Run the repository checks and explain the evidence",
})
```

Image inputs use the same task API and remain immutable artifacts rather than
large base64 records in durable context:

```go
created, err := client.CreateTask(ctx, kern.CreateTaskInput{
    Goal: "Describe the relevant UI state",
    Attachments: []kern.TaskAttachment{{
        Name: "screen.png", MediaType: "image/png", Data: pngBytes,
    }},
})
```

The internal transport binds only to an ephemeral IPv4 loopback port. A random
bearer token stays inside the returned typed client. This lets the package keep
one stable API for embedded and remote callers while Core retains ownership of
SQLite, workers, plugins, approvals, artifacts, and recovery.

Use `Client().OpenEventStream` for replayable task events and the ordinary
client methods for approvals, artifacts, verifications, plugins, model
connections, and evaluations. Cancelling the context passed to `Open` closes
the runtime. `Close` is concurrency-safe and idempotent.

Runtime settings changed through the client are atomically stored at
`<DataDir>/runtime-config.json` and loaded by the next embedded `Open`. Set
`Config.SettingsPath` to share a different settings file. Explicit non-zero
budget, timeout, policy, or retention values in `Config` take precedence over
the stored value for that process.

## Custom model provider

Set `Config.ModelProvider` to an implementation of `ModelProvider`. Implement
`StreamingModelProvider` on the same value when the provider supports streams.
Kern supplies public aliases for its provider-neutral request, response,
message, tool-call, stream, usage, capability, and classified-error types.

## Trusted host capabilities

`Config.Capabilities` registers trusted integration code beneath the existing
`capability` tool. Provider IDs use the reserved `host.` prefix. Each
capability declares a strict object JSON Schema and its maximum effect:

```go
Capabilities: []embedded.Capability{{
    ProviderID:   "host.example",
    ProviderName: "Example",
    ID:            "lookup",
    Description:   "Look up one record.",
    InputSchema:   json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`),
    Effect:        embedded.CapabilityNetworkRead,
    Handler: func(ctx context.Context, input json.RawMessage) (embedded.CapabilityResult, error) {
        // This function runs only after schema validation, Operation
        // persistence, Core policy, and any required user approval.
        return embedded.CapabilityResult{Content: `{"found":true}`}, nil
    },
}},
```

Host capability code runs in the embedding process and is therefore trusted
application code, not a replacement for isolated third-party plugins.
