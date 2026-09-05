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

# Paimon Terraform Provider

This project integrates Terraform and OpenTofu with an Apache Paimon REST
Catalog. It follows the provider shape established by
[`apache/terraform-provider-iceberg`](https://github.com/apache/terraform-provider-iceberg),
but talks directly to Paimon's language-neutral REST Catalog API instead of
embedding a JVM client.

Releases use signed Apache release candidates. See the
[release guide](docs/releasing.md) for the source-vote and Registry workflow,
including verification of a published version before installation.

## Supported objects

Resources:

- `paimon_database` creates, reads, updates, imports, and drops databases.
- `paimon_table` creates, reads, imports, evolves supported table fields,
  updates options/comments, and drops managed tables.
- `paimon_permission` grants, reads, imports, replaces mutable assignment
  content, and revokes direct catalog permissions.
- `paimon_row_filter` manages one principal's row filter on a table.
- `paimon_column_mask` manages one principal's mask on a table column.

Data sources:

- `paimon_database` reads a database and its server metadata.
- `paimon_table` reads a table schema, keys, options, and server metadata.

## Example

```hcl
terraform {
  required_providers {
    paimon = {
      source = "apache/paimon"
    }
  }
}

provider "paimon" {
  uri       = "http://localhost:8080"
  warehouse = "default"

  token_provider = "bear"
  token          = var.paimon_token
}

resource "paimon_database" "analytics" {
  name = "analytics"
  options = {
    owner = "data-platform"
  }
}

resource "paimon_table" "events" {
  database = paimon_database.analytics.name
  name     = "events"

  fields = [
    {
      name     = "event_id"
      type     = "BIGINT"
      nullable = false
    },
    {
      name = "event_time"
      type = "TIMESTAMP(3)"
    },
    {
      name = "payload"
      type = "STRING"
    }
  ]

  partition_keys = []
  options = {
    "primary-key" = "event_id"
    "bucket"      = "4"
  }
  comment = "Events managed by Terraform"
}

resource "paimon_permission" "analyst_read" {
  resource_type = "TABLE"
  database      = paimon_table.events.database
  table         = paimon_table.events.name
  access        = "SELECT"
  principal     = "role:analyst"
}
```

## Lifecycle and safety

Only the REST metastore is in scope. Filesystem, Hive, and JDBC catalogs are
not accessed directly because Terraform needs a stable remote control-plane
contract; Paimon's REST OpenAPI provides that contract.

Permission, row-filter, and column-mask resources use Paimon's experimental
REST management API. Principal lifecycle and group or role membership remain
server responsibilities.

Table replacements are blocked unless `allow_replacement = true`.
Protect retained production data with Terraform's `prevent_destroy` lifecycle
rule as shown in the [production example](examples/production/main.tf).
Row-filter and column-mask content updates require
`allow_non_atomic_update = true` during a maintenance window because the REST
API cannot update a policy atomically. Import existing policies explicitly.

The [production validation guide](docs/production-readiness.md) distinguishes
local protocol coverage from real REST/DLF and query-engine validation.

## Import

```bash
terraform import paimon_database.analytics analytics
terraform import paimon_table.events analytics.events
terraform import paimon_permission.analyst_read \
  'resource_type=TABLE&database=analytics&table=events&access=SELECT&principal=role%3Aanalyst'
```

## Development

Use the Go version specified in `go.mod` or a newer compatible version.

```bash
make check
make test-acceptance
```

See [`docs/index.md`](docs/index.md) for the full provider configuration and
resource notes.

See the [API contract](docs/api-contract.md) for the current resource model and
experimental management boundary. Primary keys use `options["primary-key"]`;
`primary_keys` is a computed output.
