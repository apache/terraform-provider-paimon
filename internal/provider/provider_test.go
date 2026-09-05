// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership. The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	providerschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider(t *testing.T) {
	assert.NotNil(t, New("test")())
}

func TestDiffOptions(t *testing.T) {
	removals, updates := diffOptions(
		map[string]string{"remove": "old", "change": "old", "same": "value"},
		map[string]string{"change": "new", "same": "value", "add": "value"},
	)
	assert.ElementsMatch(t, []string{"remove"}, removals)
	assert.Equal(t, map[string]string{"change": "new", "add": "value"}, updates)
}

func TestSchemasHaveValidFrameworkImplementations(t *testing.T) {
	ctx := context.Background()
	p := &paimonProvider{version: "test"}

	var providerResponse frameworkprovider.SchemaResponse
	p.Schema(ctx, frameworkprovider.SchemaRequest{}, &providerResponse)
	require.False(t, providerResponse.Diagnostics.HasError())
	require.False(t, providerResponse.Schema.ValidateImplementation(ctx).HasError())

	for _, factory := range p.Resources(ctx) {
		var response resource.SchemaResponse
		factory().Schema(ctx, resource.SchemaRequest{}, &response)
		require.False(t, response.Diagnostics.HasError())
		diagnostics := response.Schema.ValidateImplementation(ctx)
		require.False(t, diagnostics.HasError(), diagnostics.Errors())
	}

	for _, factory := range p.DataSources(ctx) {
		var response datasource.SchemaResponse
		factory().Schema(ctx, datasource.SchemaRequest{}, &response)
		require.False(t, response.Diagnostics.HasError())
		diagnostics := response.Schema.ValidateImplementation(ctx)
		require.False(t, diagnostics.HasError(), diagnostics.Errors())
	}
}

func TestProviderDLFCredentialAttributesAreSensitive(t *testing.T) {
	ctx := context.Background()
	p := &paimonProvider{version: "test"}
	var response frameworkprovider.SchemaResponse
	p.Schema(ctx, frameworkprovider.SchemaRequest{}, &response)
	require.False(t, response.Diagnostics.HasError())

	for _, name := range []string{
		"dlf_access_key_id",
		"dlf_access_key_secret",
		"dlf_security_token",
	} {
		attribute, ok := response.Schema.Attributes[name].(providerschema.StringAttribute)
		require.True(t, ok, "attribute %s should be a string", name)
		assert.True(t, attribute.Sensitive, "attribute %s should be sensitive", name)
	}
}

func TestHasDLFConfiguration(t *testing.T) {
	assert.False(t, hasDLFConfiguration(paimonProviderModel{}))
	assert.True(t, hasDLFConfiguration(paimonProviderModel{
		DLFTokenLoader: types.StringValue(client.DLFTokenLoaderECS),
	}))
}

func TestSchemaFromResourceModelNormalizesPrimaryKeyNullability(t *testing.T) {
	ctx := context.Background()
	fields, diagnostics := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: tableFieldAttrTypes()}, []tableFieldModel{
		{
			ID:             types.Int64Unknown(),
			Name:           types.StringValue("id"),
			Type:           types.StringValue("BIGINT"),
			Nullable:       types.BoolUnknown(),
			Description:    types.StringNull(),
			DefaultValue:   types.StringNull(),
			NestedFieldIDs: types.MapUnknown(types.Int64Type),
		},
	})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	primaryKeys, diagnostics := types.ListValueFrom(ctx, types.StringType, []string{"id"})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())

	model := tableResourceModel{
		Fields:        fields,
		PartitionKeys: types.ListUnknown(types.StringType),
		PrimaryKeys:   primaryKeys,
		Options:       types.MapNull(types.StringType),
		Comment:       types.StringNull(),
	}
	tableSchema := schemaFromResourceModel(ctx, &model, &diagnostics)
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	require.Len(t, tableSchema.Fields, 1)
	assert.Equal(t, client.DataType("BIGINT NOT NULL"), tableSchema.Fields[0].Type)
}

func TestSchemaFromResourceModelAllocatesUnusedFieldIDs(t *testing.T) {
	ctx := context.Background()
	fields, diagnostics := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: tableFieldAttrTypes()}, []tableFieldModel{
		tableFieldForTest("first", types.Int64Value(1)),
		tableFieldForTest("second", types.Int64Unknown()),
		tableFieldForTest("third", types.Int64Value(3)),
		tableFieldForTest("fourth", types.Int64Null()),
		tableFieldForTest("max", types.Int64Value(maxPaimonFieldID)),
	})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	model := tableResourceModel{
		Fields:        fields,
		PartitionKeys: types.ListNull(types.StringType),
		PrimaryKeys:   types.ListNull(types.StringType),
		Options:       types.MapNull(types.StringType),
		Comment:       types.StringNull(),
	}

	tableSchema := schemaFromResourceModel(ctx, &model, &diagnostics)
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	require.Len(t, tableSchema.Fields, 5)
	assert.Equal(t, []int{1, 0, 3, 2, maxPaimonFieldID}, []int{
		tableSchema.Fields[0].ID,
		tableSchema.Fields[1].ID,
		tableSchema.Fields[2].ID,
		tableSchema.Fields[3].ID,
		tableSchema.Fields[4].ID,
	})
}

func TestSchemaFromResourceModelRejectsInvalidFieldIDs(t *testing.T) {
	ctx := context.Background()
	fields, diagnostics := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: tableFieldAttrTypes()}, []tableFieldModel{
		tableFieldForTest("first", types.Int64Value(2)),
		tableFieldForTest("duplicate", types.Int64Value(2)),
		tableFieldForTest("negative", types.Int64Value(-1)),
		tableFieldForTest("reserved", types.Int64Value(maxPaimonFieldID+1)),
	})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	model := tableResourceModel{
		Fields:        fields,
		PartitionKeys: types.ListNull(types.StringType),
		PrimaryKeys:   types.ListNull(types.StringType),
		Options:       types.MapNull(types.StringType),
		Comment:       types.StringNull(),
	}

	_ = schemaFromResourceModel(ctx, &model, &diagnostics)
	require.True(t, diagnostics.HasError())
	require.Len(t, diagnostics.Errors(), 3)
	assert.Contains(t, diagnostics.Errors()[0].Summary(), "Duplicate Paimon field ID")
	assert.Contains(t, diagnostics.Errors()[1].Summary(), "Invalid Paimon field ID")
	assert.Contains(t, diagnostics.Errors()[2].Summary(), "Invalid Paimon field ID")
}

func TestReservedTableOptionsValidator(t *testing.T) {
	ctx := context.Background()
	options, diagnostics := types.MapValueFrom(ctx, types.StringType, map[string]string{
		"bucket":      "4",
		"partition":   "dt",
		"primary-key": "id",
	})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())

	var response validator.MapResponse
	reservedTableOptionsValidator{}.ValidateMap(ctx, validator.MapRequest{
		Path:        path.Root("options"),
		ConfigValue: options,
	}, &response)
	require.True(t, response.Diagnostics.HasError())
	assert.Contains(t, response.Diagnostics.Errors()[0].Detail(), "Do not configure partition in options")
}

func TestImmutableTableOptionsChanged(t *testing.T) {
	mapValue := func(values map[string]attr.Value) types.Map {
		return types.MapValueMust(types.StringType, values)
	}

	assert.False(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{"bucket": types.StringValue("2")}),
		mapValue(map[string]attr.Value{"bucket": types.StringValue("4")}),
	))
	assert.True(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{"merge-engine": types.StringValue("deduplicate")}),
		mapValue(map[string]attr.Value{"merge-engine": types.StringValue("partial-update")}),
	))
	assert.True(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{"primary-key.nullable": types.StringValue("true")}),
		mapValue(map[string]attr.Value{}),
	))
	assert.True(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{"video-frame-field": types.StringValue("frames")}),
		mapValue(map[string]attr.Value{"video-frame-field": types.StringValue("new_frames")}),
	))
	assert.False(t, immutableTableOptionsChanged(
		types.MapNull(types.StringType),
		mapValue(map[string]attr.Value{"type": types.StringValue("table")}),
	))
	assert.False(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{"type": types.StringValue("table")}),
		mapValue(map[string]attr.Value{"type": types.StringValue("TABLE")}),
	))
	assert.False(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{"type": types.StringValue("table")}),
		mapValue(map[string]attr.Value{}),
	))
	assert.False(t, immutableTableOptionsChanged(
		types.MapUnknown(types.StringType),
		mapValue(map[string]attr.Value{"merge-engine": types.StringValue("partial-update")}),
	))
	assert.False(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{"merge-engine": types.StringValue("deduplicate")}),
		mapValue(map[string]attr.Value{"merge-engine": types.StringUnknown()}),
	))
	assert.True(t, immutableTableOptionsChanged(
		mapValue(map[string]attr.Value{
			"bucket-key":   types.StringValue("id"),
			"merge-engine": types.StringValue("deduplicate"),
		}),
		mapValue(map[string]attr.Value{
			"bucket-key":   types.StringUnknown(),
			"merge-engine": types.StringValue("partial-update"),
		}),
	))
}

func TestTableTypeSemanticNoOpPreservesConfiguredValue(t *testing.T) {
	removals, updates := diffTableOptions(
		map[string]string{},
		map[string]string{"type": "table"},
	)
	assert.Empty(t, removals)
	assert.Empty(t, updates)

	removals, updates = diffTableOptions(
		map[string]string{"type": "table"},
		map[string]string{"type": "TABLE"},
	)
	assert.Empty(t, removals)
	assert.Empty(t, updates)

	removals, updates = diffTableOptions(
		map[string]string{"type": "table"},
		map[string]string{},
	)
	assert.Empty(t, removals)
	assert.Empty(t, updates)

	ctx := context.Background()
	managed := types.MapValueMust(types.StringType, map[string]attr.Value{
		"type": types.StringValue("TABLE"),
	})
	var diagnostics diag.Diagnostics
	synced := syncManagedTableOptions(ctx, managed, map[string]string{}, &diagnostics)
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	assert.Equal(t, map[string]string{"type": "TABLE"}, mapFromValue(ctx, synced, &diagnostics))
	require.False(t, diagnostics.HasError(), diagnostics.Errors())

	synced = syncManagedTableOptions(ctx, managed, map[string]string{"type": "table"}, &diagnostics)
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	assert.Equal(t, map[string]string{"type": "TABLE"}, mapFromValue(ctx, synced, &diagnostics))
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
}

func TestTableResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	remote := client.Table{
		ID:       "table-id",
		Database: "analytics",
		Name:     "events",
		SchemaID: 1,
		Schema: client.Schema{
			Fields: []client.Field{
				{ID: 0, Name: "id", Type: client.DataType("BIGINT NOT NULL")},
				{ID: 1, Name: "labels", Type: client.DataType("MAP<INT, STRING>")},
				{ID: 2, Name: "payload", Type: client.DataType("ROW<`item` STRING>")},
			},
			PartitionKeys: []string{},
			PrimaryKeys:   []string{"id"},
			Options:       map[string]string{"bucket": "2", "server-only": "preserved"},
		},
	}
	createCalls, readCalls, updateCalls, deleteCalls := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables":
			createCalls++
			var body struct {
				Identifier client.Identifier `json:"identifier"`
				Schema     client.Schema     `json:"schema"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "events", body.Identifier.Object)
			assert.Equal(t, map[string]string{"bucket": "2"}, body.Schema.Options)
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/databases/analytics/tables/events":
			readCalls++
			if readCalls == 1 {
				http.NotFound(w, request)

				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(remote))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events":
			updateCalls++
			var body struct {
				Changes []client.SchemaChange `json:"changes"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, []client.SchemaChange{{"action": "setOption", "key": "bucket", "value": "4"}}, body.Changes)
			remote.Schema.Options["bucket"] = "4"
			remote.SchemaID++
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/catalog/databases/analytics/tables/events":
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	table := &tableResource{client: api}
	var schemaResponse resource.SchemaResponse
	table.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	require.False(t, schemaResponse.Diagnostics.HasError(), schemaResponse.Diagnostics.Errors())

	fields, diagnostics := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: tableFieldAttrTypes()}, []tableFieldModel{
		{
			ID:             types.Int64Unknown(),
			Name:           types.StringValue("id"),
			Type:           types.StringValue("BIGINT"),
			Nullable:       types.BoolValue(false),
			Description:    types.StringNull(),
			DefaultValue:   types.StringNull(),
			NestedFieldIDs: types.MapUnknown(types.Int64Type),
		},
		{
			ID:             types.Int64Unknown(),
			Name:           types.StringValue("labels"),
			Type:           types.StringValue("MAP<INTEGER,STRING>"),
			Nullable:       types.BoolValue(true),
			Description:    types.StringNull(),
			DefaultValue:   types.StringNull(),
			NestedFieldIDs: types.MapUnknown(types.Int64Type),
		},
		{
			ID:             types.Int64Unknown(),
			Name:           types.StringValue("payload"),
			Type:           types.StringValue("ROW<`item` STRING>"),
			Nullable:       types.BoolValue(true),
			Description:    types.StringNull(),
			DefaultValue:   types.StringNull(),
			NestedFieldIDs: types.MapUnknown(types.Int64Type),
		},
	})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	primaryKeys, diagnostics := types.ListValueFrom(ctx, types.StringType, []string{"id"})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	options, diagnostics := types.MapValueFrom(ctx, types.StringType, map[string]string{"bucket": "2"})
	require.False(t, diagnostics.HasError(), diagnostics.Errors())
	planModel := tableResourceModel{
		ID:            types.StringUnknown(),
		ServerID:      types.StringUnknown(),
		Database:      types.StringValue("analytics"),
		Name:          types.StringValue("events"),
		Fields:        fields,
		PartitionKeys: types.ListValueMust(types.StringType, []attr.Value{}),
		PrimaryKeys:   primaryKeys,
		Options:       options,
		ServerOptions: types.MapUnknown(types.StringType),
		Comment:       types.StringNull(),
		SchemaID:      types.Int64Unknown(),
		Path:          types.StringUnknown(),
		IsExternal:    types.BoolUnknown(),
		Owner:         types.StringUnknown(),
		CreatedAt:     types.Int64Unknown(),
		CreatedBy:     types.StringUnknown(),
		UpdatedAt:     types.Int64Unknown(),
		UpdatedBy:     types.StringUnknown(),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	createResponse := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	table.Create(ctx, resource.CreateRequest{Plan: plan}, &createResponse)
	require.False(t, createResponse.Diagnostics.HasError(), createResponse.Diagnostics.Errors())
	var createdModel tableResourceModel
	require.False(t, createResponse.State.Get(ctx, &createdModel).HasError())
	var createdFields []tableFieldModel
	require.False(t, createdModel.Fields.ElementsAs(ctx, &createdFields, false).HasError())
	require.Len(t, createdFields, 3)
	assert.Equal(t, "MAP<INTEGER,STRING>", createdFields[1].Type.ValueString())
	assert.Equal(t, "ROW<`item` STRING>", createdFields[2].Type.ValueString())

	readResponse := resource.ReadResponse{State: createResponse.State}
	table.Read(ctx, resource.ReadRequest{State: createResponse.State}, &readResponse)
	require.False(t, readResponse.Diagnostics.HasError(), readResponse.Diagnostics.Errors())

	var updateModel tableResourceModel
	require.False(t, readResponse.State.Get(ctx, &updateModel).HasError())
	var refreshedFields []tableFieldModel
	require.False(t, updateModel.Fields.ElementsAs(ctx, &refreshedFields, false).HasError())
	require.Len(t, refreshedFields, 3)
	assert.Equal(t, "MAP<INTEGER,STRING>", refreshedFields[1].Type.ValueString())
	assert.Equal(t, "ROW<`item` STRING>", refreshedFields[2].Type.ValueString())
	updateModel.Options = types.MapValueMust(types.StringType, map[string]attr.Value{"bucket": types.StringValue("4")})
	updatePlan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, updatePlan.Set(ctx, &updateModel).HasError())
	updateResponse := resource.UpdateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	table.Update(ctx, resource.UpdateRequest{State: readResponse.State, Plan: updatePlan}, &updateResponse)
	require.False(t, updateResponse.Diagnostics.HasError(), updateResponse.Diagnostics.Errors())

	deleteResponse := resource.DeleteResponse{State: updateResponse.State}
	table.Delete(ctx, resource.DeleteRequest{State: updateResponse.State}, &deleteResponse)
	require.False(t, deleteResponse.Diagnostics.HasError(), deleteResponse.Diagnostics.Errors())
	assert.Equal(t, 1, createCalls)
	assert.Equal(t, 4, readCalls)
	assert.Equal(t, 1, updateCalls)
	assert.Equal(t, 1, deleteCalls)
}

func TestDatabaseCreateRetainsIdentityWhenReadBackFails(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			writeProviderJSON(t, w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases":
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/databases/analytics":
			http.NotFound(w, request)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	database := &databaseResource{client: api}
	var schemaResponse resource.SchemaResponse
	database.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	require.False(t, schemaResponse.Diagnostics.HasError(), schemaResponse.Diagnostics.Errors())

	planModel := databaseResourceModel{
		ID:            types.StringUnknown(),
		ServerID:      types.StringUnknown(),
		Name:          types.StringValue("analytics"),
		Options:       types.MapNull(types.StringType),
		ServerOptions: types.MapUnknown(types.StringType),
		Location:      types.StringUnknown(),
		Owner:         types.StringUnknown(),
		CreatedAt:     types.Int64Unknown(),
		CreatedBy:     types.StringUnknown(),
		UpdatedAt:     types.Int64Unknown(),
		UpdatedBy:     types.StringUnknown(),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	createResponse := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	database.Create(ctx, resource.CreateRequest{Plan: plan}, &createResponse)
	require.True(t, createResponse.Diagnostics.HasError())
	assert.Contains(t, createResponse.Diagnostics.Errors()[0].Summary(), "verify Paimon database")

	var state databaseResourceModel
	require.False(t, createResponse.State.Get(ctx, &state).HasError())
	assert.Equal(t, "analytics", state.ID.ValueString())
	assert.Equal(t, "analytics", state.Name.ValueString())
}

func writeProviderJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value))
}

func tableFieldForTest(name string, id types.Int64) tableFieldModel {
	return tableFieldModel{
		ID:             id,
		Name:           types.StringValue(name),
		Type:           types.StringValue("STRING"),
		Nullable:       types.BoolValue(true),
		Description:    types.StringNull(),
		DefaultValue:   types.StringNull(),
		NestedFieldIDs: types.MapUnknown(types.Int64Type),
	}
}
