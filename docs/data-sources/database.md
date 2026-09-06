---
page_title: "paimon_database Data Source - Paimon"
subcategory: ""
description: |-
  Reads metadata for an existing Apache Paimon database.
---

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

# `paimon_database` data source

```hcl
data "paimon_database" "analytics" {
  name = "analytics"
}
```

Reads metadata without creating, updating, or importing the remote database.
A missing database produces an error rather than an empty result. Required lookup
attributes cannot be omitted or set to `null`.

```hcl
output "database_server_id" {
  value = data.paimon_database.analytics.server_id
}
```

## Schema

Required attributes must be configured. Optional attributes may be omitted or
set to `null`; their behavior is described below. Optional + computed attributes
can be configured or resolved by the provider. Computed attributes are read-only.
`number` attributes and values of `map(number)` are integers.

<!-- schema:begin -->

| Attribute | Type | Mode | Sensitive | Meaning, default, and update behavior |
| --- | --- | --- | --- | --- |
| `name` | `string` | Required | No | Name of the existing database to read. |
| `id` | `string` | Computed | No | Terraform data-source identity, equal to the database name. |
| `server_id` | `string` | Computed | No | Server-assigned database object identifier, not the Catalog ID. |
| `options` | `map(string)` | Computed | No | Complete raw database option map returned by the server. This data source takes no ownership of options. |
| `location` | `string` | Computed | No | Database location returned by the server. |
| `owner` | `string` | Computed | No | Owner metadata returned by the server; independent of a similarly named option. |
| `created_at` | `number` | Computed | No | Creation timestamp in milliseconds since the Unix epoch. |
| `created_by` | `string` | Computed | No | Principal that created the object, as reported by the server. |
| `updated_at` | `number` | Computed | No | Last update timestamp in milliseconds since the Unix epoch. |
| `updated_by` | `string` | Computed | No | Principal that last updated the object, as reported by the server. |

<!-- schema:end -->

## Missing server metadata

Omitted string metadata is reported as `""` and omitted numeric metadata as `0`.
Empty collections remain empty lists/maps. Do not interpret a zero audit
timestamp as evidence that the object was created at the Unix epoch.
