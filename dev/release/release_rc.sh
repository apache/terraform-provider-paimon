#!/usr/bin/env bash
# Licensed to the Apache Software Foundation (ASF) under one or more
# contributor license agreements. See the NOTICE file distributed with
# this work for additional information regarding copyright ownership.
# The ASF licenses this file to You under the Apache License, Version 2.0
# (the "License"); you may not use this file except in compliance with
# the License. You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Cuts a release candidate for the Apache Paimon Terraform provider:
#
#   1. Runs the release checks, then creates a signed v<version>-rc<rc> tag and
#      pushes it, which triggers the "Terraform Build Release Candidate"
#      workflow (.github/workflows/tf-release.yml). CI builds the source archive and the
#      convenience binaries and publishes them as a GitHub prerelease.
#   2. Waits for that workflow, then signs the source archive (.asc) and the
#      binary SHA256SUMS (.sig) with your KEYS-registered GPG key and uploads the
#      signatures back to the prerelease.
#   3. Uploads the signed source release, the artifact the PMC votes on, to
#      dist/dev. Convenience binaries stay on the GitHub prerelease.
#   4. Prints the [VOTE] email for dev@paimon.apache.org.
#
# Set RELEASE_PULL=0, RELEASE_PUSH_TAG=0, RELEASE_SIGN=0, or RELEASE_UPLOAD=0
# (or RELEASE_DEFAULT=0) to skip individual stages. RELEASE_REMOTE names the git
# remote for apache/terraform-provider-paimon (default: origin), and
# GPG_LOCAL_USER selects the signing key when several are available.

set -euo pipefail

if [[ $# -ne 2 || ! "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ || ! "$2" =~ ^[1-9][0-9]*$ ]]; then
  echo "usage: $0 <version> <rc>  (example: $0 0.1.0 1)" >&2
  exit 2
fi
version="$1"
rc="$2"
tag="v${version}-rc${rc}"
repository="apache/terraform-provider-paimon"
source_archive="apache-paimon-terraform-${version}-src.tgz"
binary_sums="terraform-provider-paimon_${version}_SHA256SUMS"
staging_name="paimon-terraform-${version}-rc${rc}"

: "${RELEASE_DEFAULT:=1}"
: "${RELEASE_PULL:=${RELEASE_DEFAULT}}"
: "${RELEASE_PUSH_TAG:=${RELEASE_DEFAULT}}"
: "${RELEASE_SIGN:=${RELEASE_DEFAULT}}"
: "${RELEASE_UPLOAD:=${RELEASE_DEFAULT}}"
: "${RELEASE_REMOTE:=origin}"

gpg_args=()
tag_args=(-s)
if [[ -n "${GPG_LOCAL_USER:-}" ]]; then
  gpg_args=(--local-user "${GPG_LOCAL_USER}")
  tag_args=(-u "${GPG_LOCAL_USER}")
fi

sha512_check() {
  if command -v sha512sum >/dev/null; then sha512sum -c "$@"; else shasum -a 512 -c "$@"; fi
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"

remote_url="$(git remote get-url "${RELEASE_REMOTE}")"
case "${remote_url}" in
  "git@github.com:${repository}.git" | "https://github.com/${repository}" | "https://github.com/${repository}.git") ;;
  *)
    echo "remote ${RELEASE_REMOTE} points at ${remote_url}, not ${repository}" >&2
    echo "set RELEASE_REMOTE to the remote for the Apache repository" >&2
    exit 1
    ;;
esac

if [[ -n "$(git status --porcelain)" ]]; then
  echo "release candidates must be cut from a clean checkout" >&2
  exit 1
fi
if [[ ${RELEASE_PULL} -gt 0 ]]; then
  # main for a major/minor release, paimon-terraform-X.Y.x for a patch release.
  branch="$(git symbolic-ref --short HEAD)"
  echo "Updating ${branch} from ${RELEASE_REMOTE}"
  git pull --ff-only "${RELEASE_REMOTE}" "${branch}"
fi

if [[ ${RELEASE_PUSH_TAG} -gt 0 ]]; then
  if git rev-parse --quiet --verify "refs/tags/${tag}" >/dev/null ||
    [[ -n "$(git ls-remote --tags "${RELEASE_REMOTE}" "refs/tags/${tag}")" ]]; then
    echo "tag ${tag} already exists; use the next RC number" >&2
    exit 1
  fi
  ./dev/update_licenses.sh --check
  make check
  make check-license
  make test-acceptance
fi

work_dir="$(mktemp -d -t paimon-provider-rc.XXXXXX)"
trap 'rm -rf "${work_dir}"' EXIT
staging="${work_dir}/stage"

if [[ ${RELEASE_PUSH_TAG} -gt 0 ]]; then
  git tag "${tag_args[@]}" "${tag}" -m "Apache Paimon Terraform ${version} RC${rc}"
  git tag -v "${tag}"
  git push "${RELEASE_REMOTE}" "${tag}"
fi
rc_hash="$(git rev-list --max-count=1 "${tag}")"

if [[ ${RELEASE_SIGN} -gt 0 ]]; then
  run_id=""
  for _ in $(seq 1 60); do
    run_id="$(gh run list --repo "${repository}" --workflow=tf-release.yml --json databaseId,event,headBranch --jq ".[] | select(.event == \"push\" and .headBranch == \"${tag}\") | .databaseId" | head -1)"
    [[ -n "${run_id}" ]] && break
    sleep 5
  done
  [[ -n "${run_id}" ]] || { echo "timed out waiting for tf-release.yml on ${tag}" >&2; exit 1; }
  gh run watch --repo "${repository}" --exit-status "${run_id}"

  mkdir "${staging}"
  gh release download "${tag}" --repo "${repository}" --dir "${staging}" \
    --pattern "${source_archive}" --pattern "${source_archive}.sha512" --pattern "${binary_sums}"
  (
    cd "${staging}"
    sha512_check "${source_archive}.sha512"
    gpg ${gpg_args[@]+"${gpg_args[@]}"} --armor --detach-sign "${source_archive}"
    gpg ${gpg_args[@]+"${gpg_args[@]}"} --batch --yes --detach-sign --output "${binary_sums}.sig" "${binary_sums}"
    gpg --verify "${source_archive}.asc" "${source_archive}"
    gpg --verify "${binary_sums}.sig" "${binary_sums}"
  )
  gh release upload "${tag}" --repo "${repository}" --clobber \
    "${staging}/${source_archive}.asc" "${staging}/${binary_sums}.sig"
fi

if [[ ${RELEASE_UPLOAD} -gt 0 ]]; then
  [[ -d "${staging}" ]] || { echo "RELEASE_UPLOAD requires RELEASE_SIGN in the same run" >&2; exit 1; }
  rm -f "${staging}/${binary_sums}" "${staging}/${binary_sums}.sig"
  svn import "${staging}" "https://dist.apache.org/repos/dist/dev/paimon/${staging_name}" \
    -m "Apache Paimon Terraform provider ${version} RC${rc}"
fi

cat <<MAIL
To: dev@paimon.apache.org
Subject: [VOTE] Release Apache Paimon Terraform ${version} (RC${rc})

Hi everyone,

Please review and vote on release candidate ${rc} of Apache Paimon
Terraform ${version}, the Terraform and OpenTofu provider for Apache Paimon.

The candidate is based on commit ${rc_hash} [1].

The source release, its signature, and its SHA-512 checksum are staged at [2].
The release is signed with a key from the Apache Paimon KEYS file [3].

Convenience binaries for the Terraform and OpenTofu registries, with their
signed SHA256SUMS, are attached to the GitHub prerelease [4]. Only the source
release is voted on.

See [5] for how to verify the release candidate:

  ./dev/release/verify_rc.sh ${version} ${rc}

The vote will be open for at least 72 hours.

[ ] +1 Release this as Apache Paimon Terraform ${version}
[ ] +0
[ ] -1 Do not release this as Apache Paimon Terraform ${version} because...

[1] https://github.com/${repository}/tree/${rc_hash}
[2] https://dist.apache.org/repos/dist/dev/paimon/${staging_name}/
[3] https://downloads.apache.org/paimon/KEYS
[4] https://github.com/${repository}/releases/tag/${tag}
[5] https://github.com/${repository}/blob/main/docs/releasing.md#verifying-a-release-candidate
MAIL
