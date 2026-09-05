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
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &tableResource{}
	_ resource.ResourceWithImportState = &tableResource{}
	_ resource.ResourceWithModifyPlan  = &tableResource{}
)

type tableResource struct {
	client *client.Client
}

type tableResourceModel struct {
	ID               types.String `tfsdk:"id"`
	ServerID         types.String `tfsdk:"server_id"`
	Database         types.String `tfsdk:"database"`
	Name             types.String `tfsdk:"name"`
	Fields           types.List   `tfsdk:"fields"`
	PartitionKeys    types.List   `tfsdk:"partition_keys"`
	PrimaryKeys      types.List   `tfsdk:"primary_keys"`
	Options          types.Map    `tfsdk:"options"`
	AllowReplacement types.Bool   `tfsdk:"allow_replacement"`
	ServerOptions    types.Map    `tfsdk:"server_options"`
	Comment          types.String `tfsdk:"comment"`
	SchemaID         types.Int64  `tfsdk:"schema_id"`
	Path             types.String `tfsdk:"path"`
	IsExternal       types.Bool   `tfsdk:"is_external"`
	Owner            types.String `tfsdk:"owner"`
	CreatedAt        types.Int64  `tfsdk:"created_at"`
	CreatedBy        types.String `tfsdk:"created_by"`
	UpdatedAt        types.Int64  `tfsdk:"updated_at"`
	UpdatedBy        types.String `tfsdk:"updated_by"`
}

func NewTableResource() resource.Resource {
	return &tableResource{}
}

func (r *tableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_table"
}

func (r *tableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a table in a Paimon REST Catalog. Dropping this resource calls the catalog's managed-table drop API and can remove table data.",
		Attributes:  tableResourceAttributes(),
	}
}

func (r *tableResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	clientFromProviderData(req.ProviderData, &r.client, &resp.Diagnostics, "paimon_table resource")
}

func (r *tableResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var config, state, plan tableResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	stabilizeTableKeys(ctx, config, state, &plan, &resp.Diagnostics)
	replacementPaths := make([]path.Path, 0)
	if tableFieldsInspectable(config.Fields) && tableFieldsInspectable(plan.Fields) {
		var configuredFields, stateFields, plannedFields []tableFieldModel
		resp.Diagnostics.Append(config.Fields.ElementsAs(ctx, &configuredFields, false)...)
		if !req.State.Raw.IsNull() {
			resp.Diagnostics.Append(state.Fields.ElementsAs(ctx, &stateFields, false)...)
		}
		resp.Diagnostics.Append(plan.Fields.ElementsAs(ctx, &plannedFields, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if len(configuredFields) != len(plannedFields) {
			resp.Diagnostics.AddError("Unable to stabilize Paimon field identities", "The configured and planned field lists have different lengths. Please report this issue to the provider developers.")

			return
		}

		stabilizePlannedFieldIdentities(configuredFields, stateFields, plannedFields)
		if !req.State.Raw.IsNull() {
			validateAddedFieldIDs(stateFields, configuredFields, &resp.Diagnostics)
		}
		stabilizeFieldNullability(ctx, configuredFields, stateFields, plannedFields, plan, state, &resp.Diagnostics)
		keyFields := append(stringListFromValue(ctx, state.PartitionKeys, &resp.Diagnostics), stringListFromValue(ctx, state.PrimaryKeys, &resp.Diagnostics)...)
		keyFields = append(keyFields, stringListFromValue(ctx, plan.PartitionKeys, &resp.Diagnostics)...)
		keyFields = append(keyFields, stringListFromValue(ctx, plan.PrimaryKeys, &resp.Diagnostics)...)
		plan.Fields = fieldsValueFromModels(ctx, plannedFields, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		if !req.State.Raw.IsNull() && (compositeFieldTypesRequireReplace(stateFields, plannedFields) ||
			keyFieldTypesRequireReplace(stateFields, plannedFields, keyFields) ||
			newNonNullableFieldsRequireReplace(stateFields, plannedFields)) {
			replacementPaths = append(replacementPaths, path.Root("fields"))
		}

	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	if req.State.Raw.IsNull() || resp.Diagnostics.HasError() {
		return
	}
	if !plan.PrimaryKeys.IsUnknown() && !state.PrimaryKeys.Equal(plan.PrimaryKeys) {
		replacementPaths = append(replacementPaths, path.Root("options").AtMapKey("primary-key"))
	}
	if !plan.PartitionKeys.IsUnknown() && !state.PartitionKeys.Equal(plan.PartitionKeys) {
		replacementPaths = append(replacementPaths, path.Root("partition_keys"))
	}
	if !plan.Name.IsUnknown() && !state.Name.Equal(plan.Name) {
		replacementPaths = append(replacementPaths, path.Root("name"))
	}
	if !plan.Database.IsUnknown() && !state.Database.Equal(plan.Database) {
		replacementPaths = append(replacementPaths, path.Root("database"))
	}
	if len(replacementPaths) > 0 {
		if !plan.AllowReplacement.ValueBool() {
			resp.Diagnostics.AddError("Destructive table change is disabled", "This table identity or schema change requires dropping and recreating the table and can delete its data. Use a data migration, or explicitly set allow_replacement = true to permit replacement.")

			return
		}
		resp.RequiresReplace = append(resp.RequiresReplace, replacementPaths...)
	}
}

func validateAddedFieldIDs(previous, configured []tableFieldModel, diags *diag.Diagnostics) {
	known := make(map[int64]struct{}, len(previous))
	for _, field := range previous {
		if !field.ID.IsNull() && !field.ID.IsUnknown() {
			known[field.ID.ValueInt64()] = struct{}{}
		}
	}
	for index, field := range configured {
		if field.ID.IsNull() || field.ID.IsUnknown() {
			continue
		}
		if _, exists := known[field.ID.ValueInt64()]; !exists {
			diags.AddAttributeError(path.Root("fields").AtListIndex(index).AtName("id"), "New field IDs are assigned by Paimon", "Omit id when adding a field to an existing table. Explicit IDs identify existing fields for updates and renames; the add-column API assigns new IDs on the server.")
		}
	}
}

func stabilizePlannedFieldIdentities(configured, state, planned []tableFieldModel) {
	stateByName := make(map[string]tableFieldModel, len(state))
	stateByID := make(map[int64]tableFieldModel, len(state))
	for _, field := range state {
		if !field.Name.IsNull() && !field.Name.IsUnknown() {
			stateByName[field.Name.ValueString()] = field
		}
		if !field.ID.IsNull() && !field.ID.IsUnknown() {
			stateByID[field.ID.ValueInt64()] = field
		}
	}

	for index := range planned {
		var previous tableFieldModel
		var matched bool
		if !configured[index].ID.IsNull() && !configured[index].ID.IsUnknown() {
			previous, matched = stateByID[configured[index].ID.ValueInt64()]
			planned[index].ID = configured[index].ID
		} else if !configured[index].Name.IsNull() && !configured[index].Name.IsUnknown() {
			previous, matched = stateByName[configured[index].Name.ValueString()]
			if matched {
				planned[index].ID = previous.ID
			}
		}
		if matched {
			planned[index].NestedFieldIDs = previous.NestedFieldIDs
		}
	}
}

func (r *tableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tableSchema := schemaFromResourceModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.CreateTable(ctx, plan.Database.ValueString(), plan.Name.ValueString(), tableCreateSchema(ctx, plan, tableSchema, &resp.Diagnostics)); err != nil {
		if !client.IsMutationOutcomeUncertain(err) {
			resp.Diagnostics.AddError("Unable to create Paimon table", err.Error())

			return
		}
		table, recoveryErr := r.lookupAfterMutation(ctx, plan.Database.ValueString(), plan.Name.ValueString(), func(observed *client.Table) bool {
			return tableMatchesPlannedSchema(observed, tableSchema, true, true)
		})
		if recoveryErr != nil {
			resp.Diagnostics.AddError("Unable to create Paimon table", fmt.Sprintf("create request failed (%s), and bounded reconciliation could not establish the remote state: %s", err, recoveryErr))

			return
		}
		stableCtx := context.WithoutCancel(ctx)
		setTableResourceModel(stableCtx, &plan, table, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
		resp.Diagnostics.AddWarning("Recovered Paimon table creation", "The create request returned an error, but bounded reconciliation found the exact table schema that Terraform planned, so the resource was adopted into state.")

		return
	}
	stableCtx := context.WithoutCancel(ctx)
	plan.ID = types.StringValue(tableID(plan.Database.ValueString(), plan.Name.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	table, err := r.lookupAfterMutation(ctx, plan.Database.ValueString(), plan.Name.ValueString(), func(observed *client.Table) bool {
		return tableMatchesPlannedSchema(observed, tableSchema, true, false)
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to verify Paimon table after creation", err.Error()+". Terraform retained the planned table identity in state.")

		return
	}
	setTableResourceModel(stableCtx, &plan, table, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
}

func (r *tableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	table, err := r.client.GetTable(ctx, state.Database.ValueString(), state.Name.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read Paimon table", err.Error())

		return
	}
	setTableResourceModel(ctx, &state, table, &resp.Diagnostics)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
}

func (r *tableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan tableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	beforeSchema := schemaFromResourceModel(ctx, &state, &resp.Diagnostics)
	effectivePlan := tablePlanWithServerDefaults(plan, state)
	afterSchema := schemaFromResourceModel(ctx, &effectivePlan, &resp.Diagnostics)
	var plannedFields []tableFieldModel
	resp.Diagnostics.Append(plan.Fields.ElementsAs(ctx, &plannedFields, false)...)
	if !resp.Diagnostics.HasError() {
		validateAddedFieldIDs(fieldModelsFromRemote(beforeSchema.Fields), plannedFields, &resp.Diagnostics)
		if err := assignTemporaryIDsToNewFields(beforeSchema.Fields, plannedFields, afterSchema.Fields); err != nil {
			resp.Diagnostics.AddError("Unable to plan Paimon table field identities", err.Error())
		}
	}
	before := beforeSchema.Options
	after := afterSchema.Options
	serverOptions := mapFromValue(ctx, state.ServerOptions, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !slices.Equal(beforeSchema.PrimaryKeys, afterSchema.PrimaryKeys) || !slices.Equal(beforeSchema.PartitionKeys, afterSchema.PartitionKeys) {
		resp.Diagnostics.AddError("Table key change requires replacement", "Changing table keys must be planned as a replacement; no in-place key mutation was sent.")

		return
	}
	baseline := mapFromValue(ctx, effectiveManagedTableOptions(state.Options, plan.Options, tableOptionsWithPrimaryKeys(ctx, state.ServerOptions, state.PrimaryKeys, &resp.Diagnostics)), &resp.Diagnostics)
	removals, updates := diffTableOptions(baseline, after)
	sort.Strings(removals)
	updateKeys := make([]string, 0, len(updates))
	for key := range updates {
		updateKeys = append(updateKeys, key)
	}
	sort.Strings(updateKeys)

	addBeforePartitionValue, serverReportedOption := serverOptions["add-column-before-partition"]
	if !serverReportedOption {
		addBeforePartitionValue = before["add-column-before-partition"]
	}
	addBeforePartition := strings.EqualFold(strings.TrimSpace(addBeforePartitionValue), "true")
	changes, err := tableFieldSchemaChanges(beforeSchema.Fields, afterSchema.Fields, addBeforePartition, beforeSchema.PartitionKeys)
	if err != nil {
		resp.Diagnostics.AddError("Unable to plan Paimon table schema changes", err.Error())

		return
	}
	for _, key := range removals {
		changes = append(changes, client.SchemaChange{"action": "removeOption", "key": key})
	}
	for _, key := range updateKeys {
		changes = append(changes, client.SchemaChange{"action": "setOption", "key": key, "value": updates[key]})
	}
	if !state.Comment.Equal(plan.Comment) {
		changes = append(changes, client.SchemaChange{"action": "updateComment", "comment": optionalStringPointer(plan.Comment)})
	}
	stableCtx := context.WithoutCancel(ctx)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ready := func(observed *client.Table) bool {
		return tableMatchesPlannedSchema(observed, afterSchema, false, false) && optionsConverged(observed.Schema.Options, nil, removals)
	}

	if len(changes) > 0 {
		if err := r.client.AlterTable(ctx, plan.Database.ValueString(), plan.Name.ValueString(), changes); err != nil {
			if client.IsMutationOutcomeUncertain(err) {
				table, recoveryErr := r.lookupAfterMutation(ctx, plan.Database.ValueString(), plan.Name.ValueString(), ready)
				if recoveryErr == nil {
					stableCtx := context.WithoutCancel(ctx)
					setTableResourceModel(stableCtx, &plan, table, &resp.Diagnostics)
					resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
					resp.Diagnostics.AddWarning("Recovered Paimon table update", "The alter request returned an error, but bounded reconciliation found the exact table schema that Terraform planned.")

					return
				}
			}
			resp.Diagnostics.AddError("Unable to update Paimon table", err.Error())

			return
		}
	}
	table, err := r.lookupAfterMutation(ctx, plan.Database.ValueString(), plan.Name.ValueString(), ready)
	if err != nil {
		resp.Diagnostics.AddError("Unable to verify Paimon table after update", err.Error()+". Terraform retained the previous state so unverified removals remain managed.")

		return
	}
	setTableResourceModel(stableCtx, &plan, table, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
}

func (r *tableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DropTable(ctx, state.Database.ValueString(), state.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to drop Paimon table", err.Error())
	}
}

func (r *tableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	database, name, err := parseTableID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Paimon table import identifier", err.Error())

		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tableID(database, name))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database"), database)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

func (r *tableResource) lookupAfterMutation(ctx context.Context, database, name string, ready func(*client.Table) bool) (*client.Table, error) {
	recoveryCtx, cancel := mutationRecoveryContext(ctx, r.client)
	defer cancel()
	table, found, converged, err := retryLookupUntil(recoveryCtx, func(attemptCtx context.Context) (*client.Table, bool, error) {
		observed, lookupErr := r.client.GetTable(attemptCtx, database, name)
		if client.IsNotFound(lookupErr) {
			return nil, false, nil
		}

		return observed, lookupErr == nil, lookupErr
	}, ready)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("table %q.%q was not visible during bounded reconciliation", database, name)
	}
	if !converged {
		return nil, fmt.Errorf("table %q.%q did not converge to the planned schema during bounded reconciliation", database, name)
	}

	return table, nil
}

func tableMatchesPlannedSchema(table *client.Table, expected client.Schema, compareFieldIDs, exactOptions bool) bool {
	if table == nil || len(table.Schema.Fields) != len(expected.Fields) || len(table.Schema.PartitionKeys) != len(expected.PartitionKeys) || len(table.Schema.PrimaryKeys) != len(expected.PrimaryKeys) || exactOptions && len(table.Schema.Options) != len(expected.Options) {
		return false
	}
	for index, planned := range expected.Fields {
		observed := table.Schema.Fields[index]
		if compareFieldIDs && (observed.ID != planned.ID || len(planned.NestedFieldIDs) > 0 && !maps.Equal(observed.NestedFieldIDs, planned.NestedFieldIDs)) || observed.Name != planned.Name || !client.EquivalentDataTypes(observed.Type, planned.Type) || !stringPointersEqual(observed.Description, planned.Description) || !stringPointersEqual(observed.DefaultValue, planned.DefaultValue) {
			return false
		}
	}
	for index := range expected.PartitionKeys {
		if table.Schema.PartitionKeys[index] != expected.PartitionKeys[index] {
			return false
		}
	}
	for index := range expected.PrimaryKeys {
		if table.Schema.PrimaryKeys[index] != expected.PrimaryKeys[index] {
			return false
		}
	}
	for key, value := range expected.Options {
		observed, exists := table.Schema.Options[key]
		if key == "type" {
			if !exists {
				observed = "table"
			}
			if strings.EqualFold(observed, value) {
				continue
			}
		}
		if !exists || observed != value {
			return false
		}
	}

	return stringPointersEqual(table.Schema.Comment, expected.Comment)
}

func setTableResourceModel(ctx context.Context, model *tableResourceModel, table *client.Table, diags *diag.Diagnostics) {
	database := table.Database
	if database == "" {
		database = model.Database.ValueString()
	}
	name := table.Name
	if name == "" {
		name = model.Name.ValueString()
	}
	model.ID = types.StringValue(tableID(database, name))
	model.ServerID = types.StringValue(table.ID)
	model.Database = types.StringValue(database)
	model.Name = types.StringValue(name)
	model.Fields = resourceFieldsValueFromRemote(ctx, model.Fields, table.Schema.Fields, diags)
	model.PartitionKeys = stringListValue(ctx, table.Schema.PartitionKeys, diags)
	model.PrimaryKeys = stringListValue(ctx, table.Schema.PrimaryKeys, diags)
	model.Options = syncManagedTableOptions(ctx, model.Options, tableOptionsMapWithPrimaryKeys(table.Schema.Options, table.Schema.PrimaryKeys), diags)
	if model.AllowReplacement.IsNull() {
		model.AllowReplacement = types.BoolValue(false)
	}
	model.ServerOptions = stringMapValue(ctx, table.Schema.Options, diags)
	model.Comment = stringValueFromPointer(table.Schema.Comment)
	model.SchemaID = types.Int64Value(table.SchemaID)
	model.Path = types.StringValue(table.Path)
	model.IsExternal = types.BoolValue(table.IsExternal)
	model.Owner = types.StringValue(table.Owner)
	model.CreatedAt = types.Int64Value(table.CreatedAt)
	model.CreatedBy = types.StringValue(table.CreatedBy)
	model.UpdatedAt = types.Int64Value(table.UpdatedAt)
	model.UpdatedBy = types.StringValue(table.UpdatedBy)
}

func tableID(database, name string) string {
	values := make(url.Values, 2)
	values.Set("database", database)
	values.Set("table", name)

	return values.Encode()
}

func parseTableID(value string) (string, string, error) {
	if strings.Contains(value, "=") {
		values, err := url.ParseQuery(value)
		if err != nil {
			return "", "", errors.New("table import identifier must be a valid URL query")
		}
		if len(values) != 2 || len(values["database"]) != 1 || len(values["table"]) != 1 || values.Get("database") == "" || values.Get("table") == "" {
			return "", "", errors.New("table import identifier must contain exactly one non-empty database and table query parameter")
		}

		return values.Get("database"), values.Get("table"), nil
	}
	separator := strings.LastIndex(value, ".")
	if separator <= 0 || separator == len(value)-1 {
		return "", "", fmt.Errorf("expected database=<name>&table=<name> or the legacy database.table form, got: %s", value)
	}

	return value[:separator], value[separator+1:], nil
}
