---
page_title: "paimon_row_filter Resource - Paimon"
subcategory: ""
description: |-
  Manages a row filter policy for an Apache Paimon table.
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

# `paimon_row_filter` resource

Manages the one row filter a principal may have on a table. A data policy only
restricts an already-authorized read; it does not grant `SELECT`. The table must
exist with `query-auth.enabled=true`, and the principal must exist on the
server.

```hcl
resource "paimon_row_filter" "analyst_region" {
  database  = "analytics"
  table     = "events"
  principal = "role:analyst"
  predicate = jsonencode({
    kind = "LEAF"
    transform = {
      name = "FIELD_REF"
      fieldRef = {
        index = 0
        name  = "region"
        type  = "STRING"
      }
    }
    function = "EQUAL"
    literals = ["APAC"]
  })
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
| `database` | `string` | Required | No | Nonblank database name. Changing it replaces the policy attachment. |
| `table` | `string` | Required | No | Nonblank table name. The table must exist with `query-auth.enabled=true`. Changing it replaces the policy attachment. |
| `principal` | `string` | Required | No | Existing server principal identifier. Must be nonblank and at most 128 UTF-16 code units. Changing it replaces the policy attachment. |
| `predicate` | `string` | Required | No | Nonempty JSON serialization of one Paimon `Predicate`, at most 60 KiB in UTF-8. The server validates the AST. Meaningful changes require `allow_non_atomic_update = true` and use drop/create. |
| `allow_non_atomic_update` | `bool` | Optional + computed | No | Default: `false`. Permits content updates by dropping and recreating the policy. Does not guard explicit deletion or identity replacement; see the maintenance requirements below. |
| `id` | `string` | Computed | No | Percent-encoded URL-query identity containing database, table, principal. Use the same identity for import. |

<!-- schema:end -->

## Behavior

`predicate` must be the JSON serialization of one Paimon `Predicate` and must
not exceed 60 KiB in UTF-8. The server validates field references and returns a
canonical representation. JSON formatting and object-key ordering alone do not
replace the remote policy; an in-place apply only updates the stored
representation.

Paimon currently has create and drop operations but no policy update operation.
Changing `predicate` is blocked by default. Set
`allow_non_atomic_update = true` only in a maintenance window with affected
queries paused: the provider drops and recreates the policy. If creation fails,
it attempts to restore the previous policy and reports whether restoration
succeeded. This opt-in and rollback do not eliminate the interval without the
filter. Setting the flag alone or changing only JSON formatting does not
recreate the policy. The flag controls content updates, not explicit deletion
or replacement following an attachment/identity change; keep affected queries
paused for those operations too.

A definitively rejected initial create (including HTTP 409 or 403) does not
adopt an existing policy, even if its content matches. Import it explicitly to
establish Terraform ownership. An uncertain response such as a connection loss
is reconciled by reading the exact policy identity and content.

A refresh that finds the policy missing removes it from Terraform state; a
subsequent plan can recreate it. Required attributes cannot be omitted or set
to `null`. Omitted/null `allow_non_atomic_update` resolves to `false`.

## Import

Import with the URL-query identity printed in `id`:

```bash
terraform import paimon_row_filter.analyst_region \
  'database=analytics&table=events&principal=role%3Aanalyst'
```

The provider compares known Java policy AST semantics across create, refresh,
and update: field references resolve by name against the current table schema,
and one-child AND/OR predicates simplify to their child. Equivalent configured
JSON is retained in state. Changes to field names, functions, literal values,
cast target types, or unknown AST members remain meaningful differences. The
Catalog must permit reading the protected table schema for this comparison.
See the [API contract](../api-contract.md) for the exact Java
reference revision and experimental compatibility boundary.
