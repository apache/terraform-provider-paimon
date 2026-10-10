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

This guide follows the [Apache release policy](https://www.apache.org/legal/release-policy.html).

The voted Apache release artifact is the source archive. The zip files attached
to GitHub Releases are convenience binaries consumed by the Terraform and
OpenTofu registries. CI builds candidates but never holds a release manager's
private key, and the final release promotes the voted files without rebuilding
them.

| Artifact | Location |
| --- | --- |
| Source release | `dist/{dev,release}/paimon/paimon-terraform-<version>[-rc<n>]/apache-paimon-terraform-<version>-src.tgz` with `.asc` and `.sha512` |
| Convenience binaries | GitHub Release `v<version>[-rc<n>]`: `terraform-provider-paimon_<version>_<os>_<arch>.zip` for linux, darwin, windows, and freebsd on amd64 and arm64 |
| Registry metadata | `terraform-provider-paimon_<version>_SHA256SUMS`, `..._SHA256SUMS.sig`, and `..._manifest.json` on the same GitHub Release |

## Prerequisites

- A GPG key published in the [Apache Paimon `KEYS` file](https://downloads.apache.org/paimon/KEYS).
  See [Set up a GPG key](#set-up-a-gpg-key).
- Commit access to `dist.apache.org/repos/dist/dev/paimon` and PMC access for
  `dist.apache.org/repos/dist/release/paimon`. Work with a PMC member if you do
  not have PMC access.
- Write access to `apache/terraform-provider-paimon`, and a git remote that
  points at it. The scripts use `origin` unless `RELEASE_REMOTE` names another
  remote, and refuse to run against a fork.
- `git`, `svn`, `gh`, `gpg`, `curl`, Go, Terraform, `java`, `unzip`, and
  `sha512sum`/`sha256sum` (or `shasum`) on `PATH`.
- OpenTofu and Python 3 for the public Registry installation check.

### Registry onboarding (first release only)

The registries read the final GitHub Release of
`apache/terraform-provider-paimon`. Both need one-time setup:

- **Terraform Registry.** A member of the `apache` GitHub organization with
  admin rights must publish the `apache/paimon` provider from the repository and
  register the release manager's public key under the namespace's
  **Settings → GPG Keys**. Request this through an
  [ASF Infra ticket](https://issues.apache.org/jira/projects/INFRA) before the
  first vote ends. Every later release manager's key must be added the same way.
- **OpenTofu Registry.** After the first final GitHub Release exists, open a
  *Provider* issue for `apache/paimon` and a *Provider Key* issue with the
  release key on [`opentofu/registry`](https://github.com/opentofu/registry/issues/new/choose).
  Later versions are picked up automatically.

## Prepare the release

- Remove attributes, resources, and data sources whose deprecation period ends
  in this version, and update `docs/` and `examples/` to match.
- Confirm the provider address in [`main.go`](../main.go) is
  `registry.terraform.io/apache/paimon`.
- Refresh the binary license inventory and commit any change.
  `LICENSE-binary`, `NOTICE-binary`, and `licenses-binary/` describe the code
  statically linked into every convenience binary and must match what ships.

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

### Versioning

The git tag carries the version; there is no version string to bump in source.
GoReleaser injects it into `main.version`. Versions follow
[semantic versioning](https://semver.org/) with a `v` prefix (`v0.1.0`), and
release candidates add `-rc<n>` starting at 1 (`v0.1.0-rc1`).

Major and minor releases are cut from `main`. Patch releases are cut from a
`paimon-terraform-X.Y.x` branch created from the previous release tag:

```bash
git fetch --tags <apache-remote>
git checkout -b paimon-terraform-0.1.x v0.1.0
git cherry-pick <commit>
git push <apache-remote> paimon-terraform-0.1.x
```

## Release candidate

From a clean checkout of the branch being released:

```bash
./dev/release/release_rc.sh 0.1.0 1
```

The script:

1. Fast-forwards the branch from the Apache remote, refuses an existing tag,
   runs the release checks, then creates a GPG-signed `v0.1.0-rc1` tag and
   pushes it.
2. Waits for the [release workflow](../.github/workflows/tf-release.yml), which
   builds the source archive with its SHA-512 checksum, builds the Registry
   zip files, `SHA256SUMS`, and manifest with GoReleaser, and publishes them as
   a GitHub prerelease.
3. Signs the source archive (`.asc`) and binary checksums (`SHA256SUMS.sig`)
   locally and uploads both signatures to the prerelease.
4. Stages the signed source release in ASF `dist/dev` and prints the vote
   email.

Set `RELEASE_PULL=0`, `RELEASE_PUSH_TAG=0`, `RELEASE_SIGN=0`, or
`RELEASE_UPLOAD=0` (or `RELEASE_DEFAULT=0`) to skip a stage, for example to
re-sign an existing candidate without re-tagging. Set `GPG_LOCAL_USER` to
choose the key for the tag and artifact signatures.

## Vote

Add a short summary of notable changes to the printed
`[VOTE] Release Apache Paimon Terraform 0.1.0 (RC1)` email and send it to
`dev@paimon.apache.org`. The vote stays open for at least 72 hours and passes
with at least three binding `+1` votes and more binding `+1` than `-1` votes.

If the candidate has problems, fix them on the release branch and cut the next
candidate with `./dev/release/release_rc.sh <version> <next-rc>`. Never
overwrite an existing candidate. After its vote is closed, delete the failed
candidate from `dist/dev`.

After 72 hours, reply to the vote thread with
`[RESULT][VOTE] Release Apache Paimon Terraform 0.1.0 (RC1)`. State whether the
vote passed, list binding and non-binding votes separately, and link the
archived vote thread.

## Final release

After a successful vote:

```bash
./dev/release/release.sh 0.1.0 1
```

The script:

1. Confirms the RC prerelease carries `SHA256SUMS`, a valid `SHA256SUMS.sig`,
   and the manifest that the registries require.
2. Creates a GPG-signed `v0.1.0` tag at the exact RC commit and pushes it.
3. Moves the voted source release from `dist/dev` to `dist/release`.
4. Creates the final GitHub Release from the voted source and binaries. Nothing
   is rebuilt. Because the binaries are promoted, `go version -m` reports the RC
   tag they were built from; file names and Registry metadata carry the final
   version, and those are what Terraform and OpenTofu resolve.
5. Lists any superseded `paimon-terraform-*` directories in `dist/release` and
   prints the announcement email.

## Post release

1. Record the release at <https://reporter.apache.org/addrelease.html?paimon>.
2. After each Registry has indexed the final version, verify installation from
   a fresh directory without development overrides or a local plugin mirror:

   ```bash
   ./dev/release/verify_registry.sh 0.1.0 terraform
   ./dev/release/verify_registry.sh 0.1.0 tofu
   ```

   The script pins the requested version, runs the CLI's normal Registry
   download and checksum/signature verification, validates configuration, and
   checks all five resource and two data-source schemas. It does not contact a
   Catalog or create resources. A missing or unindexed release must fail this
   check; do not substitute a local binary when recording distribution
   evidence. Retain the output and the release signing-key identity with the
   release verification.
3. Wait at least 24 hours after publishing, following the Paimon project
   convention, and confirm the source release is on
   `downloads.apache.org/paimon/`. Then send the printed `[ANNOUNCE]` email as
   plain text from your `@apache.org` address to `dev@paimon.apache.org` and
   `announce@apache.org`.
4. Open a PR against [`apache/paimon-website`](https://github.com/apache/paimon-website)
   that adds the release to the download page and release notes. Merge it only
   after the 24-hour wait.
5. Remove superseded releases listed by `release.sh` from `dist/release`; they
   remain available from `archive.apache.org`.
6. Review the generated GitHub release notes and confirm the release is marked
   as the latest release.

## Verifying a release candidate

Reviewers verify a candidate with:

```bash
./dev/release/verify_rc.sh 0.1.0 1
```

The script imports the Apache Paimon `KEYS` into a temporary keyring, verifies
the source release signature and SHA-512 checksum from `dist/dev`, verifies the
convenience binaries against their signed `SHA256SUMS`, runs Apache RAT and the
binary license inventory check, and runs `make check` and the protocol
acceptance tests from the extracted source. Go modules and build caches stay in
the throwaway sandbox, which is kept for inspection when verification fails.

Set `VERIFY_SIGN=0`, `VERIFY_BINARY=0`, `VERIFY_RAT=0`, or
`VERIFY_ACCEPTANCE=0` to skip a stage you cannot run, and say so in your vote.
`VERIFY_ARTIFACTS_DIR=<dir>` verifies a local copy of the source release.

A valid signature from a key in `KEYS`, matching checksums, a clean RAT check,
verified binaries, and a passing build and test run support a `+1`.

## Set up a GPG key

Create a key as described in the
[ASF OpenPGP guide](https://infra.apache.org/openpgp.html), then append it to
the Paimon `KEYS` file (this requires PMC access):

```bash
svn co --depth files https://dist.apache.org/repos/dist/release/paimon paimon-dist
cd paimon-dist
(gpg --list-sigs <KEY_ID> && gpg --armor --export <KEY_ID>) >> KEYS
svn commit -m "Add the release signing key for <YOUR NAME>"
```

Register the same public key with both registries as described in
[Registry onboarding](#registry-onboarding-first-release-only), so they can
verify `SHA256SUMS.sig`.
