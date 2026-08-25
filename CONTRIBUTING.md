# Contributing to Kern

Kern accepts focused contributions that preserve its core guarantees: bounded
tools, explicit approval, durable operation records, conservative recovery,
and evidence-based completion.

## Prerequisites

- Go 1.26.6 or newer;
- Node.js 22 or newer when changing the Web client or TypeScript SDK;
- Git.

## Local setup

```sh
git clone https://github.com/userInner/kern.git
cd kern
make build
go test ./...
```

The generated Web assets are committed so Go-only changes do not require a
Node.js build. If you change `web/`, run:

```sh
make web
git diff -- internal/transport/httpapi/web
```

If you change the TypeScript SDK, run:

```sh
make sdk
cd sdk/typescript && npm test
```

## Before opening a pull request

Run the checks that cover your change. For a complete local release check:

```sh
make check
make race
```

The full check verifies formatting, dependency checksums, static analysis,
known Go vulnerabilities, Go tests, evaluation assets, npm dependencies, the
Web build, and both client test suites.

Keep pull requests narrow. Include:

- the problem and the intended behavior;
- tests or deterministic evaluation evidence;
- any security, compatibility, migration, or recovery impact;
- screenshots for visible Web changes;
- documentation updates for public behavior.

Do not include credentials, local databases, private task transcripts, model
outputs, benchmark workspaces, or generated local binaries.

## Plugins and evaluation cases

Plugins must declare every resource and permission, remain additive to Core,
and preserve the general fallback. See
[`docs/plugin-authoring.md`](docs/plugin-authoring.md).

Evaluation changes must keep fixtures isolated, graders deterministic, and
network or command allowances explicit. Validate a suite before running it:

```sh
./bin/kern eval validate ./evals/go
```

## Security reports

Do not file public issues for suspected vulnerabilities. Follow
[`SECURITY.md`](SECURITY.md) and use GitHub's private vulnerability reporting
flow.
