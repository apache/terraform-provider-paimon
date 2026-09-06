---
page_title: "paimon_permission Resource - Paimon"
subcategory: ""
description: |-
  Manages a direct Apache Paimon catalog permission assignment.
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

# `paimon_permission` resource

Manages one direct permission assignment. Paimon's REST management API is
experimental, and the configured principal must already exist in the server's
principal namespace.

```hcl
resource "paimon_permission" "read_events" {
  resource_type = "TABLE"
  database      = "analytics"
  table         = "events"
  access        = "SELECT"
  principal     = "role:analyst"
  expire_time   = "2026-12-31T23:59:59Z"
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
| `resource_type` | `string` | Required | No | Exact uppercase resource type from the locator table below. Determines required locators and allowed accesses. Changing it replaces the assignment. |
| `access` | `string` | Required | No | Access from the supported-access table below; must use exact uppercase spelling. Changing it replaces the assignment. |
| `principal` | `string` | Required | No | Existing server principal identifier. Must be nonblank and at most 128 UTF-16 code units. Changing it replaces the assignment. |
| `database` | `string` | Optional | No | Required, nonblank database locator for database, table, column, function, and view types; forbidden for catalog types. Changing it replaces the assignment. |
| `table` | `string` | Optional | No | Required, nonblank table locator for `TABLE` and `COLUMN`; forbidden for other types. Changing it replaces the assignment. |
| `function` | `string` | Optional | No | Required, nonblank function locator for `FUNCTION`; forbidden for other types. Changing it replaces the assignment. |
| `view` | `string` | Optional | No | Required, nonblank view locator for `VIEW`; forbidden for other types. Changing it replaces the assignment. |
| `column_names` | `set(string)` | Optional | No | Allowed columns for a `COLUMN` assignment. Exactly one nonempty column range is required; names must be nonblank and non-null. Forbidden for other types. Changes use grant-or-replace. |
| `excluded_column_names` | `set(string)` | Optional | No | Excluded columns for a `COLUMN` assignment, mutually exclusive with `column_names`. Same validation and update behavior; newly added columns remain allowed. |
| `expire_time` | `string` | Optional | No | Exclusive expiry instant in UTC, as detailed below. Omitted/null means no expiry and removes a previously managed expiry; an empty string is invalid. Changes use grant-or-replace. |
| `id` | `string` | Computed | No | Percent-encoded URL-query identity containing resource type, locators, access, and principal. Expiry and column ranges are content, not part of the identity. |

<!-- schema:end -->

## Behavior

The assignment identity is `resource_type` plus its locator, `access`, and
`principal`. Changing an identity attribute replaces the resource. Changing
`expire_time`, `column_names`, or `excluded_column_names` uses Paimon's
grant-or-replace operation.

Optional locators must be omitted or `null` when not applicable; unrelated
locators are rejected. Column ranges are sets, so order does not matter. Empty
sets do not satisfy the required range for a `COLUMN` assignment. Deletion
revokes this direct assignment; principal lifecycle and inherited access remain
server responsibilities. A refresh that finds the assignment missing removes
it from Terraform state.

Resource locators have these exact shapes:

| `resource_type` | Locator attributes |
| --- | --- |
| `CATALOG`, `CATALOG_ALL` | none |
| `DATABASE`, `DATABASE_ALL` | `database` |
| `TABLE`, `COLUMN` | `database`, `table` |
| `FUNCTION` | `database`, `function` |
| `VIEW` | `database`, `view` |

Supported accesses follow the server contract:

| Resource type | Accesses |
| --- | --- |
| `CATALOG` | `ALL`, `ALTER`, `DROP`, `GRANT`, `CREATEDATABASE` |
| `CATALOG_ALL` | `ALL`, `DESCRIBE`, `ALTER`, `DROP`, `GRANT`, `CREATETABLE`, `CREATEVIEW`, `CREATEFUNCTION`, `LIST`, `SELECT`, `UPDATE` |
| `DATABASE` | `ALL`, `DESCRIBE`, `ALTER`, `DROP`, `GRANT`, `CREATETABLE`, `CREATEVIEW`, `CREATEFUNCTION`, `LIST` |
| `DATABASE_ALL` | `ALL`, `SELECT`, `UPDATE`, `ALTER`, `DROP`, `GRANT` |
| `TABLE` | `ALL`, `SELECT`, `UPDATE`, `ALTER`, `DROP`, `GRANT` |
| `COLUMN` | `SELECT` |
| `VIEW`, `FUNCTION` | `ALL`, `SELECT`, `ALTER`, `DROP`, `GRANT` |

A `COLUMN` assignment must set exactly one non-empty column range:

```hcl
resource "paimon_permission" "limited_event_columns" {
  resource_type = "COLUMN"
  database      = "analytics"
  table         = "events"
  access        = "SELECT"
  principal     = "role:analyst"
  column_names  = ["event_id", "event_time"]
}
```

`column_names` is an allowlist and denies columns added later.
`excluded_column_names` is a denylist and allows columns added later. The table
must enable `query-auth.enabled=true` before a column assignment is granted.

`expire_time` is optional. It must be a UTC ISO-8601 instant with a `Z` suffix.
Fractional seconds may contain up to nine digits, but the parsed value must
resolve exactly to milliseconds: `.123000Z` is valid and `.123456Z` is not.
Equivalent spellings are normalized to upper-case `T`/`Z` with no more than
three fractional digits before they are sent to the server. Expiry is an
exclusive upper bound evaluated by the server clock.

## Import

Import with the URL-query identity printed in `id`:

```bash
terraform import paimon_permission.read_events \
  'resource_type=TABLE&database=analytics&table=events&access=SELECT&principal=role%3Aanalyst'
```
