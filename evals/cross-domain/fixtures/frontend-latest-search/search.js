export function createLatestSearch(fetcher) {
  const state = { loading: false, data: null, error: null, query: "" }
  return {
    async search(query) {
      state.loading = true
      state.query = query
      try {
        state.data = await fetcher(query, {})
      } catch (error) {
        state.error = error
      }
      state.loading = false
    },
    cancel() {},
    snapshot() { return state }
  }
}
