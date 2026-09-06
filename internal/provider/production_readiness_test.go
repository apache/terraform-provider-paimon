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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"
)

func TestDatabaseFailedOptionRemovalRetainsManagement(t *testing.T) {
	ctx := context.Background()
	remote := &client.Database{Name: "analytics", Options: map[string]string{"owner": "old"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeProviderJSON(t, w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}
		writeProviderJSON(t, w, remote)
	}))
	defer server.Close()
	api, err := client.New(client.Config{URI: server.URL, RecoveryTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	res := &databaseResource{client: api}
	var sr resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &sr)
	var ds diag.Diagnostics
	model := databaseResourceModel{Options: types.MapValueMust(types.StringType, map[string]attr.Value{"owner": types.StringValue("old")})}
	setDatabaseResourceModel(ctx, &model, remote, &ds)
	require.False(t, ds.HasError(), ds)
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	model.Options = types.MapValueMust(types.StringType, map[string]attr.Value{})
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.UpdateResponse{State: state}
	res.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &resp)
	require.True(t, resp.Diagnostics.HasError(), resp.Diagnostics)
	require.Empty(t, resp.Diagnostics.Warnings())
	var after databaseResourceModel
	require.False(t, resp.State.Get(ctx, &after).HasError())
	require.Len(t, after.Options.Elements(), 1)
	require.Equal(t, "old", remote.Options["owner"])
}

func TestTableFailedOptionRemovalRetainsManagement(t *testing.T) {
	ctx := context.Background()
	remote := &client.Table{Database: "analytics", Name: "events", Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT"}}, Options: map[string]string{"retention": "old"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeProviderJSON(t, w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}
		writeProviderJSON(t, w, remote)
	}))
	defer server.Close()
	api, err := client.New(client.Config{URI: server.URL, RecoveryTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	res := &tableResource{client: api}
	var sr resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &sr)
	var ds diag.Diagnostics
	model := tableResourceModel{Fields: types.ListNull(types.ObjectType{AttrTypes: tableFieldAttrTypes()}), Options: types.MapValueMust(types.StringType, map[string]attr.Value{"retention": types.StringValue("old")})}
	setTableResourceModel(ctx, &model, remote, &ds)
	require.False(t, ds.HasError(), ds)
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	model.Options = types.MapValueMust(types.StringType, map[string]attr.Value{})
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.UpdateResponse{State: state}
	res.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &resp)
	require.True(t, resp.Diagnostics.HasError(), resp.Diagnostics)
	require.Empty(t, resp.Diagnostics.Warnings())
	var after tableResourceModel
	require.False(t, resp.State.Get(ctx, &after).HasError())
	require.Len(t, after.Options.Elements(), 1)
	require.Equal(t, "old", remote.Schema.Options["retention"])
}

func TestNotNullTypeRoundTripPreservesConfiguration(t *testing.T) {
	ctx := context.Background()
	var ds diag.Diagnostics
	field := tableFieldForTest("id", types.Int64Value(0))
	field.Type = types.StringValue("BIGINT NOT NULL")
	field.Nullable = types.BoolValue(false)
	managed := fieldsValueFromModels(ctx, []tableFieldModel{field}, &ds)
	remote := resourceFieldsValueFromRemote(ctx, managed, []client.Field{{ID: 0, Name: "id", Type: "BIGINT NOT NULL"}}, &ds)
	var got []tableFieldModel
	require.False(t, remote.ElementsAs(ctx, &got, false).HasError())
	require.False(t, ds.HasError(), ds)
	require.Equal(t, "BIGINT NOT NULL", got[0].Type.ValueString())
}

func TestImportedImmutableOptionCanBecomeManaged(t *testing.T) {
	ctx := context.Background()
	remote := &client.Table{Database: "analytics", Name: "events", Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT"}}, Options: map[string]string{"merge-engine": "partial-update"}}}
	var ds diag.Diagnostics
	imported := tableResourceModel{Fields: types.ListNull(types.ObjectType{AttrTypes: tableFieldAttrTypes()}), Options: types.MapNull(types.StringType)}
	setTableResourceModel(ctx, &imported, remote, &ds)
	require.False(t, ds.HasError(), ds)
	require.True(t, imported.Options.IsNull())
	plannedOptions := types.MapValueMust(types.StringType, map[string]attr.Value{"merge-engine": types.StringValue("partial-update")})
	require.Equal(t, plannedOptions, imported.ServerOptions)
	baseline := effectiveManagedTableOptions(imported.Options, plannedOptions, imported.ServerOptions)
	require.False(t, immutableTableOptionsChanged(baseline, plannedOptions))
}

func TestPolicyCreateRejectsExistingPolicy(t *testing.T) {
	for _, mask := range []bool{false, true} {
		for _, status := range []int{http.StatusConflict, http.StatusForbidden} {
			t.Run(fmt.Sprintf("mask=%t/status=%d", mask, status), func(t *testing.T) {
				ctx := context.Background()
				reads, mutations := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/config" {
						writeProviderJSON(t, w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

						return
					}
					if r.Method == http.MethodPost {
						mutations++
						w.WriteHeader(status)

						return
					}
					reads++
					writeProviderJSON(t, w, client.ListPoliciesResponse{Policies: []client.DataPolicy{{
						Resource:  client.PermissionResource{Type: client.ResourceTypeTable, Database: "db", Table: "tbl"},
						Principal: "role:reader", RowFilter: &client.RowFilter{Predicate: `{"kind":"LEAF"}`},
						ColumnMask: &client.ColumnMask{OnColumn: "secret", Transform: `{"name":"CONCAT"}`},
					}}})
				}))
				defer server.Close()
				api, err := client.New(client.Config{URI: server.URL, RecoveryTimeout: 100 * time.Millisecond})
				require.NoError(t, err)
				var res resource.Resource
				var model any
				if mask {
					res = &columnMaskResource{client: api}
					model = &columnMaskResourceModel{Database: types.StringValue("db"), Table: types.StringValue("tbl"), Principal: types.StringValue("role:reader"), Column: types.StringValue("secret"), Transform: types.StringValue(`{"name":"CONCAT"}`)}
				} else {
					res = &rowFilterResource{client: api}
					model = &rowFilterResourceModel{Database: types.StringValue("db"), Table: types.StringValue("tbl"), Principal: types.StringValue("role:reader"), Predicate: types.StringValue(`{"kind":"LEAF"}`)}
				}
				var sr resource.SchemaResponse
				res.Schema(ctx, resource.SchemaRequest{}, &sr)
				plan := tfsdk.Plan{Schema: sr.Schema}
				require.False(t, plan.Set(ctx, model).HasError())
				resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema}}
				res.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
				require.True(t, resp.Diagnostics.HasError())
				require.True(t, resp.State.Raw.IsNull(), "a rejected create must not establish ownership")
				require.Zero(t, reads, "definitive rejections must not enter adoption reconciliation")
				require.Equal(t, 1, mutations)
			})
		}
	}
}

func TestPolicyUpdateRequiresMaintenanceOptIn(t *testing.T) {
	ctx := context.Background()
	for _, mask := range []bool{false, true} {
		t.Run(strconv.FormatBool(mask), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			api, err := client.New(client.Config{URI: server.URL, RecoveryTimeout: 100 * time.Millisecond})
			require.NoError(t, err)
			var res resource.Resource
			var before, after any
			if mask {
				res = &columnMaskResource{client: api}
				m := columnMaskResourceModel{ID: types.StringValue("existing"), Database: types.StringValue("db"), Table: types.StringValue("tbl"), Principal: types.StringValue("role:reader"), Column: types.StringValue("secret"), Transform: types.StringValue(`{"name":"old"}`)}
				n := m
				n.Transform = types.StringValue(`{"name":"new"}`)
				before, after = &m, &n
			} else {
				res = &rowFilterResource{client: api}
				m := rowFilterResourceModel{ID: types.StringValue("existing"), Database: types.StringValue("db"), Table: types.StringValue("tbl"), Principal: types.StringValue("role:reader"), Predicate: types.StringValue(`{"kind":"old"}`)}
				n := m
				n.Predicate = types.StringValue(`{"kind":"new"}`)
				before, after = &m, &n
			}
			var sr resource.SchemaResponse
			res.Schema(ctx, resource.SchemaRequest{}, &sr)
			state := tfsdk.State{Schema: sr.Schema}
			require.False(t, state.Set(ctx, before).HasError())
			plan := tfsdk.Plan{Schema: sr.Schema}
			require.False(t, plan.Set(ctx, after).HasError())
			planned := resource.ModifyPlanResponse{Plan: plan}
			res.(resource.ResourceWithModifyPlan).ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, &planned)
			require.True(t, planned.Diagnostics.HasError())
			response := resource.UpdateResponse{State: state}
			res.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &response)
			require.True(t, response.Diagnostics.HasError())
			require.True(t, response.State.Raw.Equal(state.Raw))
			require.Zero(t, requests, "blocked policy update must not remove protection")
		})
	}
}

func TestNotNullTypeDriftRemainsReadable(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	field := tableFieldForTest("id", types.Int64Value(0))
	field.Type = types.StringValue("BIGINT NOT NULL")
	field.Nullable = types.BoolValue(false)
	managed := fieldsValueFromModels(ctx, []tableFieldModel{field}, &diags)
	observed := resourceFieldsValueFromRemote(ctx, managed, []client.Field{{ID: 0, Name: "id", Type: "BIGINT"}}, &diags)
	var fields []tableFieldModel
	require.False(t, observed.ElementsAs(ctx, &fields, false).HasError())
	require.Equal(t, "BIGINT", fields[0].Type.ValueString())
	require.True(t, fields[0].Nullable.ValueBool())
	schemaFromResourceModel(ctx, &tableResourceModel{Fields: observed, Options: types.MapNull(types.StringType)}, &diags)
	require.False(t, diags.HasError(), diags)
}

func TestTableUpdateRejectsResolvedNewFieldIDBeforeMutation(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	remote := &client.Table{Database: "analytics", Name: "events", Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT"}}}}
	model := tableResourceModel{Fields: types.ListNull(types.ObjectType{AttrTypes: tableFieldAttrTypes()}), Options: types.MapNull(types.StringType)}
	setTableResourceModel(ctx, &model, remote, &diags)
	require.False(t, diags.HasError(), diags)
	res := &tableResource{} // No client: validation must finish before any REST call.
	var sr resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &sr)
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	fields := fieldModelsFromRemote(remote.Schema.Fields)
	fields = append(fields, tableFieldForTest("new_value", types.Int64Value(100)))
	model.Fields = fieldsValueFromModels(ctx, fields, &diags)
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	response := resource.UpdateResponse{State: state}
	res.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError())
	require.Contains(t, response.Diagnostics.Errors()[0].Summary(), "New field IDs")
	require.True(t, response.State.Raw.Equal(state.Raw))
}

func TestOptionReconciliationRequiresPresenceAndRemoval(t *testing.T) {
	require.False(t, optionsConverged(map[string]string{}, map[string]string{"owner": ""}, nil))
	require.True(t, optionsConverged(map[string]string{"owner": ""}, map[string]string{"owner": ""}, nil))
	require.False(t, optionsConverged(map[string]string{"owner": "old"}, nil, []string{"owner"}))
	require.True(t, optionsConverged(map[string]string{"server-only": "kept"}, nil, []string{"owner"}))
}

func TestTableReplacementRequiresOptIn(t *testing.T) {
	ctx := context.Background()
	for _, changed := range []string{"name", "database", "primary_keys", "partition_keys"} {
		for _, allowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allowed=%t", changed, allowed), func(t *testing.T) {
				res := &tableResource{}
				var sr resource.SchemaResponse
				res.Schema(ctx, resource.SchemaRequest{}, &sr)
				var diags diag.Diagnostics
				before := tableResourceModel{Fields: types.ListNull(types.ObjectType{AttrTypes: tableFieldAttrTypes()}), Options: types.MapNull(types.StringType)}
				remote := &client.Table{Database: "analytics", Name: "events", Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT NOT NULL"}}}}
				setTableResourceModel(ctx, &before, remote, &diags)
				require.False(t, diags.HasError(), diags)
				state := tfsdk.State{Schema: sr.Schema}
				require.False(t, state.Set(ctx, &before).HasError())
				after := before
				after.AllowReplacement = types.BoolValue(allowed)
				switch changed {
				case "name":
					after.Name = types.StringValue("renamed")
				case "database":
					after.Database = types.StringValue("other")
				case "primary_keys":
					after.Options = types.MapValueMust(types.StringType, map[string]attr.Value{"primary-key": types.StringValue("id")})
				case "partition_keys":
					after.PartitionKeys = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("id")})
				}
				plan := tfsdk.Plan{Schema: sr.Schema}
				require.False(t, plan.Set(ctx, &after).HasError())
				response := resource.ModifyPlanResponse{Plan: plan}
				res.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan, Config: tfsdk.Config{Schema: sr.Schema, Raw: plan.Raw}}, &response)
				if allowed {
					require.False(t, response.Diagnostics.HasError(), response.Diagnostics)
					require.Len(t, response.RequiresReplace, 1)
				} else {
					require.True(t, response.Diagnostics.HasError())
					require.Contains(t, response.Diagnostics.Errors()[0].Summary(), "Destructive table change")
				}
			})
		}
	}
}

func TestDatabaseSuccessfulButStaleRemovalRetainsManagement(t *testing.T) {
	ctx := context.Background()
	var visible atomic.Bool
	remote := &client.Database{Name: "analytics", Options: map[string]string{"owner": "old"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeProviderJSON(t, w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)

			return
		}
		if visible.Load() {
			writeProviderJSON(t, w, &client.Database{Name: remote.Name, Options: map[string]string{}})

			return
		}
		writeProviderJSON(t, w, remote)
	}))
	defer server.Close()
	api, err := client.New(client.Config{URI: server.URL, RecoveryTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	res := &databaseResource{client: api}
	var sr resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &sr)
	var ds diag.Diagnostics
	model := databaseResourceModel{Options: types.MapValueMust(types.StringType, map[string]attr.Value{"owner": types.StringValue("old")})}
	setDatabaseResourceModel(ctx, &model, remote, &ds)
	require.False(t, ds.HasError(), ds)
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	model.Options = types.MapValueMust(types.StringType, map[string]attr.Value{})
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.UpdateResponse{State: state}
	res.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &resp)
	require.True(t, resp.Diagnostics.HasError(), resp.Diagnostics)
	require.Empty(t, resp.Diagnostics.Warnings())
	var after databaseResourceModel
	require.False(t, resp.State.Get(ctx, &after).HasError())
	require.Len(t, after.Options.Elements(), 1)
	require.Equal(t, "old", remote.Options["owner"])

	visible.Store(true)
	read := resource.ReadResponse{State: resp.State}
	res.Read(ctx, resource.ReadRequest{State: resp.State}, &read)
	require.False(t, read.Diagnostics.HasError(), read.Diagnostics)
	require.False(t, read.State.Get(ctx, &after).HasError())
	require.Empty(t, after.Options.Elements(), "a subsequent refresh must observe the completed removal")
}

func TestTableSuccessfulButStaleRemovalRetainsManagement(t *testing.T) {
	ctx := context.Background()
	var visible atomic.Bool
	remote := &client.Table{Database: "analytics", Name: "events", Schema: client.Schema{Fields: []client.Field{{ID: 0, Name: "id", Type: "BIGINT"}}, Options: map[string]string{"retention": "old"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeProviderJSON(t, w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)

			return
		}
		if visible.Load() {
			writeProviderJSON(t, w, &client.Table{Database: remote.Database, Name: remote.Name, Schema: client.Schema{Fields: remote.Schema.Fields, Options: map[string]string{}}})

			return
		}
		writeProviderJSON(t, w, remote)
	}))
	defer server.Close()
	api, err := client.New(client.Config{URI: server.URL, RecoveryTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	res := &tableResource{client: api}
	var sr resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &sr)
	var ds diag.Diagnostics
	model := tableResourceModel{Fields: types.ListNull(types.ObjectType{AttrTypes: tableFieldAttrTypes()}), Options: types.MapValueMust(types.StringType, map[string]attr.Value{"retention": types.StringValue("old")})}
	setTableResourceModel(ctx, &model, remote, &ds)
	require.False(t, ds.HasError(), ds)
	state := tfsdk.State{Schema: sr.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	model.Options = types.MapValueMust(types.StringType, map[string]attr.Value{})
	plan := tfsdk.Plan{Schema: sr.Schema}
	require.False(t, plan.Set(ctx, &model).HasError())
	resp := resource.UpdateResponse{State: state}
	res.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &resp)
	require.True(t, resp.Diagnostics.HasError(), resp.Diagnostics)
	require.Empty(t, resp.Diagnostics.Warnings())
	var after tableResourceModel
	require.False(t, resp.State.Get(ctx, &after).HasError())
	require.Len(t, after.Options.Elements(), 1)
	require.Equal(t, "old", remote.Schema.Options["retention"])

	visible.Store(true)
	read := resource.ReadResponse{State: resp.State}
	res.Read(ctx, resource.ReadRequest{State: resp.State}, &read)
	require.False(t, read.Diagnostics.HasError(), read.Diagnostics)
	require.False(t, read.State.Get(ctx, &after).HasError())
	require.Empty(t, after.Options.Elements(), "a subsequent refresh must observe the completed removal")
}
