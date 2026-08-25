export function createSSEDecoder() {
  const decoder = new TextDecoder()
  return {
    push(chunk) {
      return decoder.decode(chunk).split("\n\n").filter(Boolean).map((event) => event.replace(/^data:\s?/, ""))
    },
    finish() {
      return []
    }
  }
}
