export function reconcileMessages(current, incoming) {
  return [...current, ...incoming]
}
