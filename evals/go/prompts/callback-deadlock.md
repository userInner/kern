`Store.Update` deadlocks when a callback reads the same store. Do not execute
unknown caller code while holding the mutex. Preserve atomic state replacement,
document the callback semantics through tests, and run the full suite.
