`WriteAll` loses bytes when a valid `io.Writer` performs a short write without an
error. Implement the full `io.Writer` contract, including no-progress handling,
without changing the signature. Run the deterministic regression tests.
