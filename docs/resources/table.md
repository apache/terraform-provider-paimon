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

Dropping a managed table can delete its data. Use `prevent_destroy` where
appropriate:

```hcl
lifecycle {
  prevent_destroy = true
}
```

Import with the unambiguous URL-query identity. The legacy `database.table`
form remains accepted when names do not contain dots:

```bash
terraform import paimon_table.example 'database=analytics&table=events'
```
