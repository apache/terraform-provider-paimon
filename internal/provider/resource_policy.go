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
	"fmt"
	"net/url"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const maxSerializedPolicyBytes = 60 * 1024

const nonAtomicPolicyUpdateDetail = "The REST Catalog has no atomic policy update. Changing policy content drops and recreates the policy, leaving an interval without filtering or masking. Stop affected queries first, then explicitly set allow_non_atomic_update = true for the maintenance operation. Rollback cannot eliminate this interval."

func nonAtomicPolicyUpdateAttribute() schema.BoolAttribute {
	return schema.BoolAttribute{
		Description: "Permit drop-and-create updates during a controlled maintenance window. Defaults to false because reads can be unfiltered or unmasked between requests.",
		Optional:    true,
		Computed:    true,
		Default:     booldefault.StaticBool(false),
	}
}

func (r *rowFilterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	validatePolicyUpdatePlan(ctx, r.client, req, resp, "predicate")
}

func (r *columnMaskResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	validatePolicyUpdatePlan(ctx, r.client, req, resp, "transform")
}

func validatePolicyUpdatePlan(ctx context.Context, api *client.Client, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, contentAttribute string) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	identities := []string{"database", "table", "principal"}
	if contentAttribute == "transform" {
		identities = append(identities, "column")
	}
	for _, name := range identities {
		var previous, planned types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &previous)...)
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(name), &planned)...)
		if resp.Diagnostics.HasError() || planned.IsUnknown() || !previous.Equal(planned) {
			// Identity changes use replacement. Its destination table may not
			// exist yet, and this guard only governs in-place content updates.
			return
		}
	}
	var before, after types.String
	var allowed types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(contentAttribute), &before)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(contentAttribute), &after)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("allow_non_atomic_update"), &allowed)...)
	if resp.Diagnostics.HasError() || before.IsUnknown() || after.IsUnknown() {
		return
	}
	var database, table types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("database"), &database)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("table"), &table)...)
	if resp.Diagnostics.HasError() || database.IsUnknown() || table.IsUnknown() {
		return
	}
	policyType := client.PolicyTypeRowFilter
	if contentAttribute == "transform" {
		policyType = client.PolicyTypeColumnMasking
	}
	equal, err := equivalentPolicyContent(ctx, api, policySpec{database: database.ValueString(), table: table.ValueString(), policyType: policyType}, before.ValueString(), after.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to compare Paimon policy", err.Error())

		return
	}
	if !equal && !allowed.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root(contentAttribute), "Non-atomic policy update is disabled", nonAtomicPolicyUpdateDetail)
	}
}

var (
	_ resource.Resource                   = &rowFilterResource{}
	_ resource.ResourceWithImportState    = &rowFilterResource{}
	_ resource.ResourceWithValidateConfig = &rowFilterResource{}
	_ resource.ResourceWithModifyPlan     = &rowFilterResource{}
	_ resource.Resource                   = &columnMaskResource{}
	_ resource.ResourceWithImportState    = &columnMaskResource{}
	_ resource.ResourceWithValidateConfig = &columnMaskResource{}
	_ resource.ResourceWithModifyPlan     = &columnMaskResource{}
)

type rowFilterResource struct {
	client *client.Client
}

type rowFilterResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Database             types.String `tfsdk:"database"`
	Table                types.String `tfsdk:"table"`
	Principal            types.String `tfsdk:"principal"`
	Predicate            types.String `tfsdk:"predicate"`
	AllowNonAtomicUpdate types.Bool   `tfsdk:"allow_non_atomic_update"`
}

type columnMaskResource struct {
	client *client.Client
}

type columnMaskResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Database             types.String `tfsdk:"database"`
	Table                types.String `tfsdk:"table"`
	Principal            types.String `tfsdk:"principal"`
	Column               types.String `tfsdk:"column"`
	Transform            types.String `tfsdk:"transform"`
	AllowNonAtomicUpdate types.Bool   `tfsdk:"allow_non_atomic_update"`
}

func NewRowFilterResource() resource.Resource {
	return &rowFilterResource{}
}

func NewColumnMaskResource() resource.Resource {
	return &columnMaskResource{}
}

func (r *rowFilterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_row_filter"
}

func (r *rowFilterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manages one principal-scoped row-filter policy on a Paimon table. The table must enable query-auth.enabled. The management API is experimental in Paimon.",
		Attributes: map[string]schema.Attribute{
			"allow_non_atomic_update": nonAtomicPolicyUpdateAttribute(),
			"id": schema.StringAttribute{
				Description:   "Stable URL-query identifier for the row-filter identity.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"database":  schema.StringAttribute{Description: "Database containing the protected table.", Required: true, Validators: nonEmptyStringValidators(), PlanModifiers: replace},
			"table":     schema.StringAttribute{Description: "Protected table name.", Required: true, Validators: nonEmptyStringValidators(), PlanModifiers: replace},
			"principal": schema.StringAttribute{Description: "Opaque canonical principal identifier resolved by the REST server.", Required: true, Validators: principalValidators(), PlanModifiers: replace},
			"predicate": schema.StringAttribute{
				Description: "JSON serialization of one Paimon Predicate. The server validates and canonicalizes it against the table schema.",
				Required:    true,
				Validators:  []validator.String{stringvalidator.UTF8LengthAtMost(maxSerializedPolicyBytes)},
			},
		},
	}
}

func (r *rowFilterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model rowFilterResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() || model.Predicate.IsNull() || model.Predicate.IsUnknown() {
		return
	}
	validateSerializedPolicy("predicate", model.Predicate.ValueString(), &resp.Diagnostics)
}

func (r *rowFilterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	clientFromProviderData(req.ProviderData, &r.client, &resp.Diagnostics, "paimon_row_filter resource")
}

func (r *rowFilterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rowFilterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateSerializedPolicy("predicate", plan.Predicate.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	result := createPolicyWithReconciliation(ctx, r.client, rowFilterSpec(plan), false)
	if !result.accepted {
		resp.Diagnostics.AddError("Unable to create Paimon row filter", result.err.Error())

		return
	}
	plan.ID = types.StringValue(rowFilterID(plan))
	if result.err != nil && result.observed != nil && result.observed.RowFilter != nil && !equivalentJSON(plan.Predicate.ValueString(), result.observed.RowFilter.Predicate) {
		plan.Predicate = types.StringValue(result.observed.RowFilter.Predicate)
	}
	stableCtx := context.WithoutCancel(ctx)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	if result.warning != "" {
		resp.Diagnostics.AddWarning("Recovered Paimon row-filter creation", result.warning)
	}
	if result.err != nil {
		resp.Diagnostics.AddError("Unable to verify Paimon row filter after creation", result.err.Error())
	}
}

func (r *rowFilterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rowFilterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.readIntoState(ctx, &state, &resp.Diagnostics) {
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *rowFilterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan rowFilterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateSerializedPolicy("predicate", plan.Predicate.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	stableCtx := context.WithoutCancel(ctx)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	equal, err := equivalentPolicyContent(ctx, r.client, rowFilterSpec(plan), state.Predicate.ValueString(), plan.Predicate.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to compare Paimon policy", err.Error())

		return
	}
	if equal {
		plan.ID = types.StringValue(rowFilterID(plan))
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)

		return
	}
	if !plan.AllowNonAtomicUpdate.ValueBool() {
		resp.Diagnostics.AddError("Non-atomic policy update is disabled", nonAtomicPolicyUpdateDetail)

		return
	}
	result := replacePolicyWithReconciliation(ctx, r.client, rowFilterSpec(state), rowFilterSpec(plan))
	if result.desired {
		plan.ID = types.StringValue(rowFilterID(plan))
		if result.err != nil && result.observed != nil && result.observed.RowFilter != nil && !equivalentJSON(plan.Predicate.ValueString(), result.observed.RowFilter.Predicate) {
			plan.Predicate = types.StringValue(result.observed.RowFilter.Predicate)
		}
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	}
	if result.warning != "" {
		resp.Diagnostics.AddWarning("Recovered Paimon row-filter replacement", result.warning)
	}
	if result.err != nil {
		resp.Diagnostics.AddError("Unable to replace Paimon row filter", result.err.Error())
	}
}

func (r *rowFilterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rowFilterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DropPolicy(ctx, state.Database.ValueString(), state.Table.ValueString(), client.PolicyTypeRowFilter, state.Principal.ValueString(), ""); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to drop Paimon row filter", err.Error())
	}
}

func (r *rowFilterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	values, err := parsePolicyID(req.ID, false)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Paimon row-filter import identifier", err.Error())

		return
	}
	model := rowFilterResourceModel{
		Database:  types.StringValue(values.Get("database")),
		Table:     types.StringValue(values.Get("table")),
		Principal: types.StringValue(values.Get("principal")),
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rowFilterID(model))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database"), model.Database)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("table"), model.Table)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal"), model.Principal)...)
}

func (r *rowFilterResource) readIntoState(ctx context.Context, model *rowFilterResourceModel, diags *diag.Diagnostics) bool {
	policy, found, err := lookupPolicy(ctx, r.client, rowFilterSpec(*model))
	if err != nil {
		diags.AddError("Unable to read Paimon row filter", err.Error())

		return false
	}
	if !found {
		return false
	}
	remote := policy.RowFilter.Predicate
	equal, err := equivalentPolicyContent(ctx, r.client, rowFilterSpec(*model), model.Predicate.ValueString(), remote)
	if err != nil {
		diags.AddError("Unable to compare Paimon policy", err.Error())

		return false
	}
	if model.Predicate.IsNull() || model.Predicate.IsUnknown() || !equal {
		model.Predicate = types.StringValue(remote)
	}
	model.ID = types.StringValue(rowFilterID(*model))

	return true
}

func (r *columnMaskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_column_mask"
}

func (r *columnMaskResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manages one principal-scoped column-masking policy on a Paimon table. The table must enable query-auth.enabled. The management API is experimental in Paimon.",
		Attributes: map[string]schema.Attribute{
			"allow_non_atomic_update": nonAtomicPolicyUpdateAttribute(),
			"id": schema.StringAttribute{
				Description:   "Stable URL-query identifier for the column-mask identity.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"database":  schema.StringAttribute{Description: "Database containing the protected table.", Required: true, Validators: nonEmptyStringValidators(), PlanModifiers: replace},
			"table":     schema.StringAttribute{Description: "Protected table name.", Required: true, Validators: nonEmptyStringValidators(), PlanModifiers: replace},
			"principal": schema.StringAttribute{Description: "Opaque canonical principal identifier resolved by the REST server.", Required: true, Validators: principalValidators(), PlanModifiers: replace},
			"column":    schema.StringAttribute{Description: "Protected top-level table column.", Required: true, Validators: nonEmptyStringValidators(), PlanModifiers: replace},
			"transform": schema.StringAttribute{
				Description: "JSON serialization of one Paimon Transform. The server validates and canonicalizes its references and result type against the table schema.",
				Required:    true,
				Validators:  []validator.String{stringvalidator.UTF8LengthAtMost(maxSerializedPolicyBytes)},
			},
		},
	}
}

func (r *columnMaskResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model columnMaskResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() || model.Transform.IsNull() || model.Transform.IsUnknown() {
		return
	}
	validateSerializedPolicy("transform", model.Transform.ValueString(), &resp.Diagnostics)
}

func (r *columnMaskResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	clientFromProviderData(req.ProviderData, &r.client, &resp.Diagnostics, "paimon_column_mask resource")
}

func (r *columnMaskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan columnMaskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateSerializedPolicy("transform", plan.Transform.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	result := createPolicyWithReconciliation(ctx, r.client, columnMaskSpec(plan), false)
	if !result.accepted {
		resp.Diagnostics.AddError("Unable to create Paimon column mask", result.err.Error())

		return
	}
	plan.ID = types.StringValue(columnMaskID(plan))
	if result.err != nil && result.observed != nil && result.observed.ColumnMask != nil && !equivalentJSON(plan.Transform.ValueString(), result.observed.ColumnMask.Transform) {
		plan.Transform = types.StringValue(result.observed.ColumnMask.Transform)
	}
	stableCtx := context.WithoutCancel(ctx)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	if result.warning != "" {
		resp.Diagnostics.AddWarning("Recovered Paimon column-mask creation", result.warning)
	}
	if result.err != nil {
		resp.Diagnostics.AddError("Unable to verify Paimon column mask after creation", result.err.Error())
	}
}

func (r *columnMaskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state columnMaskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.readIntoState(ctx, &state, &resp.Diagnostics) {
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *columnMaskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan columnMaskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateSerializedPolicy("transform", plan.Transform.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	stableCtx := context.WithoutCancel(ctx)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	equal, err := equivalentPolicyContent(ctx, r.client, columnMaskSpec(plan), state.Transform.ValueString(), plan.Transform.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to compare Paimon policy", err.Error())

		return
	}
	if equal {
		plan.ID = types.StringValue(columnMaskID(plan))
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)

		return
	}
	if !plan.AllowNonAtomicUpdate.ValueBool() {
		resp.Diagnostics.AddError("Non-atomic policy update is disabled", nonAtomicPolicyUpdateDetail)

		return
	}
	result := replacePolicyWithReconciliation(ctx, r.client, columnMaskSpec(state), columnMaskSpec(plan))
	if result.desired {
		plan.ID = types.StringValue(columnMaskID(plan))
		if result.err != nil && result.observed != nil && result.observed.ColumnMask != nil && !equivalentJSON(plan.Transform.ValueString(), result.observed.ColumnMask.Transform) {
			plan.Transform = types.StringValue(result.observed.ColumnMask.Transform)
		}
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	}
	if result.warning != "" {
		resp.Diagnostics.AddWarning("Recovered Paimon column-mask replacement", result.warning)
	}
	if result.err != nil {
		resp.Diagnostics.AddError("Unable to replace Paimon column mask", result.err.Error())
	}
}

func (r *columnMaskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state columnMaskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DropPolicy(ctx, state.Database.ValueString(), state.Table.ValueString(), client.PolicyTypeColumnMasking, state.Principal.ValueString(), state.Column.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to drop Paimon column mask", err.Error())
	}
}

func (r *columnMaskResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	values, err := parsePolicyID(req.ID, true)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Paimon column-mask import identifier", err.Error())

		return
	}
	model := columnMaskResourceModel{
		Database:  types.StringValue(values.Get("database")),
		Table:     types.StringValue(values.Get("table")),
		Principal: types.StringValue(values.Get("principal")),
		Column:    types.StringValue(values.Get("column")),
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), columnMaskID(model))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database"), model.Database)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("table"), model.Table)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal"), model.Principal)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("column"), model.Column)...)
}

func (r *columnMaskResource) readIntoState(ctx context.Context, model *columnMaskResourceModel, diags *diag.Diagnostics) bool {
	policy, found, err := lookupPolicy(ctx, r.client, columnMaskSpec(*model))
	if err != nil {
		diags.AddError("Unable to read Paimon column mask", err.Error())

		return false
	}
	if !found {
		return false
	}
	remote := policy.ColumnMask.Transform
	equal, err := equivalentPolicyContent(ctx, r.client, columnMaskSpec(*model), model.Transform.ValueString(), remote)
	if err != nil {
		diags.AddError("Unable to compare Paimon policy", err.Error())

		return false
	}
	if model.Transform.IsNull() || model.Transform.IsUnknown() || !equal {
		model.Transform = types.StringValue(remote)
	}
	model.ID = types.StringValue(columnMaskID(*model))

	return true
}

type policySpec struct {
	database   string
	table      string
	policyType string
	principal  string
	column     string
	content    string
	label      string
	request    client.PolicyRequest
}

func rowFilterSpec(model rowFilterResourceModel) policySpec {
	return policySpec{
		database:   model.Database.ValueString(),
		table:      model.Table.ValueString(),
		policyType: client.PolicyTypeRowFilter,
		principal:  model.Principal.ValueString(),
		content:    model.Predicate.ValueString(),
		label:      "row filter",
		request: client.PolicyRequest{
			RowFilter: &client.RowFilter{Predicate: model.Predicate.ValueString()},
			Principal: model.Principal.ValueString(),
		},
	}
}

func columnMaskSpec(model columnMaskResourceModel) policySpec {
	return policySpec{
		database:   model.Database.ValueString(),
		table:      model.Table.ValueString(),
		policyType: client.PolicyTypeColumnMasking,
		principal:  model.Principal.ValueString(),
		column:     model.Column.ValueString(),
		content:    model.Transform.ValueString(),
		label:      "column mask",
		request: client.PolicyRequest{
			ColumnMask: &client.ColumnMask{OnColumn: model.Column.ValueString(), Transform: model.Transform.ValueString()},
			Principal:  model.Principal.ValueString(),
		},
	}
}

func lookupPolicy(ctx context.Context, api *client.Client, spec policySpec) (client.DataPolicy, bool, error) {
	response, err := api.ListPolicies(ctx, client.ListPoliciesRequest{
		Database:   spec.database,
		Table:      spec.table,
		Type:       spec.policyType,
		Principal:  spec.principal,
		Column:     spec.column,
		MaxResults: 2,
	})
	if client.IsNotFound(err) {
		return client.DataPolicy{}, false, nil
	}
	if err != nil {
		return client.DataPolicy{}, false, err
	}
	matches := make([]client.DataPolicy, 0, len(response.Policies))
	for _, policy := range response.Policies {
		identityMatches := policy.Resource.Type == client.ResourceTypeTable &&
			policy.Resource.Database == spec.database &&
			policy.Resource.Table == spec.table &&
			policy.Principal == spec.principal
		if !identityMatches {
			continue
		}
		switch spec.policyType {
		case client.PolicyTypeRowFilter:
			if policy.RowFilter != nil {
				matches = append(matches, policy)
			}
		case client.PolicyTypeColumnMasking:
			if policy.ColumnMask != nil && policy.ColumnMask.OnColumn == spec.column {
				matches = append(matches, policy)
			}
		}
	}
	if len(matches) == 0 {
		return client.DataPolicy{}, false, nil
	}
	if len(matches) > 1 {
		return client.DataPolicy{}, false, fmt.Errorf("the REST Catalog returned more than one %s for the same identity", spec.label)
	}

	return matches[0], true, nil
}

type policyCreateResult struct {
	accepted bool
	observed *client.DataPolicy
	warning  string
	err      error
}

func createPolicyWithReconciliation(ctx context.Context, api *client.Client, spec policySpec, alreadyManaged bool) policyCreateResult {
	createErr := api.CreatePolicy(ctx, spec.database, spec.table, spec.request)
	if createErr != nil && !alreadyManaged && !client.IsMutationOutcomeUncertain(createErr) {
		return policyCreateResult{err: fmt.Errorf("creating the %s was rejected: %w. Import an existing policy explicitly; it has not been adopted into state", spec.label, createErr)}
	}
	recoveryCtx, cancel := mutationRecoveryContext(ctx, api)
	defer cancel()
	observation, found, converged, reconcileErr := retryLookupUntil(recoveryCtx, func(attemptCtx context.Context) (policyLookupObservation, bool, error) {
		observation, err := observePolicy(attemptCtx, api, spec)

		return observation, observation.found, err
	}, func(observation policyLookupObservation) bool { return observation.matches })
	observed := observation.policy
	if createErr == nil {
		result := policyCreateResult{accepted: true}
		if reconcileErr != nil {
			result.err = fmt.Errorf("the REST Catalog accepted the %s, but bounded reconciliation failed: %w. Terraform retained the planned identity in state", spec.label, reconcileErr)

			return result
		}
		if !found {
			result.err = fmt.Errorf("the REST Catalog accepted the %s but did not return it during bounded reconciliation. Terraform retained the planned identity in state", spec.label)

			return result
		}
		result.observed = &observed
		if !converged {
			result.err = fmt.Errorf("the REST Catalog accepted the %s but returned non-equivalent policy content during reconciliation", spec.label)
		}

		return result
	}
	if reconcileErr != nil {
		return policyCreateResult{err: fmt.Errorf("creating the %s failed (%s), and bounded reconciliation could not establish the remote state: %w", spec.label, createErr, reconcileErr)}
	}
	if !found {
		return policyCreateResult{err: fmt.Errorf("creating the %s failed, and bounded reconciliation confirmed that the policy is absent: %w", spec.label, createErr)}
	}
	if !converged {
		return policyCreateResult{observed: &observed, err: fmt.Errorf("creating the %s failed (%s), and the same identity exists with different policy content", spec.label, createErr)}
	}

	return policyCreateResult{
		accepted: true,
		observed: &observed,
		warning:  fmt.Sprintf("The create request returned an error, but bounded reconciliation found the exact %s that Terraform planned, so the resource was adopted into state.", spec.label),
	}
}

type policyReplacementResult struct {
	desired  bool
	observed *client.DataPolicy
	warning  string
	err      error
}

type policyLookupObservation struct {
	policy  client.DataPolicy
	found   bool
	matches bool
}

func replacePolicyWithReconciliation(ctx context.Context, api *client.Client, previous, desired policySpec) policyReplacementResult {
	mutationCtx := ctx
	dropErr := api.DropPolicy(ctx, desired.database, desired.table, desired.policyType, desired.principal, desired.column)
	if dropErr != nil && !client.IsNotFound(dropErr) {
		recoveryCtx, cancel := mutationRecoveryContext(ctx, api)
		defer cancel()
		observation, _, _, reconcileErr := retryLookupUntil(recoveryCtx, func(attemptCtx context.Context) (policyLookupObservation, bool, error) {
			observation, err := observePolicy(attemptCtx, api, desired)

			return observation, true, err
		}, func(observed policyLookupObservation) bool {
			return !observed.found || observed.matches
		})
		if reconcileErr != nil {
			return policyReplacementResult{err: fmt.Errorf("dropping the previous %s returned an error (%s), and bounded reconciliation could not establish the remote state: %w", previous.label, dropErr, reconcileErr)}
		}
		if observation.found {
			observed := observation.policy
			previousMatches, err := previous.matchesWithSchema(recoveryCtx, api, observed)
			if err != nil {
				return policyReplacementResult{err: err}
			}
			switch {
			case observation.matches:
				return policyReplacementResult{
					desired:  true,
					observed: &observed,
					warning:  fmt.Sprintf("Dropping the previous %s returned an error, but reconciliation found the exact replacement already attached.", previous.label),
				}
			case previousMatches:
				return policyReplacementResult{observed: &observed, err: fmt.Errorf("dropping the previous %s failed and reconciliation confirmed that it remains attached: %w", previous.label, dropErr)}
			default:
				return policyReplacementResult{observed: &observed, err: fmt.Errorf("dropping the previous %s returned an error (%s), and reconciliation found unexpected policy content for the same identity", previous.label, dropErr)}
			}
		}
		mutationCtx = context.WithoutCancel(ctx)
	}

	created := createPolicyWithReconciliation(mutationCtx, api, desired, true)
	if created.accepted {
		return policyReplacementResult{desired: true, observed: created.observed, warning: created.warning, err: created.err}
	}
	if created.observed != nil {
		matches, err := previous.matchesWithSchema(ctx, api, *created.observed)
		if err != nil {
			return policyReplacementResult{observed: created.observed, err: err}
		}
		if matches {
			return policyReplacementResult{observed: created.observed, err: fmt.Errorf("creating the replacement failed, but reconciliation confirmed that the previous %s remains attached: %w", previous.label, created.err)}
		}

		return policyReplacementResult{observed: created.observed, err: fmt.Errorf("creating the replacement failed, and reconciliation found unexpected policy content for the same identity: %w", created.err)}
	}

	recoveryCtx, cancel := mutationRecoveryContext(ctx, api)
	defer cancel()
	restored := createPolicyWithReconciliation(recoveryCtx, api, previous, true)
	if restored.accepted {
		detail := fmt.Sprintf("creating the replacement failed (%s), so the previous %s was restored", created.err, previous.label)
		if restored.err != nil {
			detail += fmt.Sprintf(", but verification of the restored policy also failed (%s)", restored.err)
		}

		return policyReplacementResult{observed: restored.observed, err: fmt.Errorf("%s", detail)}
	}
	restoredDesired := false
	if restored.observed != nil {
		var err error
		restoredDesired, err = desired.matchesWithSchema(recoveryCtx, api, *restored.observed)
		if err != nil {
			return policyReplacementResult{observed: restored.observed, err: err}
		}
	}
	if restoredDesired {
		return policyReplacementResult{
			desired:  true,
			observed: restored.observed,
			warning:  fmt.Sprintf("The replacement request returned an error and restoring the previous %s conflicted, but reconciliation found the exact replacement attached.", previous.label),
		}
	}

	return policyReplacementResult{err: fmt.Errorf("creating the replacement failed (%s), and restoring the previous %s also failed (%s). The principal may currently have no %s", created.err, previous.label, restored.err, previous.label)}
}

func validateSerializedPolicy(name, value string, diags *diag.Diagnostics) {
	if value == "" {
		diags.AddError("Invalid Paimon policy JSON", name+" cannot be empty.")

		return
	}
	if len([]byte(value)) > maxSerializedPolicyBytes {
		diags.AddError("Invalid Paimon policy JSON", name+" must not exceed 60 KiB in UTF-8.")

		return
	}
	if !json.Valid([]byte(value)) {
		diags.AddError("Invalid Paimon policy JSON", name+" must contain a valid serialized Paimon JSON value.")
	}
}

func rowFilterID(model rowFilterResourceModel) string {
	return policyID(model.Database.ValueString(), model.Table.ValueString(), model.Principal.ValueString(), "")
}

func columnMaskID(model columnMaskResourceModel) string {
	return policyID(model.Database.ValueString(), model.Table.ValueString(), model.Principal.ValueString(), model.Column.ValueString())
}

func policyID(database, table, principal, column string) string {
	values := make(url.Values)
	values.Set("database", database)
	values.Set("table", table)
	values.Set("principal", principal)
	setIdentityValue(values, "column", column)

	return values.Encode()
}

func parsePolicyID(id string, columnRequired bool) (url.Values, error) {
	allowed := []string{"database", "table", "principal"}
	required := []string{"database", "table", "principal"}
	if columnRequired {
		allowed = append(allowed, "column")
		required = append(required, "column")
	}
	values, err := parseIdentityQuery(id, allowed, required)
	if err != nil {
		return nil, err
	}
	if err := validateManagementPrincipal(values.Get("principal")); err != nil {
		return nil, err
	}

	return values, nil
}
