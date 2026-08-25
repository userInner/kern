The registry is used by concurrent discovery and request paths. Its map access
currently races and can crash. Make `Set` and `Get` safe under concurrency while
keeping reads efficient and preserving the API. Run the race-enabled test suite.
