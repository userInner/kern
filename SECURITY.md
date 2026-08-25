# Security policy

## Reporting a vulnerability

Do not open a public issue for suspected authorization bypasses, credential
exposure, workspace escapes, command injection, SSRF, plugin sandbox escapes,
or unsafe recovery behavior. Use the repository's **Security → Report a
vulnerability** flow to create a private GitHub Security Advisory.

Include the affected version and operating system, the smallest reproducible
sequence, expected and observed policy decisions, and whether a secret or
external side effect was exposed. Remove real credentials, personal data, and
unrelated repository content from logs and reproductions.

There is no guaranteed response SLA before the project has dedicated security
maintainers. Please avoid publishing details until a fix or coordinated
disclosure plan is available.

## Supported versions

Before 1.0, only the latest published release receives security fixes. Older
versions may be useful for restoring a pre-migration backup but should not be
used for new tasks after a security release.

## Security boundaries

Kern is local-first, not an operating-system security boundary. Core still
enforces workspace paths, exact operation hashes, approval receipts, bounded
commands and network requests, secret-free model context, plugin integrity,
and conservative crash recovery. Users must review approval cards and should
run Kern under an operating-system account that has no unnecessary access.
