---
page_title: "paimon_table Data Source - Paimon"
subcategory: ""
description: |-
  Reads metadata for an existing Apache Paimon table.
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

# `paimon_table` data source

```hcl
data "paimon_table" "events" {
  database = "analytics"
  name     = "events"
}
```

Reads metadata without creating, updating, or importing the remote table.
A missing table produces an error rather than an empty result. Required lookup
attributes cannot be omitted or set to `null`.

```hcl
output "table_server_id" {
  value = data.paimon_table.events.server_id
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
| `database` | `string` | Required | No | Database containing the existing table to read. |
| `name` | `string` | Required | No | Name of the existing table to read. |
| `id` | `string` | Computed | No | Terraform data-source identity in percent-encoded URL-query form, for example `database=analytics&table=events`. |
| `server_id` | `string` | Computed | No | Server-assigned table object identifier, distinct from Terraform `id` and the Catalog ID. |
| `fields` | `list(object)` | Computed | No | All top-level fields in schema order. Nested object attributes are documented below. |
| `partition_keys` | `list(string)` | Computed | No | Ordered partition field names. Empty for an unpartitioned table. |
| `primary_keys` | `list(string)` | Computed | No | Normalized, ordered primary-key field names. Empty when the table has no primary keys. |
| `options` | `map(string)` | Computed | No | Complete raw table option map. Normally excludes the Java-consumed `primary-key` option; read `primary_keys` instead. This data source takes no ownership of options. |
| `comment` | `string` | Computed | No | Table comment; `null` when absent. An explicit empty string is distinct from absence. |
| `schema_id` | `number` | Computed | No | Current schema version identifier returned by the server. |
| `path` | `string` | Computed | No | Table storage path returned by the server. |
| `is_external` | `bool` | Computed | No | Whether the server reports an external table. |
| `owner` | `string` | Computed | No | Owner metadata returned by the server; independent of a similarly named option. |
| `created_at` | `number` | Computed | No | Creation timestamp in milliseconds since the Unix epoch. |
| `created_by` | `string` | Computed | No | Principal that created the object, as reported by the server. |
| `updated_at` | `number` | Computed | No | Last update timestamp in milliseconds since the Unix epoch. |
| `updated_by` | `string` | Computed | No | Principal that last updated the object, as reported by the server. |

### Nested `fields` attributes

| Attribute | Type | Mode | Sensitive | Meaning, default, and update behavior |
| --- | --- | --- | --- | --- |
| `fields[].id` | `number` | Computed | No | Stable top-level field identifier assigned by Paimon. |
| `fields[].name` | `string` | Computed | No | Top-level field name. |
| `fields[].type` | `string` | Computed | No | Paimon SQL type string. Top-level nullability is reported separately in `nullable`. |
| `fields[].nullable` | `bool` | Computed | No | Whether this field accepts null values. |
| `fields[].description` | `string` | Computed | No | Field comment; `null` when absent. An empty string is distinct from absence. |
| `fields[].default_value` | `string` | Computed | No | Constant default represented as a string, or `null` when absent. An empty string and the literal text `NULL` are distinct from no default. |
| `fields[].nested_field_ids` | `map(number)` | Computed | No | Stable nested ROW field IDs keyed by escaped field paths. Path components escape `~` as `~0` and `/` as `~1`; empty when there are no nested ROW fields. |

<!-- schema:end -->

## Missing server metadata

Comments and field defaults that are absent on the server are reported as `null`
where those attributes exist. Other omitted string metadata is reported as `""`,
omitted numeric metadata as `0`, and an omitted external-table flag as `false`.
Empty collections remain empty lists/maps. Do not interpret a zero audit
timestamp as evidence that the object was created at the Unix epoch.
