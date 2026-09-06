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
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resourceschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	schemavalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	var remote *client.PermissionAssignment
	grantCalls, listCalls, revokeCalls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/permissions/grant":
			grantCalls++
			var assignment client.PermissionAssignment
			require.NoError(t, json.NewDecoder(request.Body).Decode(&assignment))
			remote = &assignment
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/permissions":
			listCalls++
			assert.Equal(t, client.ResourceTypeColumn, request.URL.Query().Get("resourceType"))
			assert.Equal(t, client.PermissionAccessSelect, request.URL.Query().Get("access"))
			if listCalls == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "transient"}))

				return
			}
			if remote == nil {
				require.NoError(t, json.NewEncoder(w).Encode(client.ListPermissionsResponse{Permissions: []client.PermissionAssignment{}}))
			} else {
				listed := *remote
				if listed.ExpireTime != nil {
					canonical := "2026-09-01T00:00:00.123Z"
					listed.ExpireTime = &canonical
				}
				require.NoError(t, json.NewEncoder(w).Encode(client.ListPermissionsResponse{Permissions: []client.PermissionAssignment{listed}}))
			}
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/permissions/revoke":
			revokeCalls++
			remote = nil
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	managed := &permissionResource{client: api}
	var schemaResponse resource.SchemaResponse
	managed.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	require.False(t, schemaResponse.Diagnostics.HasError(), schemaResponse.Diagnostics.Errors())

	planModel := permissionResourceModel{
		ID:                  types.StringUnknown(),
		ResourceType:        types.StringValue(client.ResourceTypeColumn),
		Database:            types.StringValue("analytics"),
		Table:               types.StringValue("events"),
		Function:            types.StringNull(),
		View:                types.StringNull(),
		Access:              types.StringValue(client.PermissionAccessSelect),
		Principal:           types.StringValue("role:analyst"),
		ColumnNames:         types.SetValueMust(types.StringType, []attr.Value{types.StringValue("event_id"), types.StringValue("event_time")}),
		ExcludedColumnNames: types.SetNull(types.StringType),
		ExpireTime:          types.StringNull(),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	createResponse := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Create(ctx, resource.CreateRequest{Plan: plan}, &createResponse)
	require.False(t, createResponse.Diagnostics.HasError(), createResponse.Diagnostics.Errors())
	var created permissionResourceModel
	require.False(t, createResponse.State.Get(ctx, &created).HasError())
	assert.Contains(t, created.ID.ValueString(), "resource_type=COLUMN")
	assert.Equal(t, []string{"event_id", "event_time"}, remote.Columns.ColumnNames)

	created.ColumnNames = types.SetNull(types.StringType)
	created.ExcludedColumnNames = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("secret")})
	created.ExpireTime = types.StringValue("2026-09-01t00:00:00.123000z")
	updatePlan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, updatePlan.Set(ctx, &created).HasError())
	updateResponse := resource.UpdateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Update(ctx, resource.UpdateRequest{State: createResponse.State, Plan: updatePlan}, &updateResponse)
	require.False(t, updateResponse.Diagnostics.HasError(), updateResponse.Diagnostics.Errors())
	var updated permissionResourceModel
	require.False(t, updateResponse.State.Get(ctx, &updated).HasError())
	assert.Equal(t, "2026-09-01t00:00:00.123000z", updated.ExpireTime.ValueString())
	require.NotNil(t, remote.Columns)
	assert.Nil(t, remote.Columns.ColumnNames)
	assert.Equal(t, []string{"secret"}, remote.Columns.ExcludedColumnNames)
	require.NotNil(t, remote.ExpireTime)
	assert.Equal(t, "2026-09-01T00:00:00.123Z", *remote.ExpireTime)

	deleteResponse := resource.DeleteResponse{State: updateResponse.State}
	managed.Delete(ctx, resource.DeleteRequest{State: updateResponse.State}, &deleteResponse)
	require.False(t, deleteResponse.Diagnostics.HasError(), deleteResponse.Diagnostics.Errors())
	assert.Equal(t, 2, grantCalls)
	assert.Equal(t, 3, listCalls)
	assert.Equal(t, 1, revokeCalls)
}

func TestRowFilterResourceLifecyclePreservesEquivalentJSON(t *testing.T) {
	ctx := context.Background()
	var remote *client.DataPolicy
	createCalls, listCalls, dropCalls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			createCalls++
			var body client.PolicyRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			var value map[string]any
			require.NoError(t, json.Unmarshal([]byte(body.RowFilter.Predicate), &value))
			canonical, err := json.Marshal(value)
			require.NoError(t, err)
			remote = &client.DataPolicy{
				Resource:  client.PermissionResource{Type: client.ResourceTypeTable, Database: "analytics", Table: "events"},
				RowFilter: &client.RowFilter{Predicate: string(canonical)},
				Principal: body.Principal,
			}
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			listCalls++
			if remote == nil || listCalls == 1 {
				require.NoError(t, json.NewEncoder(w).Encode(client.ListPoliciesResponse{Policies: []client.DataPolicy{}}))
			} else {
				require.NoError(t, json.NewEncoder(w).Encode(client.ListPoliciesResponse{Policies: []client.DataPolicy{*remote}}))
			}
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies/drop":
			dropCalls++
			remote = nil
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	managed := &rowFilterResource{client: api}
	var schemaResponse resource.SchemaResponse
	managed.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	require.False(t, schemaResponse.Diagnostics.HasError(), schemaResponse.Diagnostics.Errors())
	configuredPredicate := `{ "op": "eq", "field": "tenant_id" }`
	planModel := rowFilterResourceModel{
		AllowNonAtomicUpdate: types.BoolValue(true),
		ID:                   types.StringUnknown(),
		Database:             types.StringValue("analytics"),
		Table:                types.StringValue("events"),
		Principal:            types.StringValue("role:analyst"),
		Predicate:            types.StringValue(configuredPredicate),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	createResponse := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Create(ctx, resource.CreateRequest{Plan: plan}, &createResponse)
	require.False(t, createResponse.Diagnostics.HasError(), createResponse.Diagnostics.Errors())
	var created rowFilterResourceModel
	require.False(t, createResponse.State.Get(ctx, &created).HasError())
	assert.Equal(t, configuredPredicate, created.Predicate.ValueString())

	equivalent := created
	equivalent.Predicate = types.StringValue(`{"field":"tenant_id","op":"eq"}`)
	equivalentPlan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, equivalentPlan.Set(ctx, &equivalent).HasError())
	equivalentResponse := resource.UpdateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Update(ctx, resource.UpdateRequest{State: createResponse.State, Plan: equivalentPlan}, &equivalentResponse)
	require.False(t, equivalentResponse.Diagnostics.HasError(), equivalentResponse.Diagnostics.Errors())
	assert.Equal(t, 1, createCalls)
	assert.Equal(t, 0, dropCalls)

	created.Predicate = types.StringValue(`{"field":"tenant_id","op":"not_eq"}`)
	updatePlan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, updatePlan.Set(ctx, &created).HasError())
	updateResponse := resource.UpdateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Update(ctx, resource.UpdateRequest{State: createResponse.State, Plan: updatePlan}, &updateResponse)
	require.False(t, updateResponse.Diagnostics.HasError(), updateResponse.Diagnostics.Errors())

	deleteResponse := resource.DeleteResponse{State: updateResponse.State}
	managed.Delete(ctx, resource.DeleteRequest{State: updateResponse.State}, &deleteResponse)
	require.False(t, deleteResponse.Diagnostics.HasError(), deleteResponse.Diagnostics.Errors())
	assert.Equal(t, 2, createCalls)
	assert.Equal(t, 3, listCalls)
	assert.Equal(t, 2, dropCalls)
}

func TestColumnMaskResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	var remote *client.DataPolicy
	createCalls, listCalls, dropCalls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			createCalls++
			var body client.PolicyRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			remote = &client.DataPolicy{
				Resource:   client.PermissionResource{Type: client.ResourceTypeTable, Database: "analytics", Table: "events"},
				ColumnMask: body.ColumnMask,
				Principal:  body.Principal,
			}
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			listCalls++
			assert.Equal(t, "email", request.URL.Query().Get("column"))
			if listCalls == 1 {
				require.NoError(t, json.NewEncoder(w).Encode(client.ListPoliciesResponse{Policies: []client.DataPolicy{}}))

				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(client.ListPoliciesResponse{Policies: []client.DataPolicy{*remote}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies/drop":
			dropCalls++
			remote = nil
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	managed := &columnMaskResource{client: api}
	var schemaResponse resource.SchemaResponse
	managed.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	planModel := columnMaskResourceModel{
		AllowNonAtomicUpdate: types.BoolValue(true),
		ID:                   types.StringUnknown(),
		Database:             types.StringValue("analytics"),
		Table:                types.StringValue("events"),
		Principal:            types.StringValue("role:analyst"),
		Column:               types.StringValue("email"),
		Transform:            types.StringValue(`{"type":"null"}`),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	createResponse := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Create(ctx, resource.CreateRequest{Plan: plan}, &createResponse)
	require.False(t, createResponse.Diagnostics.HasError(), createResponse.Diagnostics.Errors())
	var created columnMaskResourceModel
	require.False(t, createResponse.State.Get(ctx, &created).HasError())
	created.Transform = types.StringValue(`{ "type": "null" }`)
	equivalentPlan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, equivalentPlan.Set(ctx, &created).HasError())
	equivalentResponse := resource.UpdateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Update(ctx, resource.UpdateRequest{State: createResponse.State, Plan: equivalentPlan}, &equivalentResponse)
	require.False(t, equivalentResponse.Diagnostics.HasError(), equivalentResponse.Diagnostics.Errors())
	assert.Equal(t, 1, createCalls)
	assert.Equal(t, 0, dropCalls)

	deleteResponse := resource.DeleteResponse{State: equivalentResponse.State}
	managed.Delete(ctx, resource.DeleteRequest{State: equivalentResponse.State}, &deleteResponse)
	require.False(t, deleteResponse.Diagnostics.HasError(), deleteResponse.Diagnostics.Errors())
	assert.Nil(t, remote)
	assert.Equal(t, 2, listCalls)
	assert.Equal(t, 1, dropCalls)
}

func TestRowFilterUpdateRestoresPreviousPolicyWhenReplacementFails(t *testing.T) {
	ctx := context.Background()
	remotePredicate := `{"version":"old"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies/drop":
			remotePredicate = ""
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			var body client.PolicyRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			if body.RowFilter.Predicate == `{"version":"new"}` {
				w.WriteHeader(http.StatusBadRequest)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 400, "message": "invalid replacement"}))

				return
			}
			remotePredicate = body.RowFilter.Predicate
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	managed := &rowFilterResource{client: api}
	var schemaResponse resource.SchemaResponse
	managed.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	stateModel := rowFilterResourceModel{
		AllowNonAtomicUpdate: types.BoolValue(true),
		ID:                   types.StringValue("database=analytics&principal=role%3Aanalyst&table=events"),
		Database:             types.StringValue("analytics"),
		Table:                types.StringValue("events"),
		Principal:            types.StringValue("role:analyst"),
		Predicate:            types.StringValue(`{"version":"old"}`),
	}
	planModel := stateModel
	planModel.Predicate = types.StringValue(`{"version":"new"}`)
	state := tfsdk.State{Schema: schemaResponse.Schema}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, state.Set(ctx, &stateModel).HasError())
	require.False(t, plan.Set(ctx, &planModel).HasError())
	response := resource.UpdateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError())
	assert.Contains(t, response.Diagnostics.Errors()[0].Detail(), "previous row filter was restored")
	assert.Equal(t, `{"version":"old"}`, remotePredicate)
}

func TestPermissionValidationAndImportIdentifiers(t *testing.T) {
	model := permissionResourceModel{
		ResourceType:        types.StringValue(client.ResourceTypeColumn),
		Database:            types.StringValue("analytics"),
		Table:               types.StringValue("events"),
		Function:            types.StringNull(),
		View:                types.StringNull(),
		Access:              types.StringValue(client.PermissionAccessUpdate),
		Principal:           types.StringValue("role:analyst"),
		ColumnNames:         types.SetNull(types.StringType),
		ExcludedColumnNames: types.SetNull(types.StringType),
		ExpireTime:          types.StringValue("2026-09-01T08:00:00+08:00"),
	}
	var diagnostics diag.Diagnostics
	validatePermissionModel(model, &diagnostics)
	require.True(t, diagnostics.HasError())
	assert.Len(t, diagnostics.Errors(), 3)

	parsed, err := parsePermissionID("resource_type=TABLE&database=analytics&table=events&access=SELECT&principal=user%3Aalice%2Fprod")
	require.NoError(t, err)
	assert.Equal(t, "user:alice/prod", parsed.Principal.ValueString())
	assert.Equal(t, "access=SELECT&database=analytics&principal=user%3Aalice%2Fprod&resource_type=TABLE&table=events", permissionID(parsed))

	_, err = parsePermissionID("resource_type=TABLE&database=analytics&table=events&access=CREATEDATABASE&principal=alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid")

	rowID, err := parsePolicyID("database=analytics&table=events&principal=role%3Aanalyst", false)
	require.NoError(t, err)
	assert.Equal(t, "role:analyst", rowID.Get("principal"))
	_, err = parsePolicyID("database=analytics&table=events&principal=role%3Aanalyst", true)
	require.Error(t, err)
}

func TestPermissionExpiryValidationUsesParsedMillisecondPrecision(t *testing.T) {
	base := permissionResourceModel{
		ResourceType:        types.StringValue(client.ResourceTypeTable),
		Database:            types.StringValue("analytics"),
		Table:               types.StringValue("events"),
		Function:            types.StringNull(),
		View:                types.StringNull(),
		Access:              types.StringValue(client.PermissionAccessSelect),
		Principal:           types.StringValue("role:analyst"),
		ColumnNames:         types.SetNull(types.StringType),
		ExcludedColumnNames: types.SetNull(types.StringType),
	}
	for _, test := range []struct {
		name     string
		value    string
		hasError bool
	}{
		{name: "whole seconds", value: "2027-01-01T00:00:00Z"},
		{name: "tenths", value: "2027-01-01T00:00:00.1Z"},
		{name: "hundredths", value: "2027-01-01T00:00:00.12Z"},
		{name: "canonical milliseconds", value: "2027-01-01T00:00:00.123Z"},
		{name: "trailing fractional zeros", value: "2027-01-01T00:00:00.123000Z"},
		{name: "nanosecond-width trailing zeros", value: "2027-01-01T00:00:00.123000000Z"},
		{name: "lower-case separators", value: "2027-01-01t00:00:00.123000z"},
		{name: "numeric offset is not portable across supported JDKs", value: "2027-01-01T01:00:00.123+01:00", hasError: true},
		{name: "microsecond precision", value: "2027-01-01T00:00:00.123456Z", hasError: true},
		{name: "nanosecond precision", value: "2027-01-01T00:00:00.123000001Z", hasError: true},
		{name: "too many fractional digits", value: "2027-01-01T00:00:00.1230000000Z", hasError: true},
		{name: "invalid instant", value: "2027-02-29T00:00:00Z", hasError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := base
			model.ExpireTime = types.StringValue(test.value)
			var diagnostics diag.Diagnostics
			validatePermissionModel(model, &diagnostics)
			assert.Equal(t, test.hasError, diagnostics.HasError())
		})
	}

	expectedExpiry := "2027-01-01t00:00:00.123000z"
	observedExpiry := "2027-01-01T00:00:00.123Z"
	assignment := client.PermissionAssignment{
		Resource:   client.PermissionResource{Type: client.ResourceTypeTable, Database: "analytics", Table: "events"},
		Access:     client.PermissionAccessSelect,
		Principal:  "role:analyst",
		ExpireTime: &expectedExpiry,
	}
	observed := assignment
	observed.ExpireTime = &observedExpiry
	assert.True(t, permissionAssignmentsEquivalent(assignment, observed))

	model := base
	model.ExpireTime = types.StringValue(expectedExpiry)
	var diagnostics diag.Diagnostics
	wireAssignment := permissionAssignmentFromModel(context.Background(), model, &diagnostics)
	require.False(t, diagnostics.HasError())
	require.NotNil(t, wireAssignment.ExpireTime)
	assert.Equal(t, "2027-01-01T00:00:00.123Z", *wireAssignment.ExpireTime)

	setPermissionModel(context.Background(), &model, observed, &diagnostics)
	require.False(t, diagnostics.HasError())
	assert.Equal(t, expectedExpiry, model.ExpireTime.ValueString())
}

func TestValidateSerializedPolicy(t *testing.T) {
	var diagnostics diag.Diagnostics
	validateSerializedPolicy("predicate", `{invalid`, &diagnostics)
	require.True(t, diagnostics.HasError())

	diagnostics = nil
	validateSerializedPolicy("predicate", `{"field":"tenant_id"}`, &diagnostics)
	require.False(t, diagnostics.HasError())

	assert.True(t, equivalentJSON(`{"a":1,"b":2}`, `{"b":2,"a":1}`))
	assert.False(t, equivalentJSON(`{"literal":9007199254740992}`, `{"literal":9007199254740993}`))
}

func TestPrincipalValidatorsMatchJavaContract(t *testing.T) {
	ctx := context.Background()
	var permissionSchemaResponse resource.SchemaResponse
	(&permissionResource{}).Schema(ctx, resource.SchemaRequest{}, &permissionSchemaResponse)
	permissionPrincipal, ok := permissionSchemaResponse.Schema.Attributes["principal"].(resourceschema.StringAttribute)
	require.True(t, ok)

	tests := []struct {
		name       string
		validators []schemavalidator.String
	}{
		{name: "permission", validators: permissionPrincipal.Validators},
		{name: "policy", validators: principalValidators()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, value := range []struct {
				principal string
				hasError  bool
			}{
				{principal: strings.Repeat("界", 128)},
				{principal: strings.Repeat("界", 129), hasError: true},
				{principal: strings.Repeat("😀", 64)},
				{principal: strings.Repeat("😀", 65), hasError: true},
				{principal: " \t", hasError: true},
			} {
				var diagnostics diag.Diagnostics
				for _, configuredValidator := range test.validators {
					response := schemavalidator.StringResponse{}
					configuredValidator.ValidateString(ctx, schemavalidator.StringRequest{ConfigValue: types.StringValue(value.principal)}, &response)
					diagnostics.Append(response.Diagnostics...)
				}
				assert.Equal(t, value.hasError, diagnostics.HasError())
			}
		})
	}
}

func TestManagementSchemasRejectWhitespaceOnlyIdentifiers(t *testing.T) {
	ctx := context.Background()
	assertRejectsWhitespace := func(t *testing.T, validators []schemavalidator.String) {
		t.Helper()
		var diagnostics diag.Diagnostics
		for _, configuredValidator := range validators {
			response := schemavalidator.StringResponse{}
			configuredValidator.ValidateString(ctx, schemavalidator.StringRequest{ConfigValue: types.StringValue(" \t")}, &response)
			diagnostics.Append(response.Diagnostics...)
		}
		assert.True(t, diagnostics.HasError())
	}

	var permissionSchemaResponse resource.SchemaResponse
	(&permissionResource{}).Schema(ctx, resource.SchemaRequest{}, &permissionSchemaResponse)
	for _, name := range []string{"database", "table", "function", "view"} {
		attribute, ok := permissionSchemaResponse.Schema.Attributes[name].(resourceschema.StringAttribute)
		require.True(t, ok)
		t.Run("permission_"+name, func(t *testing.T) {
			assertRejectsWhitespace(t, attribute.Validators)
		})
	}
	for _, name := range []string{"column_names", "excluded_column_names"} {
		attribute, ok := permissionSchemaResponse.Schema.Attributes[name].(resourceschema.SetAttribute)
		require.True(t, ok)
		var diagnostics diag.Diagnostics
		for _, configuredValidator := range attribute.Validators {
			response := schemavalidator.SetResponse{}
			configuredValidator.ValidateSet(ctx, schemavalidator.SetRequest{
				ConfigValue: types.SetValueMust(types.StringType, []attr.Value{types.StringValue(" \t")}),
			}, &response)
			diagnostics.Append(response.Diagnostics...)
		}
		t.Run("permission_"+name, func(t *testing.T) {
			assert.True(t, diagnostics.HasError())
		})
	}

	var rowFilterSchemaResponse resource.SchemaResponse
	(&rowFilterResource{}).Schema(ctx, resource.SchemaRequest{}, &rowFilterSchemaResponse)
	for _, name := range []string{"database", "table"} {
		attribute, ok := rowFilterSchemaResponse.Schema.Attributes[name].(resourceschema.StringAttribute)
		require.True(t, ok)
		t.Run("row_filter_"+name, func(t *testing.T) {
			assertRejectsWhitespace(t, attribute.Validators)
		})
	}

	var columnMaskSchemaResponse resource.SchemaResponse
	(&columnMaskResource{}).Schema(ctx, resource.SchemaRequest{}, &columnMaskSchemaResponse)
	for _, name := range []string{"database", "table", "column"} {
		attribute, ok := columnMaskSchemaResponse.Schema.Attributes[name].(resourceschema.StringAttribute)
		require.True(t, ok)
		t.Run("column_mask_"+name, func(t *testing.T) {
			assertRejectsWhitespace(t, attribute.Validators)
		})
	}
}

func TestManagementImportIDsMatchJavaBlankAndPrincipalLengthContract(t *testing.T) {
	_, err := parsePermissionID("resource_type=TABLE&database=%20&table=events&access=SELECT&principal=alice")
	require.Error(t, err)
	_, err = parsePermissionID("resource_type=TABLE&database=analytics&table=events&access=SELECT&principal=%20%09")
	require.Error(t, err)
	_, err = parsePermissionID("resource_type=TABLE&database=analytics&table=events&access=SELECT&principal=" + url.QueryEscape(strings.Repeat("😀", 65)))
	require.Error(t, err)

	_, err = parsePolicyID("database=analytics&table=%20&principal=alice", false)
	require.Error(t, err)
	_, err = parsePolicyID("database=analytics&table=events&principal="+url.QueryEscape(strings.Repeat("😀", 65)), false)
	require.Error(t, err)
}

func TestPermissionCreateRetainsStateWhenReconciliationFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/permissions/grant":
			var assignment client.PermissionAssignment
			require.NoError(t, json.NewDecoder(request.Body).Decode(&assignment))
			require.NotNil(t, assignment.ExpireTime)
			assert.Equal(t, "2027-01-01T00:00:00.123Z", *assignment.ExpireTime)
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/permissions":
			listCalls++
			w.WriteHeader(http.StatusInternalServerError)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "unavailable"}))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	managed := &permissionResource{client: api}
	var schemaResponse resource.SchemaResponse
	managed.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	planModel := permissionResourceModel{
		ID:                  types.StringUnknown(),
		ResourceType:        types.StringValue(client.ResourceTypeTable),
		Database:            types.StringValue("analytics"),
		Table:               types.StringValue("events"),
		Function:            types.StringNull(),
		View:                types.StringNull(),
		Access:              types.StringValue(client.PermissionAccessSelect),
		Principal:           types.StringValue("role:analyst"),
		ColumnNames:         types.SetNull(types.StringType),
		ExcludedColumnNames: types.SetNull(types.StringType),
		ExpireTime:          types.StringValue("2027-01-01t00:00:00.123000z"),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Create(ctx, resource.CreateRequest{Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError())
	var retained permissionResourceModel
	require.False(t, response.State.Get(ctx, &retained).HasError())
	assert.Equal(t, permissionID(planModel), retained.ID.ValueString())
	assert.Equal(t, "2027-01-01t00:00:00.123000z", retained.ExpireTime.ValueString())
	assert.GreaterOrEqual(t, listCalls, 3)
}

func TestPermissionCreateReconcilesLostResponse(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var remote *client.PermissionAssignment
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/permissions/grant":
			var assignment client.PermissionAssignment
			require.NoError(t, json.NewDecoder(request.Body).Decode(&assignment))
			mu.Lock()
			remote = &assignment
			mu.Unlock()
			hijacker, ok := w.(http.Hijacker)
			require.True(t, ok)
			connection, _, err := hijacker.Hijack()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/permissions":
			mu.Lock()
			listed := *remote
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(client.ListPermissionsResponse{Permissions: []client.PermissionAssignment{listed}}))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	managed := &permissionResource{client: api}
	var schemaResponse resource.SchemaResponse
	managed.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	planModel := permissionResourceModel{
		ID:                  types.StringUnknown(),
		ResourceType:        types.StringValue(client.ResourceTypeTable),
		Database:            types.StringValue("analytics"),
		Table:               types.StringValue("events"),
		Function:            types.StringNull(),
		View:                types.StringNull(),
		Access:              types.StringValue(client.PermissionAccessSelect),
		Principal:           types.StringValue("role:analyst"),
		ColumnNames:         types.SetNull(types.StringType),
		ExcludedColumnNames: types.SetNull(types.StringType),
		ExpireTime:          types.StringValue("2027-01-01t00:00:00.123000z"),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Create(ctx, resource.CreateRequest{Plan: plan}, &response)
	require.False(t, response.Diagnostics.HasError(), response.Diagnostics.Errors())
	require.NotEmpty(t, response.Diagnostics.Warnings())
	var recovered permissionResourceModel
	require.False(t, response.State.Get(ctx, &recovered).HasError())
	assert.Equal(t, permissionID(planModel), recovered.ID.ValueString())
	assert.Equal(t, "2027-01-01t00:00:00.123000z", recovered.ExpireTime.ValueString())
	mu.Lock()
	remoteExpiry := remote.ExpireTime
	mu.Unlock()
	require.NotNil(t, remoteExpiry)
	assert.Equal(t, "2027-01-01T00:00:00.123Z", *remoteExpiry)
}

func TestRowFilterCreateRetainsStateWhenReconciliationFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
			require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			listCalls++
			w.WriteHeader(http.StatusInternalServerError)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "unavailable"}))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	api, err := client.New(client.Config{URI: server.URL})
	require.NoError(t, err)
	managed := &rowFilterResource{client: api}
	var schemaResponse resource.SchemaResponse
	managed.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	planModel := rowFilterResourceModel{
		ID:        types.StringUnknown(),
		Database:  types.StringValue("analytics"),
		Table:     types.StringValue("events"),
		Principal: types.StringValue("role:analyst"),
		Predicate: types.StringValue(`{"field":"tenant_id"}`),
	}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &planModel).HasError())
	response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	managed.Create(ctx, resource.CreateRequest{Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError())
	var retained rowFilterResourceModel
	require.False(t, response.State.Get(ctx, &retained).HasError())
	assert.Equal(t, rowFilterID(planModel), retained.ID.ValueString())
	assert.GreaterOrEqual(t, listCalls, 3)
}

func TestPolicyCreateReconcilesLostResponse(t *testing.T) {
	tests := []struct {
		name string
		spec policySpec
	}{
		{name: "row filter", spec: rowFilterSpec(rowFilterResourceModel{
			Database: types.StringValue("analytics"), Table: types.StringValue("events"), Principal: types.StringValue("role:analyst"), Predicate: types.StringValue(`{"field":"tenant_id"}`),
		})},
		{name: "column mask", spec: columnMaskSpec(columnMaskResourceModel{
			Database: types.StringValue("analytics"), Table: types.StringValue("events"), Principal: types.StringValue("role:analyst"), Column: types.StringValue("email"), Transform: types.StringValue(`{"name":"CONCAT","inputs":["***"]}`),
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var remote *client.DataPolicy
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
					require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
				case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
					var body client.PolicyRequest
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					policy := dataPolicyFromRequest(body)
					mu.Lock()
					remote = &policy
					mu.Unlock()
					hijacker, ok := w.(http.Hijacker)
					require.True(t, ok)
					connection, _, err := hijacker.Hijack()
					require.NoError(t, err)
					require.NoError(t, connection.Close())
				case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
					mu.Lock()
					listed := *remote
					mu.Unlock()
					require.NoError(t, json.NewEncoder(w).Encode(client.ListPoliciesResponse{Policies: []client.DataPolicy{listed}}))
				default:
					http.NotFound(w, request)
				}
			}))
			defer server.Close()

			api, err := client.New(client.Config{URI: server.URL})
			require.NoError(t, err)
			result := createPolicyWithReconciliation(context.Background(), api, test.spec, false)
			require.True(t, result.accepted)
			require.NoError(t, result.err)
			require.NotNil(t, result.observed)
			matches, matchErr := test.spec.matchesWithSchema(context.Background(), api, *result.observed)
			require.NoError(t, matchErr)
			assert.True(t, matches)
			assert.NotEmpty(t, result.warning)
		})
	}
}

func TestPolicyReplacementRecoversCanceledDrop(t *testing.T) {
	tests := []struct {
		name     string
		previous policySpec
		desired  policySpec
	}{
		{
			name:     "row filter",
			previous: rowFilterSpec(rowFilterResourceModel{Database: types.StringValue("analytics"), Table: types.StringValue("events"), Principal: types.StringValue("role:analyst"), Predicate: types.StringValue(`{"version":"old"}`)}),
			desired:  rowFilterSpec(rowFilterResourceModel{Database: types.StringValue("analytics"), Table: types.StringValue("events"), Principal: types.StringValue("role:analyst"), Predicate: types.StringValue(`{"version":"new"}`)}),
		},
		{
			name:     "column mask",
			previous: columnMaskSpec(columnMaskResourceModel{Database: types.StringValue("analytics"), Table: types.StringValue("events"), Principal: types.StringValue("role:analyst"), Column: types.StringValue("email"), Transform: types.StringValue(`{"value":"old"}`)}),
			desired:  columnMaskSpec(columnMaskResourceModel{Database: types.StringValue("analytics"), Table: types.StringValue("events"), Principal: types.StringValue("role:analyst"), Column: types.StringValue("email"), Transform: types.StringValue(`{"value":"new"}`)}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			initial := dataPolicyFromSpec(test.previous)
			remote := &initial
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case request.Method == http.MethodGet && request.URL.Path == "/v1/config":
					require.NoError(t, json.NewEncoder(w).Encode(client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}}))
				case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies/drop":
					mu.Lock()
					remote = nil
					mu.Unlock()
					cancel()
					hijacker, ok := w.(http.Hijacker)
					require.True(t, ok)
					connection, _, err := hijacker.Hijack()
					require.NoError(t, err)
					require.NoError(t, connection.Close())
				case request.Method == http.MethodPost && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
					var body client.PolicyRequest
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					policy := dataPolicyFromRequest(body)
					mu.Lock()
					remote = &policy
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
				case request.Method == http.MethodGet && request.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
					mu.Lock()
					var policies []client.DataPolicy
					if remote != nil {
						policies = []client.DataPolicy{*remote}
					}
					mu.Unlock()
					require.NoError(t, json.NewEncoder(w).Encode(client.ListPoliciesResponse{Policies: policies}))
				default:
					http.NotFound(w, request)
				}
			}))
			defer server.Close()

			api, err := client.New(client.Config{URI: server.URL})
			require.NoError(t, err)
			result := replacePolicyWithReconciliation(ctx, api, test.previous, test.desired)
			require.True(t, result.desired)
			require.NoError(t, result.err)
			mu.Lock()
			require.NotNil(t, remote)
			matches, matchErr := test.desired.matchesWithSchema(context.Background(), api, *remote)
			require.NoError(t, matchErr)
			assert.True(t, matches)
			mu.Unlock()
		})
	}
}

func dataPolicyFromSpec(spec policySpec) client.DataPolicy {
	return dataPolicyFromRequest(spec.request)
}

func dataPolicyFromRequest(request client.PolicyRequest) client.DataPolicy {
	return client.DataPolicy{
		Resource:   client.PermissionResource{Type: client.ResourceTypeTable, Database: "analytics", Table: "events"},
		RowFilter:  request.RowFilter,
		ColumnMask: request.ColumnMask,
		Principal:  request.Principal,
	}
}
