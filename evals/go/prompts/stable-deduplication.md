`Unique` removes duplicates but changes the first-seen order, which makes plans
and snapshots unstable. Preserve deterministic input order, retain nil behavior,
and avoid quadratic work. Add coverage and run all tests.
