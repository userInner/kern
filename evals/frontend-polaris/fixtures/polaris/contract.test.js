import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const [html, css, javascript] = await Promise.all([
  readFile(new URL("./index.html", import.meta.url), "utf8"),
  readFile(new URL("./styles.css", import.meta.url), "utf8"),
  readFile(new URL("./app.js", import.meta.url), "utf8")
])

const source = `${html}\n${css}\n${javascript}`

test("is a self-contained semantic application", () => {
  assert.match(html, /<main\b/i)
  assert.match(html, /<nav\b/i)
  assert.match(html, /<dialog\b/i)
  assert.match(html, /aria-live\s*=/i)
  assert.doesNotMatch(source, /https?:\/\//i)
})

test("contains the fixed expedition content", () => {
  for (const value of [
    "POLARIS",
    "AURORA-7",
    "Reading the ice before it moves.",
    "Surface temp",
    "Sørsdal Gate",
    "Pressure front approaching",
    "Send field update"
  ]) {
    assert.ok(source.includes(value), `missing required content: ${value}`)
  }
})

test("implements the required interaction hooks", () => {
  assert.match(html, /data-view/i)
  assert.match(html, /data-filter/i)
  assert.match(javascript, /addEventListener/)
  assert.match(javascript, /showModal|dialog/i)
  assert.match(javascript, /acknowledge|acknowledged/i)
  assert.match(javascript, /160/)
})

test("contains responsive, focus, and motion handling", () => {
  assert.match(css, /@media\s*\([^)]*max-width/i)
  assert.match(css, /bottom-nav/i)
  assert.match(css, /sidebar/i)
  assert.match(css, /:focus-visible/i)
  assert.match(css, /prefers-reduced-motion/i)
})
