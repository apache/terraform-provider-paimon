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
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/apache/terraform-provider-paimon/internal/client"
	accresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/require"
)

func TestAccImportMatchingImmutableOptionPreservesTable(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	catalog := &acceptanceCatalog{
		database: &client.Database{ID: "db-1", Name: "analytics", Options: map[string]string{}},
		table: &client.Table{ID: "table-1", Database: "analytics", Name: "events", SchemaID: 1, Schema: client.Schema{
			Fields:      []client.Field{{ID: 0, Name: "id", Type: "BIGINT NOT NULL"}},
			PrimaryKeys: []string{"id"}, Options: map[string]string{"merge-engine": "deduplicate"},
		}},
	}
	server := httptest.NewServer(catalog)
	defer server.Close()
	config := fmt.Sprintf(`
provider "paimon" { uri = %q }
resource "paimon_table" "events" {
  database = "analytics"
  name = "events"
  fields = [{name="id",type="BIGINT",nullable=false}]
  options = {
    "primary-key" = "id"
    "merge-engine" = "deduplicate"
  }
}
`, server.URL)
	accresource.Test(t, accresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []accresource.TestStep{
			{Config: config, ResourceName: "paimon_table.events", ImportState: true, ImportStateId: "database=analytics&table=events", ImportStatePersist: true},
			{Config: config, PlanOnly: true, ExpectNonEmptyPlan: true, ConfigPlanChecks: accresource.ConfigPlanChecks{PostApplyPreRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionUpdate)}}},
			{Config: config},
			{Config: config, PlanOnly: true},
		},
	})
	require.Zero(t, catalog.tableCreates)
	require.Zero(t, catalog.tableAlters, "taking ownership of the same immutable option must not alter the catalog")
}
