---
page_title: "paimon_database Resource - Paimon"
subcategory: ""
description: |-
  Creates and manages a database in an Apache Paimon REST Catalog.
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

# `paimon_database` resource

Creates and manages a Paimon database.

```hcl
resource "paimon_database" "example" {
  name = "analytics"
  options = {
    owner = "data-platform"
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
| `name` | `string` | Required | No | Database name. Changing it replaces the resource. |
| `options` | `map(string)` | Optional | No | Options managed by Terraform. Initially omitted/null means no managed keys. Removing previously managed keys, including omitting the whole map, deletes those keys in place; unrelated server keys are preserved. |
| `id` | `string` | Computed | No | Terraform identity, equal to the database name; also used for import. |
| `server_id` | `string` | Computed | No | Server-assigned database identifier. This is the database object ID, not the Catalog ID. |
| `server_options` | `map(string)` | Computed | No | Complete raw option map returned by the server, including unmanaged keys. |
| `location` | `string` | Computed | No | Database location returned by the server. |
| `owner` | `string` | Computed | No | Owner metadata returned by the server; independent of a similarly named option. |
| `created_at` | `number` | Computed | No | Creation timestamp in milliseconds since the Unix epoch. |
| `created_by` | `string` | Computed | No | Principal that created the object, as reported by the server. |
| `updated_at` | `number` | Computed | No | Last update timestamp in milliseconds since the Unix epoch. |
| `updated_by` | `string` | Computed | No | Principal that last updated the object, as reported by the server. |

<!-- schema:end -->

## Behavior

`name` is required and changing it replaces the resource. `options` contains
only values managed by Terraform; `server_options` exposes the complete map
returned by the catalog. Updates remove only keys previously managed by this
resource, preserving unrelated server values.
Map entries must contain strings; remove a key rather than setting its value
to `null`.

After import, omitted `options` leaves remote options unmanaged. Declaring an
option with its current server value takes ownership without changing that
value. A refresh that finds the database missing removes it from Terraform
state; a subsequent plan can recreate it.

## Import

Import with the database name:

```bash
terraform import paimon_database.example analytics
```
