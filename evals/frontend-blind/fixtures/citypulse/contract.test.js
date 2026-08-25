import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const [html, css, javascript] = await Promise.all([
  readFile(new URL("./index.html", import.meta.url), "utf8"),
  readFile(new URL("./styles.css", import.meta.url), "utf8"),
  readFile(new URL("./app.js", import.meta.url), "utf8")
])

test("uses local assets and semantic application landmarks", () => {
  assert.match(html, /<main\b/i)
  assert.match(html, /<nav\b/i)
  assert.match(html, /<dialog\b/i)
  assert.match(html, /aria-live\s*=/i)
  assert.doesNotMatch(html + css + javascript, /https?:\/\//i)
})

test("contains a responsive and reduced-motion presentation", () => {
  assert.match(css, /@media\s*\([^)]*max-width/i)
  assert.match(css, /prefers-reduced-motion/i)
  assert.match(css, /:focus-visible/i)
})

test("implements event data and required interaction states", () => {
  assert.match(javascript, /addEventListener/)
  assert.match(javascript, /dialog|showModal/i)
  assert.match(javascript, /filter|search/i)
  assert.match(javascript, /save|bookmark/i)
  const eventLikeRecords = javascript.match(/\b(title|name)\s*:/g) ?? []
  assert.ok(eventLikeRecords.length >= 6, `expected at least six seeded event records, found ${eventLikeRecords.length}`)
})
