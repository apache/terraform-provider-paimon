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

# Finalizes a release after the vote passes. It promotes the exact artifacts
# that were voted on; nothing is rebuilt:
#
#   1. Creates a signed v<version> tag at the RC commit and pushes it.
#   2. Moves the source release from dist/dev to dist/release.
#   3. Creates the final GitHub Release with the promoted source release and the
#      signed convenience binaries from the RC prerelease. The Terraform and
#      OpenTofu registries ingest this release.
#
# <rc> is the release candidate that passed the vote. RELEASE_REMOTE names the
# git remote for apache/terraform-provider-paimon (default: origin), and
# GPG_LOCAL_USER selects the tag signing key when several are available.

set -euo pipefail

if [[ $# -ne 2 || ! "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ || ! "$2" =~ ^[1-9][0-9]*$ ]]; then
  echo "usage: $0 <version> <rc>  (example: $0 0.1.0 1)" >&2
  exit 2
fi
version="$1"
rc="$2"
tag="v${version}"
rc_tag="${tag}-rc${rc}"
repository="apache/terraform-provider-paimon"
release_id="paimon-terraform-${version}"
binary_sums="terraform-provider-paimon_${version}_SHA256SUMS"
dist_dev="https://dist.apache.org/repos/dist/dev/paimon"
dist_release="https://dist.apache.org/repos/dist/release/paimon"
: "${RELEASE_REMOTE:=origin}"

tag_args=(-s)
if [[ -n "${GPG_LOCAL_USER:-}" ]]; then
  tag_args=(-u "${GPG_LOCAL_USER}")
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"

# Check every fetch and push URL, since a remote can push somewhere other than
# where it fetches from. URLs are never printed: they may embed credentials.
remote_urls="$(git remote get-url --all "${RELEASE_REMOTE}")"
remote_urls+=$'\n'"$(git remote get-url --push --all "${RELEASE_REMOTE}")"
while IFS= read -r remote_url; do
  case "${remote_url}" in
    "git@github.com:${repository}.git" | "https://github.com/${repository}" | "https://github.com/${repository}.git") ;;
    *)
      echo "remote ${RELEASE_REMOTE} has a fetch or push URL that is not ${repository}" >&2
      echo "set RELEASE_REMOTE to a remote whose URLs all point at the Apache repository" >&2
      exit 1
      ;;
  esac
done <<<"${remote_urls}"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "final releases must be published from a clean checkout" >&2
  exit 1
fi
git fetch "${RELEASE_REMOTE}" "refs/tags/${rc_tag}:refs/tags/${rc_tag}"
rc_commit="$(git rev-parse "${rc_tag}^{commit}")"

work_dir="$(mktemp -d -t paimon-provider-release.XXXXXX)"
trap 'rm -rf "${work_dir}"' EXIT

# The registries reject a release without the signed checksums, so check the
# voted assets before anything public changes.
gh release download "${rc_tag}" --repo "${repository}" --dir "${work_dir}/assets" \
  --pattern 'terraform-provider-paimon_*'
for required in "${binary_sums}" "${binary_sums}.sig" "terraform-provider-paimon_${version}_manifest.json"; do
  [[ -f "${work_dir}/assets/${required}" ]] || { echo "RC prerelease is missing ${required}" >&2; exit 1; }
done
gpg --verify "${work_dir}/assets/${binary_sums}.sig" "${work_dir}/assets/${binary_sums}"

git tag "${tag_args[@]}" "${tag}" "${rc_commit}" -m "Apache Paimon Terraform ${version}"
git tag -v "${tag}"
git push "${RELEASE_REMOTE}" "${tag}"

svn mv "${dist_dev}/${release_id}-rc${rc}" "${dist_release}/${release_id}" \
  -m "Apache Paimon Terraform provider ${version}"

svn export "${dist_release}/${release_id}" "${work_dir}/source"
cp "${work_dir}/source/"* "${work_dir}/assets/"
gh release create "${tag}" --repo "${repository}" --title "Apache Paimon Terraform provider ${version}" \
  --generate-notes --verify-tag "${work_dir}/assets/"*

echo "Published ${tag}:"
echo "  ${dist_release}/${release_id}"
echo "  https://github.com/${repository}/releases/tag/${tag}"
echo
echo "Record the release at https://reporter.apache.org/addrelease.html?paimon"

# ASF policy keeps the latest release of every maintained line in dist/release;
# older ones remain on archive.apache.org. Only older patch releases of this
# release line are superseded. Retiring another line is a separate decision.
release_line="${version%.*}"
superseded=()
while IFS= read -r old; do
  old_version="${old#paimon-terraform-}"
  if [[ "${old_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && "${old_version%.*}" == "${release_line}" &&
    ${old_version##*.} -lt ${version##*.} ]]; then
    superseded+=("${old}")
  fi
done < <(svn ls "${dist_release}" | sed 's|/$||')
if [[ ${#superseded[@]} -gt 0 ]]; then
  echo
  echo "Older ${release_line}.x releases superseded by ${version} in dist/release:"
  for old in "${superseded[@]}"; do
    echo "  svn delete -m 'Remove superseded release ${old}' ${dist_release}/${old}"
  done
fi

cat <<MAIL

Wait at least 24 hours after publishing and until both registries list
${version} (check with dev/release/verify_registry.sh). Then send this as plain
text from your @apache.org address:

To: dev@paimon.apache.org, announce@apache.org
Subject: [ANNOUNCE] Release Apache Paimon Terraform ${version}

The Apache Paimon community is pleased to announce the release of Apache
Paimon Terraform ${version}.

The provider manages Apache Paimon REST Catalog databases, tables, and access
policies with Terraform and OpenTofu.

Source release: https://downloads.apache.org/paimon/${release_id}/
Release notes:  https://github.com/${repository}/releases/tag/${tag}
Terraform:      https://registry.terraform.io/providers/apache/paimon/${version}
OpenTofu:       https://search.opentofu.org/provider/apache/paimon/v${version}

Thanks to everyone who contributed to this release.
MAIL
