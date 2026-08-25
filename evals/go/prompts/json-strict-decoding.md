Configuration decoding silently accepts misspelled fields. Make decoding reject
unknown fields and trailing JSON values while preserving the current API. Add
regression tests for both invalid forms and run the package tests.
