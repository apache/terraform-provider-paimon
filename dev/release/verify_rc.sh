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

# Verifies a release candidate of the Apache Paimon Terraform provider so you
# can cast an informed vote. It checks the source release signature and SHA-512
# checksum, the convenience binaries against their signed SHA256SUMS, runs the
# Apache RAT license check, and builds and tests the provider from source in a
# throwaway sandbox.
#
# Requirements: bash, curl, gpg, Go, Terraform, sha512sum/sha256sum (or
# shasum), gh for the binaries, and java plus unzip for RAT.
#
# Set VERIFY_SIGN=0, VERIFY_BINARY=0, VERIFY_RAT=0, or VERIFY_ACCEPTANCE=0 to
# skip a stage, and VERIFY_ARTIFACTS_DIR=<dir> to verify a local copy of the
# source release instead of downloading it from dist/dev. A failed run keeps its
# sandbox for inspection.

set -euo pipefail

if [[ $# -ne 2 || ! "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ || ! "$2" =~ ^[1-9][0-9]*$ ]]; then
  echo "usage: $0 <version> <rc>  (example: $0 0.1.0 1)" >&2
  exit 2
fi
version="$1"
rc="$2"
rc_id="paimon-terraform-${version}-rc${rc}"
source_archive="apache-paimon-terraform-${version}-src.tgz"
source_directory="apache-paimon-terraform-${version}"
repository="apache/terraform-provider-paimon"
binary_sums="terraform-provider-paimon_${version}_SHA256SUMS"

: "${VERIFY_DEFAULT:=1}"
: "${VERIFY_SIGN:=${VERIFY_DEFAULT}}"
: "${VERIFY_BINARY:=${VERIFY_DEFAULT}}"
: "${VERIFY_RAT:=${VERIFY_DEFAULT}}"
: "${VERIFY_ACCEPTANCE:=${VERIFY_DEFAULT}}"
: "${VERIFY_ARTIFACTS_DIR:=}"
if [[ -n "${VERIFY_ARTIFACTS_DIR}" ]]; then
  VERIFY_ARTIFACTS_DIR="$(cd "${VERIFY_ARTIFACTS_DIR}" && pwd)"
fi

sha_check() {
  local bits="$1"
  shift
  if command -v "sha${bits}sum" >/dev/null; then
    "sha${bits}sum" -c "$@"
  else
    shasum -a "${bits}" -c "$@"
  fi
}

work_dir="$(mktemp -d -t paimon-provider-verify.XXXXXX)"
verified=0
cleanup() {
  if [[ ${verified} -eq 1 ]]; then
    chmod -R u+w "${work_dir}" 2>/dev/null || true
    rm -rf "${work_dir}"
  else
    echo "Release candidate verification failed; see ${work_dir}" >&2
  fi
}
trap cleanup EXIT
cd "${work_dir}"
echo "Working in ${work_dir}"

# Keep downloaded modules and build output out of the voter's own Go caches.
export GOPATH="${work_dir}/gopath"
export GOMODCACHE="${GOPATH}/pkg/mod"
export GOCACHE="${work_dir}/gocache"
export GOFLAGS="-modcacherw"

if [[ ${VERIFY_SIGN} -gt 0 ]]; then
  export GNUPGHOME="${work_dir}/gnupg"
  mkdir -m 700 "${GNUPGHOME}"
  curl -fsSLO https://downloads.apache.org/paimon/KEYS
  gpg --batch --import KEYS
fi

source_files=("${source_archive}" "${source_archive}.sha512")
[[ ${VERIFY_SIGN} -gt 0 ]] && source_files+=("${source_archive}.asc")
for file in "${source_files[@]}"; do
  if [[ -n "${VERIFY_ARTIFACTS_DIR}" ]]; then
    cp "${VERIFY_ARTIFACTS_DIR}/${file}" .
  else
    curl -fsSLO "https://dist.apache.org/repos/dist/dev/paimon/${rc_id}/${file}"
  fi
done
if [[ ${VERIFY_SIGN} -gt 0 ]]; then
  gpg --batch --verify "${source_archive}.asc" "${source_archive}"
fi
sha_check 512 "${source_archive}.sha512"

if [[ ${VERIFY_BINARY} -gt 0 ]]; then
  # Convenience binaries live on the GitHub prerelease, not in dist/dev.
  mkdir binaries
  gh release download "v${version}-rc${rc}" --repo "${repository}" --dir binaries \
    --pattern 'terraform-provider-paimon_*'
  (
    cd binaries
    if [[ ${VERIFY_SIGN} -gt 0 ]]; then
      gpg --batch --verify "${binary_sums}.sig" "${binary_sums}"
    fi
    sha_check 256 "${binary_sums}"
  )
fi

tar -xzf "${source_archive}"
cd "${source_directory}"
if [[ ${VERIFY_RAT} -gt 0 ]]; then
  dev/check-license
fi
./dev/update_licenses.sh --check
make check
if [[ ${VERIFY_ACCEPTANCE} -gt 0 ]]; then
  make test-acceptance
fi

verified=1
echo "Release candidate ${version} RC${rc} verified successfully."
