Pagination emits a next page after the final full page and accepts invalid
arguments. Correct the boundary contract: pages are zero-based, size and total
must be positive, and the second result reports whether another page exists.
