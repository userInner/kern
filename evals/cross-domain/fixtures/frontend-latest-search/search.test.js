import assert from "node:assert/strict"
import test from "node:test"
import { createLatestSearch } from "./search.js"

function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

test("only the latest request commits and the previous signal is aborted", async () => {
  const calls = []
  const controller = createLatestSearch((query, options) => {
    const result = deferred()
    calls.push({ query, signal: options.signal, result })
    return result.promise
  })
  const first = controller.search("old")
  const second = controller.search("new")
  assert.equal(calls[0].signal.aborted, true)
  calls[1].result.resolve(["new result"])
  await second
  calls[0].result.resolve(["old result"])
  await first
  assert.deepEqual(controller.snapshot(), { loading: false, data: ["new result"], error: null, query: "new" })
})

test("abort is not exposed as an error but latest failures are", async () => {
  const calls = []
  const controller = createLatestSearch((_query, options) => {
    const result = deferred()
    calls.push({ signal: options.signal, result })
    return result.promise
  })
  const first = controller.search("first")
  const second = controller.search("second")
  const abortError = new Error("aborted")
  abortError.name = "AbortError"
  calls[0].result.reject(abortError)
  await first
  const failure = new Error("offline")
  calls[1].result.reject(failure)
  await second
  assert.equal(controller.snapshot().error, failure)
  assert.equal(controller.snapshot().loading, false)
})

test("snapshot and cancel cannot expose mutable internal state", async () => {
  const controller = createLatestSearch(async () => ["safe"])
  await controller.search("q")
  const snapshot = controller.snapshot()
  snapshot.data.push("mutated")
  assert.deepEqual(controller.snapshot().data, ["safe"])
  controller.cancel()
  assert.equal(controller.snapshot().loading, false)
})
