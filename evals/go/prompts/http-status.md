`FetchJSON` treats an HTTP 500 response as successful data. Make non-2xx status
codes fail with a useful bounded diagnostic while preserving successful response
behavior and always closing the body. Add regression tests and run all checks.
