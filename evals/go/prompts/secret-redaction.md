`Redact` hides only the first occurrence of a secret and accepts an empty secret,
which can corrupt output. Redact every occurrence, make empty-secret behavior
safe, preserve unrelated text, and run the regression tests.
