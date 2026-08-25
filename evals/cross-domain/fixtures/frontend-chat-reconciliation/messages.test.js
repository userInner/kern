import assert from "node:assert/strict"
import test from "node:test"
import { reconcileMessages } from "./messages.js"

test("patches by id while preserving order and omitted fields", () => {
  const current = [
    { id: "a", role: "user", content: "hello", metadata: { source: "local", count: 1 } },
    { id: "b", role: "assistant", content: "working" }
  ]
  const result = reconcileMessages(current, [{ id: "b", content: "done" }])
  assert.deepEqual(result, [current[0], { id: "b", role: "assistant", content: "done" }])
})

test("replaces an optimistic id through clientRequestId", () => {
  const result = reconcileMessages(
    [{ id: "temp-1", clientRequestId: "req-1", content: "pending", status: "sending" }],
    [{ id: "server-9", clientRequestId: "req-1", content: "saved", status: "sent" }, { id: "next", content: "new" }]
  )
  assert.deepEqual(result, [
    { id: "server-9", clientRequestId: "req-1", content: "saved", status: "sent" },
    { id: "next", content: "new" }
  ])
})

test("does not mutate inputs or share nested metadata with a patch", () => {
  const current = [{ id: "a", metadata: { count: 1, source: "local" } }]
  const incoming = [{ id: "a", metadata: { count: 2 } }]
  const beforeCurrent = structuredClone(current)
  const beforeIncoming = structuredClone(incoming)
  const result = reconcileMessages(current, incoming)
  result[0].metadata.count = 99
  assert.deepEqual(current, beforeCurrent)
  assert.deepEqual(incoming, beforeIncoming)
  assert.equal(result[0].metadata.source, "local")
})
