`Retry` calls work even when its context is already canceled and ignores
cancellation between attempts. Stop promptly with `context.Cause`, do not sleep
after the last attempt, preserve the attempt contract, and run all tests.
