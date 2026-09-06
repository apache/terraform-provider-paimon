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

## Schema

Required attributes must be configured. Optional attributes may be omitted or
set to `null`; their behavior is described below. Optional + computed attributes
can be configured or resolved by the provider. Computed attributes are read-only.
`number` attributes and values of `map(number)` are integers.

<!-- schema:begin -->

| Attribute | Type | Mode | Sensitive | Meaning, default, and update behavior |
| --- | --- | --- | --- | --- |
| `uri` | `string` | Required | No | REST Catalog base URI, including an optional base path. Must be absolute HTTP or HTTPS with a host and no query or fragment. |
| `warehouse` | `string` | Optional | No | Warehouse identifier sent to `/v1/config`. Required for DLF; otherwise omitted when unset. |
| `request_timeout_seconds` | `number` | Optional | No | Timeout for each REST or credential HTTP request. Default: `30` seconds. Allowed: `1`–`3600`. |
| `recovery_timeout_seconds` | `number` | Optional | No | Time budget for read-back reconciliation after a mutation. Default: `5` seconds. Allowed: `1`–`3600`. Does not enable mutation retries. |
| `token_provider` | `string` | Optional | No | Authentication mode: `bear` or `dlf`. Inferred from DLF configuration or a bearer token when omitted. With neither configured, there is no provider-managed authentication. |
| `token` | `string` | Optional | Yes | Bearer token. Required and nonempty when `token_provider = "bear"`; incompatible with DLF configuration. |
| `prefix` | `string` | Optional | No | Catalog path prefix for `/v1/{prefix}/…`. Server defaults apply when omitted; server overrides take precedence over this value. |
| `headers` | `map(string)` | Optional | Yes | Additional REST request headers; none by default. Provider-managed authentication headers take precedence. |
| `dlf_region` | `string` | Optional | No | Region for DLF default signing. Inferred from standard DLF endpoints; set it explicitly when default signing cannot infer it. |
| `dlf_signing_algorithm` | `string` | Optional | No | Signing algorithm: `default` or `openapi`. Inferred from the endpoint when omitted: DLF VPC/default endpoints use `default`, DLFNext endpoints use `openapi`. |
| `dlf_access_key_id` | `string` | Optional | Yes | Static Alibaba Cloud access key ID. Must be paired with `dlf_access_key_secret`; cannot accompany a dynamic credential loader. |
| `dlf_access_key_secret` | `string` | Optional | Yes | Static Alibaba Cloud access key secret. Must be paired with `dlf_access_key_id`. |
| `dlf_security_token` | `string` | Optional | Yes | Optional STS token for the static access key pair. Omit for a long-lived AK pair. |
| `dlf_token_loader` | `string` | Optional | No | Dynamic credential source: `local_file` or `ecs`. A configured `dlf_token_path` implies `local_file`; otherwise there is no default dynamic loader. |
| `dlf_token_path` | `string` | Optional | No | Path to a rotating AK/STS JSON file. Required for `local_file`; setting it selects that loader when the loader is omitted. |
| `dlf_ecs_metadata_url` | `string` | Optional | No | ECS metadata endpoint override for the `ecs` loader. Default: `http://100.100.100.200/latest/meta-data/Ram/security-credentials/`. Must be an absolute HTTP(S) URL and support the session-token API described below. |
| `dlf_ecs_role_name` | `string` | Optional | No | RAM role used by the `ecs` loader. Discovered from the metadata service when omitted. |

<!-- schema:end -->

## Authentication and request behavior

Values in `headers` must be strings; remove a header key to omit it rather than
setting its value to `null`.

Bearer and DLF configuration are mutually exclusive. An explicit
`token_provider = "bear"` rejects any nonempty `dlf_*` setting; a bearer `token`
cannot accompany DLF settings, even when `token_provider` is inferred. Unknown
values defer the check until configuration resolves.

Configure exactly one DLF credential source: static AK/STS, a local
token file, or an ECS RAM role. DLF Catalog requests also require `warehouse`.
`dlf_ecs_metadata_url` and `dlf_ecs_role_name` only take effect with the `ecs`
loader; omit them when using static credentials or a local file.
See the [static STS](../examples/dlf-sts/main.tf),
[rotating token file](../examples/dlf-token-file/main.tf), and
[ECS role](../examples/dlf-ecs/main.tf) examples.

The provider first calls `/v1/config`, merges server defaults, client values,
and server overrides in that order, and then uses the resulting `prefix` for
catalog operations. Redirects are rejected so authentication headers and DLF
signatures are never reused for a different URL.

The ECS metadata endpoint must support the IMDS session-token API at
`/latest/api/token` on the same origin; the provider does not fall back to
tokenless requests.

The ECS loader obtains and caches an IMDS session token, refreshes it before
expiry, and reacquires it once on an unauthorized metadata response. This
supports instances with `HttpTokens=required`. Catalog error diagnostics expose
HTTP/error codes and, when present, a UUID request ID suitable for support
correlation; arbitrary response text and echoed authentication values are
suppressed.

### Token file format and rotation

The `local_file` loader reads this JSON structure. Replace the example values
with credentials and the expiration supplied by your credential issuer:

```json
{
  "AccessKeyId": "replace-with-access-key-id",
  "AccessKeySecret": "replace-with-access-key-secret",
  "SecurityToken": "replace-with-sts-token",
  "Expiration": "2026-12-31T23:59:59Z"
}
```

`AccessKeyId` and `AccessKeySecret` must be nonblank strings. `SecurityToken`
is optional for a long-lived AK pair. `Expiration` is an optional RFC3339
timestamp, but is needed for automatic rotation: without it, credentials remain
cached for the current provider instance, and replacing the file does not
trigger a reload.

For credentials with an expiration, the file and ECS loaders attempt refresh
on a request when less than one hour remains. This is request-driven, not a
background file watcher. If refresh fails while the cached credentials remain
valid, the provider continues using them and tries again on a later request;
expired credentials are not used. Static AK/STS configuration has no automatic
refresh; select a dynamic loader when credentials need to rotate during a run.

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
