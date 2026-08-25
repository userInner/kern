`Save` returns immediately when its write fails but leaves the transaction open.
Guarantee rollback on every pre-commit failure, never rollback after a successful
commit, preserve the original cause, and run the deterministic fake tests.
