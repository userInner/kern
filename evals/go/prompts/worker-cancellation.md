`Process` ignores cancellation and continues through every item. Make it stop
promptly and return the context cause without changing the signature or adding
arbitrary timeouts. Add deterministic coverage and run all tests.
