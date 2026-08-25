The package exposes `ErrNotFound`, but callers cannot detect it through
`errors.Is`. Preserve the sentinel and add causal wrapping without changing the
public signature or error text unnecessarily. Run and extend the tests.
