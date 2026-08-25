Cleanup currently returns only the final failure, hiding earlier resource errors.
Return an error detectable with `errors.Is` for every failed cleanup, preserve
execution of all callbacks and nil behavior, and run all tests.
