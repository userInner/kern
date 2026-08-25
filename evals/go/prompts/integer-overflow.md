Quota addition wraps around for large positive or negative values. Detect signed
overflow and return a useful error while preserving the existing function
signature. Cover both boundaries and normal values, then run all tests.
