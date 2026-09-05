// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information regarding
// copyright ownership. The ASF licenses this file to You under the Apache
// License, Version 2.0 (the "License"); you may not use this file except in
// compliance with the License. You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/require"
)

// This suite uses a supplied, isolated REST service. It never substitutes the
// in-process acceptance fixture for a real-server compatibility result.
func TestAccRealCatalog(t *testing.T) {
	uri := os.Getenv("PAIMON_ACC_URI")
	if os.Getenv("TF_ACC") == "" || uri == "" {
		t.Skip("set TF_ACC=1 and PAIMON_ACC_URI to run live catalog acceptance")
	}
	require.Equal(t, "1", os.Getenv("PAIMON_ACC_ALLOW_MUTATIONS"), "live acceptance creates, alters and deletes its uniquely named database; set PAIMON_ACC_ALLOW_MUTATIONS=1 for an isolated test catalog")
	var suffix [8]byte
	_, err := rand.Read(suffix[:])
	require.NoError(t, err)
	database := fmt.Sprintf("terraform_acc_%x", suffix)
	t.Logf("live acceptance database: %s (retain this name for cleanup if the service is unavailable)", database)
	providerConfig := realCatalogProviderConfig(uri)
	initial := providerConfig + managementTableConfig(database)
	updated := strings.Replace(initial, `{ name = "secret", type = "STRING" }`, `{ name = "secret", type = "STRING" }, { name = "added", type = "STRING" }`, 1)
	sources := `
data "paimon_database" "read" { name = paimon_database.analytics.name }
data "paimon_table" "read" { database = paimon_table.events.database
  name = paimon_table.events.name
}
`
	initial += sources
	updated += sources
	steps := []resource.TestStep{
		{Config: initial, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("data.paimon_database.read", "name", database),
			resource.TestCheckResourceAttr("data.paimon_table.read", "name", "events"),
		)},
		{ResourceName: "paimon_table.events", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"options"}},
		{ResourceName: "paimon_database.analytics", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"options"}},
		{Config: updated, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionUpdate)}}},
		{Config: updated, PlanOnly: true},
	}
	if principal := os.Getenv("PAIMON_ACC_PRINCIPAL"); principal != "" {
		// The caller provisions a test principal; the provider does not manage
		// principals and must not grant rights to a production identity.
		first := updated + managementResourceConfig(principal, false)
		last := updated + managementResourceConfig(principal, true)
		steps = append(steps,
			resource.TestStep{Config: first},
			resource.TestStep{ResourceName: "paimon_permission.read", ImportState: true, ImportStateVerify: true},
			resource.TestStep{ResourceName: "paimon_row_filter.region", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"allow_non_atomic_update"}},
			resource.TestStep{ResourceName: "paimon_column_mask.secret", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"allow_non_atomic_update"}},
			resource.TestStep{Config: last},
			resource.TestStep{Config: last, PlanOnly: true},
		)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
}

func realCatalogProviderConfig(uri string) string {
	// Secrets stay in TF_VAR_* environment variables; they are never embedded
	// into test configurations or failure messages.
	attributes := []string{"token_provider", "token", "warehouse", "prefix", "dlf_region", "dlf_signing_algorithm", "dlf_access_key_id", "dlf_access_key_secret", "dlf_security_token", "dlf_token_loader", "dlf_token_path", "dlf_ecs_metadata_url", "dlf_ecs_role_name"}
	var variables, configuration strings.Builder
	fmt.Fprintf(&configuration, "provider \"paimon\" {\n  uri = %q\n  recovery_timeout_seconds = 60\n", uri)
	for _, attribute := range attributes {
		fmt.Fprintf(&variables, "variable \"paimon_%s\" {\n  type = string\n  default = null\n  sensitive = true\n}\n", attribute)
		fmt.Fprintf(&configuration, "  %s = var.paimon_%s\n", attribute, attribute)
	}
	configuration.WriteString("  headers = var.paimon_headers\n}\n")
	variables.WriteString("variable \"paimon_headers\" {\n  type = map(string)\n  default = {}\n  sensitive = true\n}\n")

	return variables.String() + configuration.String()
}
