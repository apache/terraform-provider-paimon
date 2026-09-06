---
page_title: "paimon_table Resource - Paimon"
subcategory: ""
description: |-
  Creates, evolves, and manages a table in an Apache Paimon REST Catalog.
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

# `paimon_table` resource

Creates and manages a Paimon table.

```hcl
resource "paimon_table" "example" {
  database = "analytics"
  name     = "events"

  fields = [
    {
      name     = "id"
      type     = "BIGINT"
      nullable = false
    },
    {
      name = "payload"
      type = "STRING"
    }
  ]

  options = {
    "primary-key" = "id"
    bucket        = "4"
  }
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
| `database` | `string` | Required | No | Database containing the table. Changing it requires replacement and `allow_replacement = true`. |
| `name` | `string` | Required | No | Table name. Changing it requires replacement and `allow_replacement = true`. |
| `fields` | `list(object)` | Required | No | Ordered, complete list of top-level fields. Removing an entry requests a column drop. See the nested schema and supported evolution below. |
| `partition_keys` | `list(string)` | Optional + computed | No | Ordered partition field names. Omitted/null means no partitions on creation and inherits existing keys on update/import. Explicit `[]` requests no partitions. Changing keys requires replacement. |
| `options` | `map(string)` | Optional | No | String-valued Paimon options managed by Terraform, including `primary-key`. No managed keys initially when omitted/null. Removing managed keys deletes them; other server keys survive. Mutable changes update in place; immutable changes require replacement. `partition` is reserved. |
| `comment` | `string` | Optional | No | Table comment. Omitted/null means no comment and clears a previous comment, including after import. `""` is an explicit empty string. Changes update in place. |
| `allow_replacement` | `bool` | Optional + computed | No | Default: `false`. Set to `true` to permit required replacement, which can delete table data. Does not block explicit destroy or resource removal. |
| `id` | `string` | Computed | No | Terraform/import identity in URL-query form, for example `database=analytics&table=events`, with names percent-encoded. |
| `server_id` | `string` | Computed | No | Server-assigned table object identifier; distinct from Terraform `id` and the Catalog ID. |
| `primary_keys` | `list(string)` | Computed | No | Normalized, ordered primary-key field names. Configure keys with `options["primary-key"]`, not this attribute. |
| `server_options` | `map(string)` | Computed | No | Complete raw server option map. Normally excludes the `primary-key` option consumed by Java schema normalization; use `primary_keys` to read those keys. |
| `schema_id` | `number` | Computed | No | Current schema version identifier returned by the server. |
| `path` | `string` | Computed | No | Table storage path returned by the server. |
| `is_external` | `bool` | Computed | No | Whether the server reports an external table. This is an output, not an external-table creation setting. |
| `owner` | `string` | Computed | No | Owner metadata returned by the server; independent of a similarly named option. |
| `created_at` | `number` | Computed | No | Creation timestamp in milliseconds since the Unix epoch. |
| `created_by` | `string` | Computed | No | Principal that created the object, as reported by the server. |
| `updated_at` | `number` | Computed | No | Last update timestamp in milliseconds since the Unix epoch. |
| `updated_by` | `string` | Computed | No | Principal that last updated the object, as reported by the server. |

### Nested `fields` attributes

| Attribute | Type | Mode | Sensitive | Meaning, default, and update behavior |
| --- | --- | --- | --- | --- |
| `fields[].name` | `string` | Required | No | Unique top-level field name. Retain its ID when renaming an existing field. |
| `fields[].type` | `string` | Required | No | Nonempty Paimon SQL type string, such as `BIGINT`, `STRING`, or `ROW<item STRING>`. A `NOT NULL` suffix must agree with `nullable = false`. |
| `fields[].id` | `number` | Optional + computed | No | Stable, unique integer ID from `0` through `1073741822`. May be supplied on initial creation or to identify a retained field. Omit for a new field added to an existing table so Paimon assigns it. |
| `fields[].nullable` | `bool` | Optional + computed | No | Omitted/null inherits a retained field’s nullability. New non-key fields default to `true` unless the type has `NOT NULL`; primary keys follow `primary-key.nullable`, default `false`. |
| `fields[].description` | `string` | Optional | No | Field comment. Omitted/null clears an existing description, including after import. `""` is an explicit empty string. Updates in place. |
| `fields[].default_value` | `string` | Optional | No | Constant string cast by Paimon to the field type, not a SQL expression. Omitted/null clears an existing default; `""` is a distinct constant. Updates in place when supported by the server. |
| `fields[].nested_field_ids` | `map(number)` | Computed | No | Stable IDs of nested ROW fields keyed by escaped field paths. Read-only; retained across supported changes. In path components, `~0` encodes `~` and `~1` encodes `/`. |

<!-- schema:end -->

## Behavior

Each field supports `id`, `name`, `type`, `nullable`, `description`, and
`default_value`. `nested_field_ids` is a computed map that preserves the stable
IDs of nested ROW fields. Top-level field IDs must be unique integers from 0
through 1073741822. IDs may be specified when creating a table or retaining an
existing field (including a rename). Omit `id` when adding a field to an
existing table: Paimon assigns that ID, and the provider reads it back. Explicit
new IDs are rejected before mutation. Use
canonical Paimon SQL type strings such as `INT`, `BIGINT`, `STRING`,
`DECIMAL(12, 2)`, `ARRAY<STRING>`, or `ROW<item STRING>`.
An explicit `NOT NULL` suffix is preserved across reads and must agree with
`nullable = false`; spelling differences alone do not cause perpetual plans.

`default_value` is a constant represented as a string, matching Java
`DefaultValueUtils`, not an evaluated SQL expression. Examples include `"42"`
for BIGINT, `"true"` for BOOLEAN, and `"2026-01-02"` for DATE. The server casts
the string to the field type and validates supported conversions. `"1 + 2"`
does not compute a BIGINT and `"CURRENT_TIMESTAMP"` does not compute a timestamp;
for STRING they are literal text. Omit the attribute or set it to HCL `null`
to remove a default. `""` is an explicit empty string, distinct from no default;
`"NULL"` is text, not HCL null. The provider passes constants unchanged and does
not attempt to reproduce the server's cast engine.

Supported top-level field additions, drops, renames, atomic type/nullability
changes, comments, defaults, and position changes use Paimon's in-place
`SchemaChange` API. Stable field IDs distinguish a rename from a drop plus add.
Rename cycles require an intermediate name. Adding a non-nullable field,
changing the type of a retained partition/primary-key field, or changing an
existing composite `ROW`, `ARRAY`, `MAP`, `MULTISET`, or `VECTOR` shape replaces
the table because the provider does not yet implement those changes in place.
Paimon itself supports some nested field evolution; this is a provider limitation.
`database`, `name`, `partition_keys`, and `options["primary-key"]` also require
replacement. These changes are rejected by default. Set
`allow_replacement = true` only when you intend to replace the table
and have accounted for data deletion. This setting controls replacement plans;
it does not block an explicit destroy or removal of the resource from
configuration.
On an existing table, configured `database`, `name`, or `partition_keys` values
that are unknown during planning also require replacement opt-in: the provider
cannot establish that they are unchanged until the dependency resolves.
Configure primary keys using the Java option `options["primary-key"] = "id,tenant"`.
Paimon trims each comma-separated name and ignores empty entries; order remains
significant. `primary_keys` is a computed output, not a configuration argument.
The provider sends the option at creation, reads normalized schema keys back,
and preserves equivalent option spelling in state. `server_options` contains
the raw server map, which normally omits the consumed `primary-key` option.
Configure partition keys with `partition_keys`; `options["partition"]` is rejected.

On import, omitted keys and field nullability inherit the existing table.
Adding the same `primary-key` option later acquires management without an ALTER
or replacement. Removing a previously managed `primary-key` option means an
empty primary key list and requires replacement when the old list was nonempty.
An explicitly empty option likewise requests no primary keys. For new fields,
omitted `nullable` defaults to true except for primary keys, whose nullability
follows `primary-key.nullable` (false by default). An explicitly configured
primary-key field's nullability must agree with that option.

Mutable `options` and `comment` update in place. Changing or removing an option
that Paimon defines as immutable, such as `merge-engine`, `bucket-key`, `type`,
or `primary-key.nullable`, requires the same replacement opt-in. After import,
adding an option to configuration with the value already present in
`server_options` takes ownership without replacing or altering the table.
Unmanaged server options are preserved and exposed through `server_options`.
Option removals are successful only when a read confirms their absence. If
verification times out, the provider reports an error and retains the previous
managed keys; refresh observes the eventual result and the next plan can retry
any remaining change.

### Omitted and null values

Omission and HCL `null` have the same meaning for optional attributes, but that
meaning depends on the attribute. Map entries must contain strings: remove an
option's key from the map rather than assigning `null` to its value.

| Input | New table | Existing or imported table |
| --- | --- | --- |
| `partition_keys` | No partition keys | Inherit existing keys; use `[]` to request removal |
| `options` | No managed options | Preserve unmanaged keys and remove keys previously managed by this resource |
| Omitted `primary-key` entry in `options` | No primary keys | Inherit unmanaged keys; removing a previously managed option requests no primary keys |
| `comment`, `fields[].description`, `fields[].default_value` | Absent | Clear the previous value; these do not inherit imported values |
| `fields[].id`, `fields[].nullable` | Resolve using the field rules above | Inherit the retained field's identity/nullability |
| `allow_replacement` | `false` | `false`; opt in explicitly for each configuration that needs replacement |

The `fields` list is always authoritative. Import does not make omitted field
entries unmanaged. Include every field you intend to retain. Each partition or
primary-key name must refer to a field, and key lists must not contain duplicates.
`partition_keys` elements may be unknown during planning, but must not be `null`.
A refresh that finds the table missing removes it from Terraform state; a
subsequent plan can recreate it.

### Immutable options

The provider requires replacement when the effective value of any of these
managed options changes or is removed:

```
aggregation.remove-record-on-delete
blob-descriptor-field
blob-field
blob-view-field
bucket-function.type
bucket-key
data-evolution.enabled
data-file.path-directory
dynamic-bucket.initial-buckets
force-lookup
index-file-in-data-file-dir
merge-engine
partial-update.remove-record-on-delete
partial-update.remove-record-on-sequence-group
pk-clustering-override
primary-key
primary-key.nullable
row-tracking.enabled
rowkind.field
sequence.snapshot-ordering
type
video-frame-field
```

`partition` is also immutable in Paimon, but cannot be configured in `options`;
use `partition_keys`. For `type`, omission has the effective default `table`,
and comparisons ignore case. Other option names and values are passed through
to the deployed server, which validates their support and constraints. This
reference documents the provider's handling of options, not every Paimon engine
option.

### Destruction

Dropping a managed table can delete its data. Use `prevent_destroy` where
appropriate:

```hcl
lifecycle {
  prevent_destroy = true
}
```

## Import

Import with the unambiguous URL-query identity. The legacy `database.table`
form remains accepted when names do not contain dots:

```bash
terraform import paimon_table.example 'database=analytics&table=events'
```
