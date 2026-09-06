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

# Production validation

Use an exact provider version and retain the dependency lock file. A passing
unit or in-process acceptance suite is evidence of provider behavior, not a
certification of a deployed Catalog or its query engines.

## Coverage and release evidence

| Layer | Repeatable check | Scope / remaining evidence |
| --- | --- | --- |
| Provider and client | `make check` | Unit tests, race detector, build, vet, and example validation |
| Terraform protocol | `make test-acceptance` | Database/table CRUD, schema evolution, immutable-option import, permission/filter/mask CRUD, import and empty plans against an in-process REST fixture |
| OpenTofu protocol | `make test-acceptance-tofu` | Same lifecycle suite using OpenTofu's registry address and CLI |
| Real REST Catalog | `make test-integration` | Supplied isolated endpoint; database/table/data-source lifecycle and schema addition; management lifecycle when a test principal is supplied |
| DLF authentication | Unit tests plus integration with each deployed signing mode | Stable default/OpenAPI signature vectors, rotating credentials, and required-token ECS metadata are covered locally; service authorization and endpoint compatibility require a live run |
| Query enforcement | Flink/Spark tests against the deployment | Verify actual reads, filters, masks, schema changes, and permission revocation using deployed engine versions |
| Distribution | `dev/release/verify_registry.sh VERSION CLI` | Install the signed release through the CLI's public registry, validate configuration, and read provider schemas |

The live suite is skipped when no endpoint is supplied. Do not interpret that
skip as a passing integration check. Record the exact provider commit/release,
CLI version, Catalog build (including server implementation), DLF region and
signing mode, engine versions, job URL, and results for each supported deployment.
No Paimon/DLF minimum server version is claimed from fixture tests. The
permission and policy APIs are experimental and require a server implementing
their persistence and authorization contracts.

Protocol CI pins Terraform 1.13.5 and OpenTofu 1.12.6. Update the pinned version
and its verification checksum together, then rerun the suite before widening
the supported CLI matrix.

## Running against a real Catalog

Provision an isolated warehouse/catalog and, for all five resources, an
existing test principal with no running queries. The runner needs rights to
create, alter, and delete the generated database and its table, and to manage
that principal's permissions and policies on this table. The suite prints its
unique `terraform_acc_*` database name for cleanup if the service becomes
unavailable during teardown.

Configure authentication through the corresponding sensitive `TF_VAR_paimon_*`
environment variables (see the provider configuration reference). Supply secrets
through your CI secret store or local secret manager, without embedding them in
HCL, commands committed to the repository, or test logs. For example, set
`TF_VAR_paimon_token` for bearer authentication, or use
`TF_VAR_paimon_dlf_token_loader=local_file` with
`TF_VAR_paimon_dlf_token_path` and `TF_VAR_paimon_warehouse` for rotating STS.
A compatible REST endpoint and ECS role configuration work the same way.

```bash
export PAIMON_ACC_URI=https://isolated-catalog.example.com
export PAIMON_ACC_ALLOW_MUTATIONS=1
export PAIMON_ACC_PRINCIPAL=role:terraform-test
make test-integration
```

Omitting `PAIMON_ACC_PRINCIPAL` runs only database/table/data-source coverage.
The GitHub **Live catalog acceptance** workflow requires a principal to cover
all resources. Configure its `catalog-integration` environment with the secrets
and variables named in the workflow, and supply the exact server version when
dispatching. Run separately for plain REST, DLF default signing, and DLF OpenAPI
where those combinations are supported. The suite deliberately exercises
non-atomic policy replacement on its isolated table.

The suite checks metadata behavior; a release owner must additionally write
representative records with the deployed engine, check them before and after
schema evolution, assert that a restricted principal sees only permitted rows
and masked columns, and verify revocation denies reads. Retain these results
with the integration job; do not use metadata equality as evidence of query
enforcement.

## Operations and recovery

- Import existing objects before managing them. In particular, a rejected
  policy create does not establish ownership of a pre-existing policy.
- Review the plan with default replacement protection enabled. For retained
  data, also use `lifecycle { prevent_destroy = true }`; removing the resource
  from configuration removes Terraform's lifecycle protection too.
- Omit IDs for new columns in an existing table. Retain existing IDs to express
  renames. Changing immutable options or keys requires a deliberate replacement.
- Pause affected queries before enabling `allow_non_atomic_update` for a policy
  content change, or replacing/removing a policy. Restore the flag to false
  afterwards. Rollback can restore old content but cannot make this API atomic.
- Set `recovery_timeout_seconds` to cover expected visibility delays. After a
  timeout or transport error, refresh and inspect a new plan before retrying;
  the provider does not blindly retry writes. Failed removal verification keeps
  the previous managed keys in state until a read observes the actual result.
- Keep state and backups in an access-controlled backend. Correlate catalog
  errors using the UUID request ID when available; never attach credentials,
  token-file contents, or metadata credential responses to reports.

A production release needs both deployed-server evidence and successful signed
Registry installation. The scripts and workflows make those checks repeatable;
they do not replace Apache release voting or publish a release automatically.
