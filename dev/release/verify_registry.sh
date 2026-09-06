#!/usr/bin/env bash
# Licensed to the Apache Software Foundation (ASF) under one or more
# contributor license agreements. See the NOTICE file distributed with
# this work for additional information regarding copyright ownership.
# The ASF licenses this file to You under the Apache License, Version 2.0
# (the "License"); you may not use this file except in compliance with
# the License. You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

version="${1:-}"
cli="${2:-terraform}"
if [[ $# -gt 2 || ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo 'Usage: verify_registry.sh VERSION [terraform|tofu|/path/to/cli]' >&2
  exit 1
fi
command -v "$cli" >/dev/null
command -v python3 >/dev/null
registry=registry.terraform.io
if [[ "$(basename "$cli")" = tofu ]]; then
  registry=registry.opentofu.org
fi

work_dir="$(mktemp -d -t paimon-registry-check.XXXXXX)"
trap 'rm -rf "$work_dir"' EXIT
cat >"$work_dir/main.tf" <<EOF
terraform {
  required_providers {
    paimon = {
      source  = "$registry/apache/paimon"
      version = "= $version"
    }
  }
}
EOF
cat >"$work_dir/cli.tfrc" <<'EOF'
provider_installation {
  direct {}
}
EOF

# Ignore local mirrors, development overrides, and cached provider installs.
export TF_CLI_CONFIG_FILE="$work_dir/cli.tfrc"
export TF_DATA_DIR="$work_dir/data"
unset TF_PLUGIN_CACHE_DIR TF_PLUGIN_CACHE_MAY_BREAK_DEPENDENCY_LOCK_FILE
unset TF_CLI_ARGS TF_CLI_ARGS_init TF_CLI_ARGS_validate TF_CLI_ARGS_providers
"$cli" version
"$cli" -chdir="$work_dir" init -backend=false -input=false -no-color
"$cli" -chdir="$work_dir" validate -no-color
"$cli" -chdir="$work_dir" providers schema -json >"$work_dir/schema.json"
python3 - "$work_dir/schema.json" "$registry/apache/paimon" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    schema = json.load(source)["provider_schemas"][sys.argv[2]]
for name in ("database", "table", "permission", "row_filter", "column_mask"):
    if "paimon_" + name not in schema.get("resource_schemas", {}):
        raise SystemExit("Missing resource schema: " + name)
for name in ("database", "table"):
    if "paimon_" + name not in schema.get("data_source_schemas", {}):
        raise SystemExit("Missing data source schema: " + name)
print("Registry installation and all resource/data-source schemas verified")
PY
