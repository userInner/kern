# Compatibility policy

Kern is pre-1.0. Schema boundaries are versioned even though source-level APIs
may still change as real integrations provide feedback.

| Boundary | Current contract | Compatibility behavior |
|---|---|---|
| HTTP API | `/api/v1`, OpenAPI 3.1 | Existing v1 fields are additive where practical; incompatible changes require a new API version. |
| SSE events | `schema_version: "1"` | Consumers must ignore unknown event types and payload fields. Event replay cursors remain monotonic per database. |
| Task data | SQLite migrations | Upgrades are forward-only and create a consistent pre-migration backup. Newer schemas are refused by older binaries. |
| Plugin manifest | `schema_version: "1"` | Unknown fields and incompatible Core ranges are rejected rather than guessed. |
| Go SDK | `sdk/kern` | Tracks the v1 HTTP contract; source stability is not promised until external consumers validate it. |
| TypeScript SDK | `@userinner/kern-sdk` 0.x | Tracks the same HTTP/SSE contract and follows semantic versioning within the 0.x rules. |
| Embedded SDK | `sdk/embedded` | Uses the same authenticated loopback API; internal storage and engine types are not public. |

The CLI prints its build tag through `kern version`. Eval reports capture the
Core, Go, OS/architecture, model, plugin digest, fixture digest, prompt digest,
budget, retry, and grader identities required to reproduce a result.

See `docs/data-migrations.md` for rollback boundaries and
`docs/plugin-authoring.md` for plugin compatibility rules.
