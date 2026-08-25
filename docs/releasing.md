# Releasing Kern

Kern releases are created only from canonical semantic-version tags matching
`vMAJOR.MINOR.PATCH`. Release tags are treated as immutable after publication.
The Release workflow builds six CLI archives:

- Linux amd64 and arm64;
- macOS amd64 and arm64;
- Windows amd64 and arm64.

The binary uses `CGO_ENABLED=0`, strips local source paths, clears the Go build
ID, and embeds the tag in `kern version`. CI includes a gate that builds the
same source twice and compares the resulting binaries byte for byte.

Every release also contains:

- the Apache-2.0 license and third-party attribution notices in every archive;
- `SHA256SUMS` for every archive and the SBOM;
- an SPDX JSON software bill of materials generated from the tagged source;
- a GitHub artifact attestation covering every checksum subject.

All third-party Actions are pinned to full commit hashes. Dependabot proposes
updates to those pins instead of allowing a mutable major-version tag to change
the release process silently.

## Maintainer checklist

1. Confirm CI, reachable Go vulnerability scanning, npm audits, and CodeQL are
   green on `main`.
2. Complete the macOS, Linux, and Windows evidence matrix in
   [`manual-validation.md`](manual-validation.md).
3. Confirm the version is a new semantic version and release notes are ready.
4. Create and push a signed tag, for example `git tag -s v0.1.0` followed by
   `git push origin v0.1.0`.
5. Wait for all six build jobs and the publish job to finish.
6. Download one archive and verify its checksum and attestation.
7. Run `kern version` and a fresh-data-directory `kern doctor` smoke test on at
   least one supported platform.

The workflow is safe to rerun: an existing release receives replacement assets
with the same names rather than creating a second release.

## User verification

After downloading the archive and `SHA256SUMS`, verify the digest using the
platform's SHA-256 tool. GitHub CLI users can additionally verify build
provenance:

```sh
gh attestation verify kern-v0.1.0-linux-amd64.tar.gz --repo userInner/kern
```

The expected repository must be supplied explicitly. A valid checksum without
a valid repository-bound attestation is not sufficient provenance evidence.
