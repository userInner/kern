import assert from "node:assert/strict"
import test from "node:test"
import { createSSEDecoder } from "./decoder.js"

function bytewise(text) {
  const decoder = createSSEDecoder()
  const output = []
  for (const byte of new TextEncoder().encode(text)) output.push(...decoder.push(Uint8Array.of(byte)))
  output.push(...decoder.finish())
  return output
}

test("preserves split UTF-8 and event boundaries", () => {
  assert.deepEqual(bytewise('data: {"delta":"你好"}\n\ndata: done\n\n'), ['{"delta":"你好"}', "done"])
})

test("supports CRLF, comments, fields, multiline data and DONE", () => {
  const input = ": keepalive\r\nid: 7\r\ndata: first\r\ndata: second\r\n\r\ndata: [DONE]\r\n\r\n"
  assert.deepEqual(bytewise(input), ["first\nsecond"])
})

test("finish flushes one final event without a blank delimiter", () => {
  const decoder = createSSEDecoder()
  assert.deepEqual(decoder.push(new TextEncoder().encode("data: tail")), [])
  assert.deepEqual(decoder.finish(), ["tail"])
  assert.deepEqual(decoder.finish(), [])
})
