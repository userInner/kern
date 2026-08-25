# Authoring a Kern plugin

A Kern plugin is an additive, versioned directory. It can contribute bounded
knowledge, professional workflows, quality rules, deterministic verifiers, and
executable capabilities. It cannot replace Core policy, approve its own tools,
increase the Task budget, or make otherwise unsupported tasks available.

Start by copying the layout of `plugins/go-expert`:

```text
my-plugin/
  kern.plugin.json
  knowledge/
  workflows/
  rules/
  verifiers/
```

The manifest is strictly decoded: unknown fields, unsafe paths, symbolic
links, duplicate entrypoints, incompatible Core ranges, and undeclared process
executables are rejected. Package limits are 2,000 regular files and 128 MiB;
individual declarative resources are limited to 64 KiB and the activated
bundle to 256 KiB.

## Manifest contract

Required fields are:

- `schema_version`: currently `1`;
- `id`: a lowercase reverse-domain identifier; `host.*` is reserved;
- `name`, optional `description`, and semantic `version`;
- `core`: the explicit compatible Core range;
- `entrypoints`: one or more package-relative resource paths;
- `activation`: bounded file signals and task intent identifiers;
- `permissions`: only the filesystem and executable names the plugin needs;
- `integrity.files`: the package SHA-256 digest.

Use the CLI to calculate the digest after every content change, place the
reported value in the manifest, and then validate installation:

```sh
kern plugin digest ./my-plugin
kern plugin install ./my-plugin
kern plugin inspect --output json dev.example.my-plugin
```

Installation does not enable a plugin implicitly. Enable it only after review,
or select it for one isolated Task.

## Resource ownership

- Knowledge is UTF-8 advisory text, not a system prompt.
- Workflow steps are filtered by Core-owned `prepare`, `execute`, and `verify`
  phases and optional intent identifiers.
- Rules add quality or risk checks; they never weaken Core decisions.
- Verifiers evaluate evidence from operations that already passed Core policy.
- Tool entrypoints must declare a strict JSON Schema, bounded timeout/output,
  runtime, and all executable permissions.

Declarative plugins are the stable v1 authoring path. Subprocess tools use one
JSON-RPC 2.0 request over standard input and one bounded response over standard
output. The [Core-owned WASM ABI](wasm-abi-v1.md) uses the same envelope and
supplies module bytes plus explicit wall-clock, output and memory ceilings to a
sandbox; it grants no host filesystem, environment, network, clock or random
capability.
Core verifies the WebAssembly binary magic and version before the sandbox is
entered; a renamed or unsupported binary is rejected without execution.
The v1 contract uses cancellable wall-clock execution rather than claiming
instruction-level fuel metering that the selected pure-Go runtime does not
expose.
Core considers a WASM adapter executable only after its bounded, non-mutating
startup probe succeeds. The production wazero adapter enforces this contract;
an unavailable adapter is reported and skipped without disabling declarative
plugins or Core's general fallback.

## Evaluation requirement

A domain plugin should ship a versioned Eval suite containing both a General
variant and the plugin variant. Both must use identical fixtures, prompts,
model identity, budgets, retry policy, grader commands, and deterministic
graders. Publish improvements and regressions; do not select only passing cases.

`evals/smoke` proves only that the plugin activation and report pipeline work.
It is not sufficient evidence that a plugin improves quality.
