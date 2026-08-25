An early failure leaks the runner's semaphore slot, causing later work to block.
Repair resource ownership on every return path without widening the critical
section. Add deterministic regression coverage and run all tests.
