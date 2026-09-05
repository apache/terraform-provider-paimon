<!--
  Licensed to the Apache Software Foundation (ASF) under one
  or more contributor license agreements. See the NOTICE file
  distributed with this work for additional information
  regarding copyright ownership. The ASF licenses this file
  to you under the Apache License, Version 2.0 (the
  "License"); you may not use this file except in compliance
  with the License. You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing,
  software distributed under the License is distributed on an
  "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
  KIND, either express or implied. See the License for the
  specific language governing permissions and limitations
  under the License.
-->

# Releasing the Paimon Terraform provider

The voted Apache release artifact is the source tarball. The zip files attached
to GitHub Releases are convenience binaries consumed by the Terraform and
OpenTofu registries. CI builds candidates but never holds a release manager's
private key.

## Prerequisites

- A GPG key published in the Apache Paimon `KEYS` file and registered with the
  Terraform Registry `apache` namespace.
- Commit access to `dist.apache.org/repos/dist/dev/paimon` and PMC access for
  `dist.apache.org/repos/dist/release/paimon`.
- `git`, `svn`, `gh`, `gpg`, Go, Terraform, and `shasum` on `PATH`.
- OpenTofu and Python 3 for the public Registry installation check.
- A clean checkout whose `origin` is the Apache repository.

Before cutting a candidate, run:

```bash
./dev/update_licenses.sh
./dev/update_licenses.sh --check
make check
make check-license
make test-acceptance
make test-acceptance-tofu
```

Complete the [production validation matrix](production-readiness.md) against
each supported real Catalog/DLF and query-engine deployment, recording exact
versions and job results before claiming production compatibility.

Commit any refreshed `LICENSE-binary`, `NOTICE-binary`, or `licenses-binary/`
files. They describe the code statically linked into every convenience binary.

## Release candidate

From the branch being released:

```bash
./dev/release/release_rc.sh 0.1.0 1
```

The script creates and pushes `v0.1.0-rc1`. The release workflow builds a
SHA-512 source artifact and GoReleaser produces signed-ready Registry zip files,
the manifest, and `SHA256SUMS`. The release manager signs the source and binary
checksums, stages the source under ASF `dist/dev`, and sends the printed vote to
`dev@paimon.apache.org`. The vote remains open for at least 72 hours and needs
three binding `+1` votes.

Reviewers verify the candidate with:

```bash
./dev/release/verify_rc.sh 0.1.0 1
```

This validates the KEYS-backed GPG signatures, SHA-512/SHA-256 checksums,
binary inventory, Apache RAT result, build, and tests.

## Final release

After a successful vote:

```bash
./dev/release/release.sh 0.1.0 1
```

The final script tags the exact RC commit, promotes the voted source artifact to
ASF `dist/release`, and creates the final GitHub Release from the already-voted
assets. Nothing is rebuilt. Confirm the release appears in both registries,
record it in the ASF reporter, and send an announcement to the Paimon mailing
list.

After each Registry has indexed the final version, verify installation from a
fresh directory without development overrides or a local plugin mirror:

```bash
./dev/release/verify_registry.sh 0.1.0 terraform
./dev/release/verify_registry.sh 0.1.0 tofu
```

The script pins the requested version, runs the CLI's normal Registry download
and checksum/signature verification, validates configuration, and checks all
five resource and two data-source schemas. It does not contact a Catalog or
create resources. A missing or unindexed release must fail this check; do not
substitute a local binary when recording distribution evidence. Retain the
output and the release signing-key identity with the release verification.
