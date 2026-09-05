---
page_title: "Paimon Provider"
description: |-
  Manage Apache Paimon REST Catalog metadata, permissions, and table data policies with Terraform.
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

# Paimon provider

The Paimon provider manages catalog metadata, direct permission assignments,
and table data policies through the Apache Paimon REST Catalog API.

## Configuration

```hcl
provider "paimon" {
  uri       = "https://catalog.example.com"
  warehouse = "production"

  token_provider = "bear"
  token          = var.paimon_token
}
```

### Arguments

- `uri` (required): REST Catalog base URI. A base path is supported.
- `warehouse` (optional): sent as the `warehouse` query parameter to
  `/v1/config`.
- `request_timeout_seconds` (optional): timeout per HTTP request, including
  credential requests. Defaults to 30; allowed range 1–3600.
- `recovery_timeout_seconds` (optional): bounded read-back reconciliation after
  a mutation. Defaults to 5; allowed range 1–3600. Increase it to cover the
  catalog's expected visibility delay. This does not enable mutation retries.
- `token_provider` (optional): `bear` for Bearer authentication or `dlf` for
  Alibaba Cloud DLF AK/STS signing. It is inferred when omitted.
- `token` (optional, sensitive): token used by the `bear` provider.
- `prefix` (optional): client catalog path prefix. A server override returned
  by `/v1/config` takes precedence.
- `headers` (optional, sensitive): additional REST request headers. The
  provider-managed `Authorization` header takes precedence.
- `dlf_region` (optional): region for DLF default signing. Standard DLF
  endpoints allow it to be inferred.
- `dlf_signing_algorithm` (optional): `default` for DLF VPC/default endpoints
  or `openapi` for DLFNext endpoints. It is inferred from standard endpoints.
- `dlf_access_key_id` and `dlf_access_key_secret` (optional, sensitive): a
  static Alibaba Cloud access key pair.
- `dlf_security_token` (optional, sensitive): STS token paired with static
  access keys.
- `dlf_token_loader` (optional): `local_file` for a rotating token file or
  `ecs` for ECS RAM role credentials.
- `dlf_token_path` (optional): path to the rotating AK/STS JSON file. Setting
  it implies the `local_file` loader.
- `dlf_ecs_metadata_url` (optional): compatible ECS metadata endpoint override.
  The endpoint must support the IMDS session-token API at `/latest/api/token`
  on the same origin; the provider does not fall back to tokenless requests.
- `dlf_ecs_role_name` (optional): RAM role name. The `ecs` loader discovers it
  when omitted.

Bearer and DLF configuration are mutually exclusive. An explicit
`token_provider = "bear"` rejects any nonempty `dlf_*` setting; a bearer `token`
cannot accompany DLF settings, even when `token_provider` is inferred. Unknown
values defer the check until configuration resolves. No credential settings
are silently ignored.

Exactly one DLF credential source may be configured: static AK/STS, a local
token file, or an ECS RAM role. DLF Catalog requests also require `warehouse`.
See the [static STS](../examples/dlf-sts/main.tf),
[rotating token file](../examples/dlf-token-file/main.tf), and
[ECS role](../examples/dlf-ecs/main.tf) examples.

The provider first calls `/v1/config`, merges server defaults, client values,
and server overrides in that order, and then uses the resulting `prefix` for
catalog operations. Redirects are rejected so authentication headers and DLF
signatures are never reused for a different URL.

The ECS loader obtains and caches an IMDS session token, refreshes it before
expiry, and reacquires it once on an unauthorized metadata response. This
supports instances with `HttpTokens=required`. Catalog error diagnostics expose
HTTP/error codes and, when present, a UUID request ID suitable for support
correlation; arbitrary response text and echoed authentication values are
suppressed.

## Resources and data sources

- [Database resource](resources/database.md)
- [Table resource](resources/table.md)
- [Permission resource](resources/permission.md)
- [Row-filter resource](resources/row_filter.md)
- [Column-mask resource](resources/column_mask.md)
- [Database data source](data-sources/database.md)
- [Table data source](data-sources/table.md)

The permission and policy resources use Paimon's experimental REST management
contract. Principal lifecycle, type resolution, role membership, persistence,
and authorization enforcement are server responsibilities. See the
[permissions example](../examples/permissions/main.tf).

See the [production validation guide](production-readiness.md) for integration
coverage, compatibility evidence, and rollout checks.

See the [API contract](api-contract.md) for the core resource model and
experimental management boundary.
