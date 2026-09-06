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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/require"
)

// The fixture exercises Terraform ownership and state mapping. Live server
// compatibility is covered separately by TestAccRealCatalog.
type managementAcceptanceCatalog struct {
	catalog         acceptanceCatalog
	mu              sync.Mutex
	permissions     []client.PermissionAssignment
	policies        map[string]client.DataPolicy
	canonicalize    func(client.PolicyRequest) client.PolicyRequest
	policyMutations int
}

func (c *managementAcceptanceCatalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/permissions") && !strings.Contains(r.URL.Path, "/policies") {
		c.catalog.ServeHTTP(w, r)

		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/policies") {
		c.policyMutations++
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/permissions/grant"):
		var p client.PermissionAssignment
		if json.NewDecoder(r.Body).Decode(&p) != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}
		c.permissions = []client.PermissionAssignment{p}
	case strings.HasSuffix(r.URL.Path, "/permissions/revoke"):
		c.permissions = nil
	case strings.HasSuffix(r.URL.Path, "/permissions"):
		writeAcceptanceJSON(w, client.ListPermissionsResponse{Permissions: c.permissions})
	case strings.HasSuffix(r.URL.Path, "/policies/drop"):
		var body struct {
			Type      string `json:"type"`
			Principal string `json:"principal"`
			Column    string `json:"column"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}
		delete(c.policies, body.Type)
	case r.Method == http.MethodPost:
		var p client.PolicyRequest
		if json.NewDecoder(r.Body).Decode(&p) != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}
		if c.canonicalize != nil {
			p = c.canonicalize(p)
		}
		kind := client.PolicyTypeRowFilter
		if p.ColumnMask != nil {
			kind = client.PolicyTypeColumnMasking
		}
		if _, exists := c.policies[kind]; exists {
			w.WriteHeader(http.StatusConflict)

			return
		}
		c.policies[kind] = client.DataPolicy{Resource: client.PermissionResource{Type: client.ResourceTypeTable, Database: "analytics", Table: "events"}, Principal: p.Principal, RowFilter: p.RowFilter, ColumnMask: p.ColumnMask}
	case r.Method == http.MethodGet:
		policies := make([]client.DataPolicy, 0, 1)
		if p, ok := c.policies[r.URL.Query().Get("type")]; ok {
			policies = append(policies, p)
		}
		writeAcceptanceJSON(w, client.ListPoliciesResponse{Policies: policies})
	default:
		http.NotFound(w, r)
	}
}

func managementResourceConfig(principal string, updated bool) string {
	region, mask, expiry := "APAC", "***", "null"
	if updated {
		region, mask, expiry = "EMEA", "hidden", `"2099-01-01T00:00:00Z"`
	}

	return fmt.Sprintf(`
resource "paimon_permission" "read" {
  resource_type = "TABLE"
  database = paimon_table.events.database
  table = paimon_table.events.name
  access = "SELECT"
  principal = %q
  expire_time = %s
}
resource "paimon_row_filter" "region" {
  database = paimon_table.events.database
  table = paimon_table.events.name
  principal = paimon_permission.read.principal
  allow_non_atomic_update = true
  predicate = jsonencode({
    kind = "LEAF"
    transform = { name = "FIELD_REF", fieldRef = { index = 1, name = "tenant", type = "STRING" } }
    function = "EQUAL"
    literals = [%q]
  })
}
resource "paimon_column_mask" "secret" {
  database = paimon_table.events.database
  table = paimon_table.events.name
  principal = paimon_permission.read.principal
  column = "secret"
  allow_non_atomic_update = true
  transform = jsonencode({ name = "CONCAT", inputs = [%q] })
}
`, principal, expiry, region, mask)
}

func managementTableConfig(database string) string {
	return fmt.Sprintf(`
resource "paimon_database" "analytics" { name = %q }
resource "paimon_table" "events" {
  database = paimon_database.analytics.name
  name = "events"
  fields = [
    { name = "id", type = "BIGINT", nullable = false },
    { name = "tenant", type = "STRING" },
    { name = "secret", type = "STRING" }
  ]
  options = {
    "primary-key" = "id"
    "query-auth.enabled" = "true"
  }
}
`, database)
}

func TestAccManagementResourceLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	catalog := &managementAcceptanceCatalog{policies: make(map[string]client.DataPolicy)}
	server := httptest.NewServer(catalog)
	defer server.Close()
	base := fmt.Sprintf("provider \"paimon\" { uri = %q }\n", server.URL) + managementTableConfig("analytics")
	initial := base + managementResourceConfig("role:analyst", false)
	updated := base + managementResourceConfig("role:analyst", true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: initial},
			{Config: initial, PlanOnly: true},
			{ResourceName: "paimon_permission.read", ImportState: true, ImportStateVerify: true},
			{ResourceName: "paimon_row_filter.region", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"allow_non_atomic_update"}},
			{ResourceName: "paimon_column_mask.secret", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"allow_non_atomic_update"}},
			{Config: updated, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("paimon_permission.read", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("paimon_row_filter.region", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("paimon_column_mask.secret", plancheck.ResourceActionUpdate),
			}}},
			{Config: updated, PlanOnly: true},
		},
	})
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	require.Empty(t, catalog.policies)
	require.Empty(t, catalog.permissions)
}
