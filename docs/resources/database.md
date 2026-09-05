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

`name` is required and changing it replaces the resource. `options` contains
only values managed by Terraform; `server_options` exposes the complete map
returned by the catalog. Updates remove only keys previously managed by this
resource, preserving unrelated server values.

The resource also exports `id`, `server_id`, `location`, `owner`,
`created_at`, `created_by`, `updated_at`, and `updated_by`.

Import with the database name:

```bash
terraform import paimon_database.example analytics
```
