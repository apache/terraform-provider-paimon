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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	accresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/require"
)

func TestProviderRejectsMixedAuthentication(t *testing.T) {
	ctx := context.Background()
	p := &paimonProvider{}
	var schema frameworkprovider.SchemaResponse
	p.Schema(ctx, frameworkprovider.SchemaRequest{}, &schema)
	model := paimonProviderModel{Headers: types.MapNull(types.StringType), URI: types.StringValue("http://localhost:8080"), TokenProvider: types.StringValue("bear"), Token: types.StringValue("dummy-token"), DLFTokenLoader: types.StringValue("ecs")}
	state := tfsdk.State{Schema: schema.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	var response frameworkprovider.ConfigureResponse
	p.Configure(ctx, frameworkprovider.ConfigureRequest{Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}}, &response)
	require.True(t, response.Diagnostics.HasError(), "explicit bearer plus ECS DLF credentials must be rejected")
}

func TestPolicyCreationAcceptsJavaCanonicalFieldReference(t *testing.T) {
	planned := `{"kind":"LEAF","transform":{"name":"FIELD_REF","fieldRef":{"index":99,"name":"tenant","type":"STRING"}},"function":"EQUAL","literals":["APAC"]}`
	canonical := `{"kind":"LEAF","transform":{"name":"FIELD_REF","fieldRef":{"index":1,"name":"tenant","type":"STRING"}},"function":"EQUAL","literals":["APAC"]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeProviderJSON(t, w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/catalog/databases/db/tables/tbl" {
			writeProviderJSON(t, w, client.Table{Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT"}, {ID: 1, Name: "tenant", Type: "STRING"}}}})

			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)

			return
		}
		writeProviderJSON(t, w, client.ListPoliciesResponse{Policies: []client.DataPolicy{{Resource: client.PermissionResource{Type: client.ResourceTypeTable, Database: "db", Table: "tbl"}, Principal: "role:reader", RowFilter: &client.RowFilter{Predicate: canonical}}}})
	}))
	defer server.Close()
	api, err := client.New(client.Config{URI: server.URL, RecoveryTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	spec := rowFilterSpec(rowFilterResourceModel{Database: types.StringValue("db"), Table: types.StringValue("tbl"), Principal: types.StringValue("role:reader"), Predicate: types.StringValue(planned)})
	result := createPolicyWithReconciliation(context.Background(), api, spec, false)
	require.True(t, result.accepted)
	require.NoError(t, result.err, "Java's name-based field-ref remapping must not be mistaken for a failed mutation")
}

func TestAccImportPreservesUnconfiguredKeysAndNullability(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	for _, nullable := range []bool{false, true} {
		t.Run(strconv.FormatBool(nullable), func(t *testing.T) {
			catalog := &acceptanceCatalog{database: &client.Database{Name: "analytics", Options: map[string]string{}}, table: &client.Table{ID: "table-1", Database: "analytics", Name: "events", SchemaID: 1, Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT NOT NULL"}}, PrimaryKeys: []string{"id"}, Options: map[string]string{"owner": "old"}}}}
			if nullable {
				catalog.table.Schema.Fields[0].Type = "BIGINT"
				catalog.table.Schema.Options["primary-key.nullable"] = "true"
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/databases/analytics/tables/events" {
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.JSONEq(t, `{"changes":[{"action":"setOption","key":"owner","value":"new"}]}`, string(body))
					r.Body = io.NopCloser(bytes.NewReader(body))
				}
				catalog.ServeHTTP(w, r)
			}))
			defer server.Close()
			config := fmt.Sprintf(`
provider "paimon" {
 uri = %q
 recovery_timeout_seconds = 1
}
resource "paimon_table" "events" {
 database = "analytics"
 name = "events"
 fields = [{ name = "id", type = "BIGINT" }]
 options = { owner = "new" }
}
`, server.URL)
			accresource.Test(t, accresource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []accresource.TestStep{
				{Config: config, ResourceName: "paimon_table.events", ImportState: true, ImportStateId: "database=analytics&table=events", ImportStatePersist: true},
				{Config: config},
				{Config: config, PlanOnly: true},
			}})
		})
	}
}

func TestAccImportPreservesUnconfiguredKeys(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	catalog := &acceptanceCatalog{database: &client.Database{Name: "analytics", Options: map[string]string{}}, table: &client.Table{ID: "table-1", Database: "analytics", Name: "events", SchemaID: 1, Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT NOT NULL"}}, PrimaryKeys: []string{"id"}, Options: map[string]string{"owner": "old"}}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/databases/analytics/tables/events" {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.JSONEq(t, `{"changes":[{"action":"setOption","key":"owner","value":"new"}]}`, string(body))
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		catalog.ServeHTTP(w, r)
	}))
	defer server.Close()
	config := fmt.Sprintf(`
provider "paimon" {
 uri = %q
 recovery_timeout_seconds = 1
}
resource "paimon_table" "events" {
 database = "analytics"
 name = "events"
 fields = [{ name = "id", type = "BIGINT", nullable = false }]
 options = { owner = "new" }
}
`, server.URL)
	accresource.Test(t, accresource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []accresource.TestStep{
		{Config: config, ResourceName: "paimon_table.events", ImportState: true, ImportStateId: "database=analytics&table=events", ImportStatePersist: true},
		{Config: config},
		{Config: config, PlanOnly: true},
	}})
}

func TestPolicySemanticComparisonPreservesMeaning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeProviderJSON(t, w, client.ConfigResponse{})

			return
		}
		writeProviderJSON(t, w, client.Table{Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT"}, {ID: 1, Name: "tenant", Type: "STRING"}}}})
	}))
	defer server.Close()
	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	leaf := `{"kind":"LEAF","transform":{"name":"FIELD_REF","fieldRef":{"index":99,"name":"tenant","type":"INT"}},"function":"EQUAL","literals":["APAC"]}`
	canonical := strings.ReplaceAll(strings.ReplaceAll(leaf, `"index":99`, `"index":1`), `"type":"INT"`, `"type":"STRING"`)
	cast := `{"name":"CAST","fieldRef":{"index":99,"name":"tenant","type":"INT"},"type":"STRING"}`
	for _, test := range []struct {
		name, kind, left, right string
		equal, invalid          bool
	}{
		{name: "name-based reference", kind: client.PolicyTypeRowFilter, left: leaf, right: canonical, equal: true},
		{name: "single child", kind: client.PolicyTypeRowFilter, left: `{"kind":"COMPOUND","function":"AND","children":[` + leaf + `]}`, right: canonical, equal: true},
		{name: "changed literal", kind: client.PolicyTypeRowFilter, left: leaf, right: strings.ReplaceAll(canonical, "APAC", "EMEA")},
		{name: "changed operator", kind: client.PolicyTypeRowFilter, left: leaf, right: strings.ReplaceAll(canonical, "EQUAL", "NOT_EQUAL")},
		{name: "cast target alias", kind: client.PolicyTypeColumnMasking, left: strings.ReplaceAll(cast, `},"type":"STRING"`, `},"type":"INTEGER"`), right: strings.ReplaceAll(cast, `},"type":"STRING"`, `},"type":"INT"`), equal: true},
		{name: "cast target nullability", kind: client.PolicyTypeColumnMasking, left: cast, right: strings.ReplaceAll(cast, `},"type":"STRING"`, `},"type":"STRING NOT NULL"`)},
		{name: "cast target", kind: client.PolicyTypeColumnMasking, left: cast, right: strings.ReplaceAll(cast, `},"type":"STRING"`, `},"type":"BIGINT"`)},
		{name: "string transform inputs", kind: client.PolicyTypeColumnMasking, left: `{"name":"CONCAT","inputs":[{"index":99,"name":"tenant","type":"INT"},"!"]}`, right: `{"name":"CONCAT","inputs":[{"index":1,"name":"tenant","type":"STRING"},"!"]}`, equal: true},
		{name: "changed reference name", kind: client.PolicyTypeRowFilter, left: leaf, right: strings.ReplaceAll(canonical, `"name":"tenant"`, `"name":"id"`)},
		{name: "unknown field", kind: client.PolicyTypeRowFilter, left: leaf, right: strings.ReplaceAll(canonical, `"name":"tenant"`, `"name":"absent"`), invalid: true},
		{name: "unknown compound attribute", kind: client.PolicyTypeRowFilter, left: `{"kind":"COMPOUND","function":"AND","children":[` + leaf + `],"extra":true}`, right: canonical},
		{name: "literal metadata is meaningful", kind: client.PolicyTypeRowFilter, left: strings.ReplaceAll(leaf, `["APAC"]`, `[{"name":"tenant","type":"INT","index":99}]`), right: strings.ReplaceAll(canonical, `["APAC"]`, `[{"name":"tenant","type":"STRING","index":1}]`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			equal, err := equivalentPolicyContent(context.Background(), api, policySpec{database: "db", table: "tbl", policyType: test.kind}, test.left, test.right)
			require.Equal(t, test.invalid, err != nil)
			require.Equal(t, test.equal, equal)
		})
	}
}

func TestAccPrimaryKeyOptionLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	catalog := &acceptanceCatalog{database: &client.Database{Name: "analytics", Options: map[string]string{}}}
	server := httptest.NewServer(catalog)
	defer server.Close()
	config := func(option string, allowed bool) string {
		return fmt.Sprintf(`
provider "paimon" { uri = %q }
resource "paimon_table" "events" {
 database = "analytics"
 name = "events"
 fields = [{name="id",type="BIGINT"},{name="tenant",type="STRING"}]
 allow_replacement = %t
 options = { %s }
}
`, server.URL, allowed, option)
	}
	initial := config(`"primary-key"=" , id , ,tenant, "`, false)
	removed := config("", true)
	accresource.Test(t, accresource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []accresource.TestStep{
		{Config: initial, Check: accresource.ComposeTestCheckFunc(accresource.TestCheckResourceAttr("paimon_table.events", "primary_keys.0", "id"), accresource.TestCheckResourceAttr("paimon_table.events", "primary_keys.1", "tenant"), accresource.TestCheckResourceAttr("paimon_table.events", "fields.0.nullable", "false"), accresource.TestCheckResourceAttr("paimon_table.events", "options.primary-key", " , id , ,tenant, "))},
		{Config: initial, PlanOnly: true},
		{Config: config("", false), PlanOnly: true, ExpectError: regexp.MustCompile("Destructive table change is disabled")},
		{Config: removed, ConfigPlanChecks: accresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionDestroyBeforeCreate)}}, Check: accresource.TestCheckResourceAttr("paimon_table.events", "primary_keys.#", "0")},
		{Config: removed, PlanOnly: true},
	}})
	require.Equal(t, 2, catalog.tableCreates)
	require.Zero(t, catalog.tableAlters, "primary-key must never be changed through ALTER options")
}

func TestAccPolicyJavaCanonicalization(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	canonicalPredicate := `{"kind":"LEAF","transform":{"name":"FIELD_REF","fieldRef":{"index":1,"name":"tenant","type":"STRING"}},"function":"EQUAL","literals":["APAC"]}`
	canonicalTransform := `{"name":"UPPER","inputs":[{"index":1,"name":"tenant","type":"STRING"}]}`
	catalog := &managementAcceptanceCatalog{policies: make(map[string]client.DataPolicy), canonicalize: func(p client.PolicyRequest) client.PolicyRequest {
		if p.RowFilter != nil {
			p.RowFilter.Predicate = canonicalPredicate
		}
		if p.ColumnMask != nil {
			p.ColumnMask.Transform = canonicalTransform
		}

		return p
	}}
	server := httptest.NewServer(catalog)
	defer server.Close()
	config := func(predicate, transform string) string {
		return fmt.Sprintf(`
provider "paimon" { uri = %q }
%s
resource "paimon_row_filter" "region" {
 database=paimon_table.events.database
 table=paimon_table.events.name
 principal="role:analyst"
 predicate=%q
}
resource "paimon_column_mask" "secret" {
 database=paimon_table.events.database
 table=paimon_table.events.name
 principal="role:analyst"
 column="secret"
 transform=%q
}
`, server.URL, managementTableConfig("analytics"), predicate, transform)
	}
	oldReference := strings.ReplaceAll(strings.ReplaceAll(canonicalPredicate, `"index":1`, `"index":99`), `"type":"STRING"`, `"type":"INT"`)
	initial := config(`{"kind":"COMPOUND","function":"AND","children":[`+oldReference+`]}`, strings.ReplaceAll(canonicalTransform, `"index":1`, `"index":99`))
	canonical := config(canonicalPredicate, canonicalTransform)
	accresource.Test(t, accresource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []accresource.TestStep{
		{Config: initial},
		{Config: initial, PlanOnly: true},
		{Config: canonical},
		{Config: canonical, PlanOnly: true},
	}})
	require.Equal(t, 4, catalog.policyMutations, "two creates plus final deletes; equivalent updates must never detach policies")
}

func TestAccTableWithDeferredSchema(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	catalog := &acceptanceCatalog{database: &client.Database{Name: "analytics", Options: map[string]string{}}}
	server := httptest.NewServer(catalog)
	defer server.Close()
	config := fmt.Sprintf(`
provider "paimon" { uri = %q }
resource "terraform_data" "schema" { input = [{name="id",type="BIGINT"}] }
resource "terraform_data" "keys" { input = "id" }
resource "paimon_table" "events" {
 database="analytics"
 name="events"
 fields=terraform_data.schema.output
 options={ "primary-key"=terraform_data.keys.output }
}
`, server.URL)
	accresource.Test(t, accresource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []accresource.TestStep{
		{Config: config, Check: accresource.ComposeTestCheckFunc(accresource.TestCheckResourceAttr("paimon_table.events", "primary_keys.0", "id"), accresource.TestCheckResourceAttr("paimon_table.events", "fields.0.nullable", "false"))},
		{Config: config, PlanOnly: true},
	}})
}

func TestFieldDefaultConstantsAndRemoval(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct{ kind, value string }{{"BIGINT", "42"}, {"BOOLEAN", "true"}, {"DATE", "2026-01-02"}, {"STRING", ""}, {"STRING", "NULL"}, {"STRING", "1 + 2"}, {"STRING", "CURRENT_TIMESTAMP"}} {
		t.Run(test.kind+"/"+test.value, func(t *testing.T) {
			var diags diag.Diagnostics
			field := tableFieldForTest("value", types.Int64Value(0))
			field.Type = types.StringValue(test.kind)
			field.DefaultValue = types.StringValue(test.value)
			model := tableResourceModel{Fields: fieldsValueFromModels(ctx, []tableFieldModel{field}, &diags), Options: types.MapNull(types.StringType), PartitionKeys: types.ListNull(types.StringType), PrimaryKeys: types.ListNull(types.StringType)}
			schema := schemaFromResourceModel(ctx, &model, &diags)
			require.False(t, diags.HasError(), diags)
			encoded, err := json.Marshal(schema.Fields[0])
			require.NoError(t, err)
			var wire map[string]any
			require.NoError(t, json.Unmarshal(encoded, &wire))
			require.Equal(t, test.value, wire["defaultValue"], "constants must be transmitted unchanged, including empty strings")
			after := schema.Fields[0]
			after.DefaultValue = nil
			changes, err := tableFieldSchemaChanges(schema.Fields, []client.Field{after}, false, nil)
			require.NoError(t, err)
			encoded, err = json.Marshal(changes)
			require.NoError(t, err)
			require.JSONEq(t, `[{"action":"updateColumnDefaultValue","fieldNames":["value"],"newDefaultValue":null}]`, string(encoded))
		})
	}
}

func TestPolicyReplacementDoesNotReadUncreatedDestination(t *testing.T) {
	ctx := context.Background()
	for _, mask := range []bool{false, true} {
		t.Run(strconv.FormatBool(mask), func(t *testing.T) {
			var res resource.Resource
			var before, after any
			if mask {
				res = &columnMaskResource{}
				old := columnMaskResourceModel{Database: types.StringValue("db"), Table: types.StringValue("old"), Column: types.StringValue("secret"), Principal: types.StringValue("role:reader"), Transform: types.StringValue(`{"name":"FIELD_REF","fieldRef":{"index":0,"name":"tenant","type":"STRING"}}`)}
				next := old
				next.Table = types.StringValue("not-yet-created")
				next.Transform = types.StringValue(`{"name":"FIELD_REF","fieldRef":{"index":0,"name":"other","type":"STRING"}}`)
				before, after = &old, &next
			} else {
				res = &rowFilterResource{}
				old := rowFilterResourceModel{Database: types.StringValue("db"), Table: types.StringValue("old"), Principal: types.StringValue("role:reader"), Predicate: types.StringValue(`{"kind":"LEAF","transform":{"name":"FIELD_REF","fieldRef":{"index":0,"name":"tenant","type":"STRING"}},"function":"EQUAL","literals":["APAC"]}`)}
				next := old
				next.Table = types.StringValue("not-yet-created")
				next.Predicate = types.StringValue(strings.ReplaceAll(old.Predicate.ValueString(), "tenant", "other"))
				before, after = &old, &next
			}
			var sr resource.SchemaResponse
			res.Schema(ctx, resource.SchemaRequest{}, &sr)
			state := tfsdk.State{Schema: sr.Schema}
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, state.Set(ctx, before).HasError())
			require.False(t, plan.Set(ctx, after).HasError())
			response := resource.ModifyPlanResponse{Plan: plan}
			res.(resource.ResourceWithModifyPlan).ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, &response)
			require.False(t, response.Diagnostics.HasError(), response.Diagnostics)
		})
	}
}
