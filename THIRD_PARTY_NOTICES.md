# Third-party notices

This file records direct production dependencies whose behavior forms part of
Kern's security or portability boundary. The complete transitive dependency
set and checksums remain authoritative in `go.mod`, `go.sum` and the release
SPDX SBOM.

## github.com/tetratelabs/wazero v1.12.0

- Purpose: import-free WebAssembly execution for `kern.plugin.abi/v1`.
- License: Apache License 2.0.
- Upstream: <https://github.com/tetratelabs/wazero>
- Kern configuration: no WASI/host imports, fresh runtime and instance per
  invocation, explicit memory-page ceiling, context termination and bounded
  response copying.

## github.com/zalando/go-keyring v0.2.8

- Purpose: macOS Keychain, Windows Credential Manager and Linux Secret Service
  access for write-only model credentials.
- License: MIT.
- Upstream: <https://github.com/zalando/go-keyring>
- Kern configuration: random opaque account IDs, 2048-byte portable value
  ceiling, bounded non-mutating readiness/read calls, idempotent deletion and
  fail-closed error mapping.

The latter uses `github.com/danieljoos/wincred` on Windows and
`github.com/godbus/dbus/v5` on Linux as platform-specific transitive
dependencies. Their exact versions and licenses are included in the generated
release SBOM.

## React 19.1.1, React DOM 19.1.1, and Scheduler 0.26.0

- Purpose: the embedded Web workbench distributed with the Kern binary.
- License: MIT.
- Upstream: <https://github.com/facebook/react>

The following license applies to React, React DOM, and Scheduler:

```text
MIT License

Copyright (c) Meta Platforms, Inc. and affiliates.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
