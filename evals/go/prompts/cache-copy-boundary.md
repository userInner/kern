The byte cache leaks its owned storage through both `Set` and `Get`. Enforce copy
boundaries so callers cannot mutate cached values, retain the missing-key
semantics, and add regression coverage before running all tests.
