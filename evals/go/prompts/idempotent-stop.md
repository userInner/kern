Calling `Worker.Stop` more than once panics. Make shutdown concurrency-safe and
idempotent while preserving the notification channel behavior. Add concurrent
and sequential regression coverage, then run the race-enabled checks.
