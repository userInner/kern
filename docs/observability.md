# Kern observability

Kern treats the durable task ledger as the source of truth for operational
metrics. Restarting the process does not reset task, model, operation,
approval, recovery, plugin, verification, or evaluation counters. Only live
process gauges, such as goroutines and database connections, are ephemeral.

## Enable metrics

Metrics are disabled by default and are exposed only on Kern's loopback HTTP
server. Enable them in the configuration file:

```json
{
  "observability": {
    "log_level": "info",
    "metrics_enabled": true
  }
}
```

`KERN_METRICS_ENABLED=true` has higher precedence than the configuration file.
The endpoint is `GET /metrics` and requires the same session cookie or bearer
token as the task API. A disabled endpoint returns `404`; a request without a
valid session returns `401`. Responses are never cached.

The Go and TypeScript SDKs expose this endpoint through `Client.Metrics` and
`KernClient.metrics` respectively.

## Metric families

| Family | Meaning |
|---|---|
| `kern_tasks_created_total` | Tasks persisted since the database was created |
| `kern_tasks_current` | Current Task snapshots by state |
| `kern_attempts_total` | Current or final state of every durable Attempt |
| `kern_task_duration_seconds` | Finished Attempt duration histogram |
| `kern_model_calls_started_total` | Calls recorded before provider I/O |
| `kern_model_calls_total` | Completed model calls by success or failure |
| `kern_model_call_duration_seconds` | Provider-call latency histogram |
| `kern_model_tokens_total` | Input, output, reasoning, and cached Token totals |
| `kern_model_cost_usd_total` | Provider-reported cost; absent cost remains zero |
| `kern_model_retries_total` | Explicitly classified retryable model failures |
| `kern_operations_total` | Operations by Core tool, effect, and final/current state |
| `kern_operation_duration_seconds` | Tool execution latency histogram |
| `kern_operations_unknown` | Outcomes requiring reconciliation or human confirmation |
| `kern_approval_requests_total` | Approval requests by bounded risk and state |
| `kern_approval_wait_seconds` | Time to immutable approval or denial receipt |
| `kern_recovery_resolutions_total` | Automatic or human uncertain-operation conclusions |
| `kern_plugin_events_total` | Activated, failed, and skipped plugin outcomes |
| `kern_verifications_total` | Durable verification conclusions |
| `kern_eval_runs_total` | Evaluation runs by state |
| `kern_eval_cases_total` | First-attempt evaluation outcomes |
| `kern_tasks_active`, `kern_tasks_queued` | Current worker demand |
| `kern_go_goroutines` | Current process goroutines |
| `kern_db_*` | Database connection limit, use, wait count, and wait duration |

The collector caches a completed read-only snapshot for two seconds to avoid
concurrent scrapes repeatedly scanning the same local ledger. It never starts a
database write transaction.

Metric labels use fixed Core-owned vocabularies. Task IDs, Attempt IDs, model
names, provider endpoints, plugin IDs, file paths, full URLs, prompts, and error
messages are never labels. Unknown stored values collapse to `unknown` or
`other` instead of creating a new series.

## Tracing

Every Attempt receives a deterministic opaque 128-bit `trace_id`, derived from
its durable Attempt identity. It is stable across restart but does not disclose
the source ID. Task snapshots and every SSE event expose the trace ID.

Task events also carry `span_id` and, for child spans, `parent_span_id`:

- Task lifecycle and Agent phase events belong to the Attempt root span.
- Model start, retry, completion, usage, and stream events are child spans.
- Operation events use the durable Operation identity.
- Plugin activation and verification events use bounded Attempt child spans.
- Eval Case execution uses a stable run/case/variant/attempt span in structured logs.

HTTP requests accept W3C `Traceparent`. Kern preserves a valid incoming trace
ID, creates a new HTTP span ID, returns the resulting `Traceparent`, and writes
both values to its structured request log. Invalid or all-zero headers start a
new trace. HTTP trace identity is deliberately separate from the Attempt trace
created by an asynchronous task submission; the returned Task supplies that
durable execution identity.

Local v1 tracing is intentionally dependency-free and uses structured logs
plus the durable event stream. A remote OpenTelemetry exporter remains part of
the later remote-service deployment mode; enabling local metrics does not send
data off the machine.

## Sensitive data

Metrics and trace identity never contain secrets. Default structured logs omit
prompts, file bodies, tool output, URLs, model response text, and API keys.
The model adapter additionally redacts its exact resolved API-key value from
complete and streaming provider output, tool arguments, response metadata, and
provider error bodies before those values reach events or logs.
Errors returned to HTTP clients remain bounded generic problems; detailed local
errors are correlated through `trace_id`.
