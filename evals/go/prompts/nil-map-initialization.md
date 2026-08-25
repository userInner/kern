`Counter.Add` panics on the first write after construction. Fix the ownership and
initialization bug without changing the exported API. Preserve the useful zero
value if practical, add boundary coverage, format the code, and run all tests.
