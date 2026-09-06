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
	"net/url"
	"regexp"
	"sort"
	"time"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &permissionResource{}
	_ resource.ResourceWithImportState    = &permissionResource{}
	_ resource.ResourceWithValidateConfig = &permissionResource{}
)

var permissionResourceTypes = []string{
	client.ResourceTypeCatalog,
	client.ResourceTypeCatalogAll,
	client.ResourceTypeDatabase,
	client.ResourceTypeDatabaseAll,
	client.ResourceTypeTable,
	client.ResourceTypeColumn,
	client.ResourceTypeFunction,
	client.ResourceTypeView,
}

var permissionAccesses = []string{
	client.PermissionAccessAll,
	client.PermissionAccessCreateDatabase,
	client.PermissionAccessDescribe,
	client.PermissionAccessAlter,
	client.PermissionAccessDrop,
	client.PermissionAccessCreateTable,
	client.PermissionAccessCreateFunction,
	client.PermissionAccessCreateView,
	client.PermissionAccessList,
	client.PermissionAccessSelect,
	client.PermissionAccessUpdate,
	client.PermissionAccessGrant,
}

var permissionAccessesByResource = map[string]map[string]bool{
	client.ResourceTypeCatalog:     accessSet(client.PermissionAccessAll, client.PermissionAccessAlter, client.PermissionAccessDrop, client.PermissionAccessGrant, client.PermissionAccessCreateDatabase),
	client.ResourceTypeCatalogAll:  accessSet(client.PermissionAccessAll, client.PermissionAccessDescribe, client.PermissionAccessAlter, client.PermissionAccessDrop, client.PermissionAccessGrant, client.PermissionAccessCreateTable, client.PermissionAccessCreateView, client.PermissionAccessCreateFunction, client.PermissionAccessList, client.PermissionAccessSelect, client.PermissionAccessUpdate),
	client.ResourceTypeDatabase:    accessSet(client.PermissionAccessAll, client.PermissionAccessDescribe, client.PermissionAccessAlter, client.PermissionAccessDrop, client.PermissionAccessGrant, client.PermissionAccessCreateTable, client.PermissionAccessCreateView, client.PermissionAccessCreateFunction, client.PermissionAccessList),
	client.ResourceTypeDatabaseAll: accessSet(client.PermissionAccessAll, client.PermissionAccessSelect, client.PermissionAccessUpdate, client.PermissionAccessAlter, client.PermissionAccessDrop, client.PermissionAccessGrant),
	client.ResourceTypeTable:       accessSet(client.PermissionAccessAll, client.PermissionAccessSelect, client.PermissionAccessUpdate, client.PermissionAccessAlter, client.PermissionAccessDrop, client.PermissionAccessGrant),
	client.ResourceTypeColumn:      accessSet(client.PermissionAccessSelect),
	client.ResourceTypeView:        accessSet(client.PermissionAccessAll, client.PermissionAccessSelect, client.PermissionAccessAlter, client.PermissionAccessDrop, client.PermissionAccessGrant),
	client.ResourceTypeFunction:    accessSet(client.PermissionAccessAll, client.PermissionAccessSelect, client.PermissionAccessAlter, client.PermissionAccessDrop, client.PermissionAccessGrant),
}

var expireTimePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?[Zz]$`)

type permissionResource struct {
	client *client.Client
}

type permissionResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	ResourceType        types.String `tfsdk:"resource_type"`
	Database            types.String `tfsdk:"database"`
	Table               types.String `tfsdk:"table"`
	Function            types.String `tfsdk:"function"`
	View                types.String `tfsdk:"view"`
	Access              types.String `tfsdk:"access"`
	Principal           types.String `tfsdk:"principal"`
	ColumnNames         types.Set    `tfsdk:"column_names"`
	ExcludedColumnNames types.Set    `tfsdk:"excluded_column_names"`
	ExpireTime          types.String `tfsdk:"expire_time"`
}

func NewPermissionResource() resource.Resource {
	return &permissionResource{}
}

func (r *permissionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permission"
}

func (r *permissionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	nonEmptyOptional := nonEmptyStringValidators()
	columnValidators := []validator.Set{
		setvalidator.SizeAtLeast(1),
		setvalidator.NoNullValues(),
		setvalidator.ValueStringsAre(nonBlankStringValidator{}),
	}
	resp.Schema = schema.Schema{
		Description: "Manages one direct permission assignment in a Paimon REST Catalog. The management API is experimental in Paimon.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Stable URL-query identifier for the permission identity.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"resource_type": schema.StringAttribute{
				Description:   "Permission resource type: CATALOG, CATALOG_ALL, DATABASE, DATABASE_ALL, TABLE, COLUMN, FUNCTION, or VIEW.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.OneOf(permissionResourceTypes...)},
				PlanModifiers: replace,
			},
			"database": schema.StringAttribute{
				Description:   "Database locator. Required by DATABASE, DATABASE_ALL, TABLE, COLUMN, FUNCTION, and VIEW.",
				Optional:      true,
				Validators:    nonEmptyOptional,
				PlanModifiers: replace,
			},
			"table": schema.StringAttribute{
				Description:   "Table locator. Required only by TABLE and COLUMN.",
				Optional:      true,
				Validators:    nonEmptyOptional,
				PlanModifiers: replace,
			},
			"function": schema.StringAttribute{
				Description:   "Function locator. Required only by FUNCTION.",
				Optional:      true,
				Validators:    nonEmptyOptional,
				PlanModifiers: replace,
			},
			"view": schema.StringAttribute{
				Description:   "View locator. Required only by VIEW.",
				Optional:      true,
				Validators:    nonEmptyOptional,
				PlanModifiers: replace,
			},
			"access": schema.StringAttribute{
				Description:   "Canonical upper-case Paimon access name. Applicability is validated against resource_type.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.OneOf(permissionAccesses...)},
				PlanModifiers: replace,
			},
			"principal": schema.StringAttribute{
				Description:   "Opaque canonical principal identifier resolved by the REST server.",
				Required:      true,
				Validators:    principalValidators(),
				PlanModifiers: replace,
			},
			"column_names": schema.SetAttribute{
				Description: "Allowlist of columns. Exactly one of column_names and excluded_column_names is required for COLUMN and forbidden for other resource types.",
				Optional:    true,
				ElementType: types.StringType,
				Validators:  columnValidators,
			},
			"excluded_column_names": schema.SetAttribute{
				Description: "Denylist of columns. Exactly one of column_names and excluded_column_names is required for COLUMN and forbidden for other resource types.",
				Optional:    true,
				ElementType: types.StringType,
				Validators:  columnValidators,
			},
			"expire_time": schema.StringAttribute{
				Description: "Optional exclusive authorization upper bound as a UTC ISO-8601 instant with a Z suffix. Fractional digits are accepted only when the parsed instant resolves exactly to milliseconds; equivalent spellings are normalized to the canonical wire format.",
				Optional:    true,
			},
		},
	}
}

func (r *permissionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model permissionResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() || permissionIdentityUnknown(model) {
		return
	}
	validatePermissionModel(model, &resp.Diagnostics)
}

func (r *permissionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	clientFromProviderData(req.ProviderData, &r.client, &resp.Diagnostics, "paimon_permission resource")
}

func (r *permissionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan permissionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validatePermissionModel(plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	assignment := permissionAssignmentFromModel(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.GrantPermission(ctx, assignment); err != nil {
		observed, recovered, recoveryErr := r.reconcileFailedGrant(ctx, plan, assignment, err)
		if !recovered {
			resp.Diagnostics.AddError("Unable to grant Paimon permission", recoveryErr.Error())

			return
		}
		stableCtx := context.WithoutCancel(ctx)
		setPermissionModel(stableCtx, &plan, observed, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
		resp.Diagnostics.AddWarning("Recovered Paimon permission grant", "The grant request returned an error, but bounded reconciliation found the exact permission assignment that Terraform planned, so the resource was adopted into state.")

		return
	}
	plan.ID = types.StringValue(permissionID(plan))
	stableCtx := context.WithoutCancel(ctx)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.readAfterMutation(ctx, &plan, assignment, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	}
}

func (r *permissionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state permissionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found := r.readIntoState(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)

		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *permissionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan permissionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validatePermissionModel(plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	assignment := permissionAssignmentFromModel(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	stableCtx := context.WithoutCancel(ctx)
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.GrantPermission(ctx, assignment); err != nil {
		observed, recovered, recoveryErr := r.reconcileFailedGrant(ctx, plan, assignment, err)
		if !recovered {
			resp.Diagnostics.AddError("Unable to update Paimon permission", recoveryErr.Error())

			return
		}
		setPermissionModel(stableCtx, &plan, observed, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
		resp.Diagnostics.AddWarning("Recovered Paimon permission update", "The grant-or-replace request returned an error, but bounded reconciliation found the exact permission assignment that Terraform planned.")

		return
	}
	plan.ID = types.StringValue(permissionID(plan))
	resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.readAfterMutation(ctx, &plan, assignment, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(stableCtx, &plan)...)
	}
}

func (r *permissionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state permissionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resourceLocator := permissionLocatorFromModel(state)
	if err := r.client.RevokePermission(ctx, resourceLocator, state.Access.ValueString(), state.Principal.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to revoke Paimon permission", err.Error())
	}
}

func (r *permissionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	model, err := parsePermissionID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Paimon permission import identifier", err.Error())

		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), permissionID(model))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_type"), model.ResourceType)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database"), model.Database)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("table"), model.Table)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("function"), model.Function)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("view"), model.View)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("access"), model.Access)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal"), model.Principal)...)
}

func (r *permissionResource) readIntoState(ctx context.Context, model *permissionResourceModel, diags *diag.Diagnostics) bool {
	assignment, found, err := r.lookup(ctx, *model)
	if err != nil {
		diags.AddError("Unable to read Paimon permission", err.Error())

		return false
	}
	if !found {
		return false
	}
	setPermissionModel(ctx, model, assignment, diags)

	return !diags.HasError()
}

func (r *permissionResource) readAfterMutation(ctx context.Context, model *permissionResourceModel, expected client.PermissionAssignment, diags *diag.Diagnostics) bool {
	recoveryCtx, cancel := mutationRecoveryContext(ctx, r.client)
	defer cancel()
	assignment, found, converged, err := retryLookupUntil(recoveryCtx, func(attemptCtx context.Context) (client.PermissionAssignment, bool, error) {
		return r.lookup(attemptCtx, *model)
	}, func(observed client.PermissionAssignment) bool {
		return permissionAssignmentsEquivalent(expected, observed)
	})
	if err != nil {
		diags.AddError("Unable to verify Paimon permission after mutation", fmt.Sprintf("The permission mutation was accepted, but reconciliation failed: %s. Terraform retained the planned identity in state.", err))

		return false
	}
	if !found {
		diags.AddError("Unable to verify Paimon permission after mutation", "The REST Catalog accepted the permission mutation but did not return the assignment during bounded reconciliation. Terraform retained the planned identity in state.")

		return false
	}
	if !converged {
		diags.AddError("Unable to verify Paimon permission after mutation", "The REST Catalog accepted the permission mutation but the assignment did not converge to the planned attributes during bounded reconciliation. Terraform retained the planned identity in state.")

		return false
	}
	setPermissionModel(recoveryCtx, model, assignment, diags)

	return !diags.HasError()
}

func (r *permissionResource) reconcileFailedGrant(ctx context.Context, model permissionResourceModel, expected client.PermissionAssignment, grantErr error) (client.PermissionAssignment, bool, error) {
	recoveryCtx, cancel := mutationRecoveryContext(ctx, r.client)
	defer cancel()
	observed, found, converged, reconcileErr := retryLookupUntil(recoveryCtx, func(attemptCtx context.Context) (client.PermissionAssignment, bool, error) {
		return r.lookup(attemptCtx, model)
	}, func(observed client.PermissionAssignment) bool {
		return permissionAssignmentsEquivalent(expected, observed)
	})
	if reconcileErr != nil {
		return client.PermissionAssignment{}, false, fmt.Errorf("granting the permission failed (%s), and bounded reconciliation could not establish the remote state: %w", grantErr, reconcileErr)
	}
	if !found {
		return client.PermissionAssignment{}, false, fmt.Errorf("granting the permission failed, and bounded reconciliation confirmed that the assignment is absent: %w", grantErr)
	}
	if !converged {
		return client.PermissionAssignment{}, false, fmt.Errorf("granting the permission failed (%s), and the same identity exists with different permission attributes", grantErr)
	}

	return observed, true, nil
}

func (r *permissionResource) lookup(ctx context.Context, model permissionResourceModel) (client.PermissionAssignment, bool, error) {
	response, err := r.client.ListPermissions(ctx, client.ListPermissionsRequest{
		Resource:   permissionLocatorFromModel(model),
		Principal:  model.Principal.ValueString(),
		Access:     model.Access.ValueString(),
		MaxResults: 2,
	})
	if client.IsNotFound(err) {
		return client.PermissionAssignment{}, false, nil
	}
	if err != nil {
		return client.PermissionAssignment{}, false, err
	}
	matches := make([]client.PermissionAssignment, 0, len(response.Permissions))
	for _, assignment := range response.Permissions {
		if permissionIdentityMatches(model, assignment) {
			matches = append(matches, assignment)
		}
	}
	if len(matches) == 0 {
		return client.PermissionAssignment{}, false, nil
	}
	if len(matches) > 1 {
		return client.PermissionAssignment{}, false, errors.New("the REST Catalog returned more than one assignment for the same resource, access, and principal identity")
	}

	return matches[0], true, nil
}

func permissionAssignmentFromModel(ctx context.Context, model permissionResourceModel, diags *diag.Diagnostics) client.PermissionAssignment {
	assignment := client.PermissionAssignment{
		Resource:   permissionLocatorFromModel(model),
		Access:     model.Access.ValueString(),
		Principal:  model.Principal.ValueString(),
		ExpireTime: optionalStringPointer(model.ExpireTime),
	}
	if assignment.ExpireTime != nil {
		canonical, err := canonicalPermissionExpireTime(*assignment.ExpireTime)
		if err != nil {
			diags.AddError("Invalid Paimon permission expiry", err.Error())
		} else {
			assignment.ExpireTime = &canonical
		}
	}
	if model.ResourceType.ValueString() == client.ResourceTypeColumn {
		assignment.Columns = &client.PermissionColumns{}
		if !model.ColumnNames.IsNull() {
			assignment.Columns.ColumnNames = stringSetFromValue(ctx, model.ColumnNames, diags)
			sort.Strings(assignment.Columns.ColumnNames)
		} else {
			assignment.Columns.ExcludedColumnNames = stringSetFromValue(ctx, model.ExcludedColumnNames, diags)
			sort.Strings(assignment.Columns.ExcludedColumnNames)
		}
	}

	return assignment
}

func permissionLocatorFromModel(model permissionResourceModel) client.PermissionResource {
	return client.PermissionResource{
		Type:     model.ResourceType.ValueString(),
		Database: knownString(model.Database),
		Table:    knownString(model.Table),
		Function: knownString(model.Function),
		View:     knownString(model.View),
	}
}

func setPermissionModel(ctx context.Context, model *permissionResourceModel, assignment client.PermissionAssignment, diags *diag.Diagnostics) {
	model.ResourceType = types.StringValue(assignment.Resource.Type)
	model.Database = optionalStringValue(assignment.Resource.Database)
	model.Table = optionalStringValue(assignment.Resource.Table)
	model.Function = optionalStringValue(assignment.Resource.Function)
	model.View = optionalStringValue(assignment.Resource.View)
	model.Access = types.StringValue(assignment.Access)
	model.Principal = types.StringValue(assignment.Principal)
	model.ColumnNames = types.SetNull(types.StringType)
	model.ExcludedColumnNames = types.SetNull(types.StringType)
	if assignment.Columns != nil {
		model.ColumnNames = stringSetValue(ctx, assignment.Columns.ColumnNames, diags)
		model.ExcludedColumnNames = stringSetValue(ctx, assignment.Columns.ExcludedColumnNames, diags)
	}
	configuredExpireTime := model.ExpireTime
	model.ExpireTime = stringValueFromPointer(assignment.ExpireTime)
	if assignment.ExpireTime != nil && !configuredExpireTime.IsNull() && !configuredExpireTime.IsUnknown() {
		configured, configuredErr := parsePermissionExpireTime(configuredExpireTime.ValueString())
		remote, remoteErr := parsePermissionExpireTime(*assignment.ExpireTime)
		if configuredErr == nil && remoteErr == nil && configured.Equal(remote) {
			model.ExpireTime = configuredExpireTime
		}
	}
	model.ID = types.StringValue(permissionID(*model))
}

func validatePermissionModel(model permissionResourceModel, diags *diag.Diagnostics) {
	resourceType := model.ResourceType.ValueString()
	database := knownString(model.Database)
	table := knownString(model.Table)
	function := knownString(model.Function)
	view := knownString(model.View)
	validLocator := false
	switch resourceType {
	case client.ResourceTypeCatalog, client.ResourceTypeCatalogAll:
		validLocator = database == "" && table == "" && function == "" && view == ""
	case client.ResourceTypeDatabase, client.ResourceTypeDatabaseAll:
		validLocator = database != "" && table == "" && function == "" && view == ""
	case client.ResourceTypeTable, client.ResourceTypeColumn:
		validLocator = database != "" && table != "" && function == "" && view == ""
	case client.ResourceTypeFunction:
		validLocator = database != "" && table == "" && function != "" && view == ""
	case client.ResourceTypeView:
		validLocator = database != "" && table == "" && function == "" && view != ""
	}
	if !validLocator {
		diags.AddError("Invalid Paimon permission resource locator", fmt.Sprintf("resource_type %s has an invalid combination of database, table, function, and view. See the attribute descriptions for the exact locator shape.", resourceType))
	}
	if accesses, ok := permissionAccessesByResource[resourceType]; ok && !accesses[model.Access.ValueString()] {
		diags.AddError("Invalid Paimon permission access", fmt.Sprintf("access %s is not valid for resource_type %s.", model.Access.ValueString(), resourceType))
	}
	if !model.ColumnNames.IsUnknown() && !model.ExcludedColumnNames.IsUnknown() {
		included := !model.ColumnNames.IsNull()
		excluded := !model.ExcludedColumnNames.IsNull()
		if resourceType == client.ResourceTypeColumn && included == excluded {
			diags.AddError("Invalid Paimon column permission", "A COLUMN permission must configure exactly one of column_names and excluded_column_names.")
		}
		if resourceType != client.ResourceTypeColumn && (included || excluded) {
			diags.AddError("Invalid Paimon permission columns", "column_names and excluded_column_names are valid only for resource_type COLUMN.")
		}
	}
	if !model.ExpireTime.IsNull() && !model.ExpireTime.IsUnknown() {
		if _, err := parsePermissionExpireTime(model.ExpireTime.ValueString()); err != nil {
			diags.AddError("Invalid Paimon permission expiry", err.Error())
		}
	}
}

func parsePermissionExpireTime(value string) (time.Time, error) {
	if !expireTimePattern.MatchString(value) {
		return time.Time{}, errors.New("expire_time must be a UTC ISO-8601 instant ending in Z with no more than nine fractional-second digits")
	}

	normalized := []byte(value)
	normalized[10] = 'T'
	normalized[len(normalized)-1] = 'Z'
	instant, err := time.Parse(time.RFC3339Nano, string(normalized))
	if err != nil {
		return time.Time{}, fmt.Errorf("expire_time is not a valid instant: %s", err)
	}
	if instant.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, errors.New("expire_time must resolve exactly to millisecond precision")
	}

	return instant, nil
}

func canonicalPermissionExpireTime(value string) (string, error) {
	instant, err := parsePermissionExpireTime(value)
	if err != nil {
		return "", err
	}

	return instant.UTC().Format(time.RFC3339Nano), nil
}

func permissionIdentityUnknown(model permissionResourceModel) bool {
	return model.ResourceType.IsUnknown() || model.Database.IsUnknown() || model.Table.IsUnknown() || model.Function.IsUnknown() || model.View.IsUnknown() || model.Access.IsUnknown() || model.Principal.IsUnknown()
}

func permissionIdentityMatches(model permissionResourceModel, assignment client.PermissionAssignment) bool {
	return assignment.Resource == permissionLocatorFromModel(model) && assignment.Access == model.Access.ValueString() && assignment.Principal == model.Principal.ValueString()
}

func permissionAssignmentsEquivalent(expected, observed client.PermissionAssignment) bool {
	if expected.Resource != observed.Resource || expected.Access != observed.Access || expected.Principal != observed.Principal {
		return false
	}
	if (expected.Columns == nil) != (observed.Columns == nil) {
		return false
	}
	if expected.Columns != nil && (!sameStringSet(expected.Columns.ColumnNames, observed.Columns.ColumnNames) || !sameStringSet(expected.Columns.ExcludedColumnNames, observed.Columns.ExcludedColumnNames)) {
		return false
	}
	if (expected.ExpireTime == nil) != (observed.ExpireTime == nil) {
		return false
	}
	if expected.ExpireTime == nil {
		return true
	}
	expectedTime, expectedErr := parsePermissionExpireTime(*expected.ExpireTime)
	observedTime, observedErr := parsePermissionExpireTime(*observed.ExpireTime)
	if expectedErr == nil && observedErr == nil {
		return expectedTime.Equal(observedTime)
	}

	return *expected.ExpireTime == *observed.ExpireTime
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}

	return true
}

func permissionID(model permissionResourceModel) string {
	values := make(url.Values)
	values.Set("resource_type", model.ResourceType.ValueString())
	setIdentityValue(values, "database", knownString(model.Database))
	setIdentityValue(values, "table", knownString(model.Table))
	setIdentityValue(values, "function", knownString(model.Function))
	setIdentityValue(values, "view", knownString(model.View))
	values.Set("access", model.Access.ValueString())
	values.Set("principal", model.Principal.ValueString())

	return values.Encode()
}

func parsePermissionID(id string) (permissionResourceModel, error) {
	values, err := parseIdentityQuery(id, []string{"resource_type", "database", "table", "function", "view", "access", "principal"}, []string{"resource_type", "access", "principal"})
	if err != nil {
		return permissionResourceModel{}, err
	}
	model := permissionResourceModel{
		ResourceType:        types.StringValue(values.Get("resource_type")),
		Database:            optionalStringValue(values.Get("database")),
		Table:               optionalStringValue(values.Get("table")),
		Function:            optionalStringValue(values.Get("function")),
		View:                optionalStringValue(values.Get("view")),
		Access:              types.StringValue(values.Get("access")),
		Principal:           types.StringValue(values.Get("principal")),
		ColumnNames:         types.SetUnknown(types.StringType),
		ExcludedColumnNames: types.SetUnknown(types.StringType),
	}
	if err := validateManagementPrincipal(model.Principal.ValueString()); err != nil {
		return permissionResourceModel{}, err
	}
	var diags diag.Diagnostics
	validatePermissionModel(model, &diags)
	if diags.HasError() {
		return permissionResourceModel{}, fmt.Errorf("%s: %s", diags.Errors()[0].Summary(), diags.Errors()[0].Detail())
	}

	return model, nil
}

func parseIdentityQuery(id string, allowed, required []string) (url.Values, error) {
	values, err := url.ParseQuery(id)
	if err != nil {
		return nil, fmt.Errorf("parse URL-query identifier: %w", err)
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	for name, entries := range values {
		if !allowedSet[name] {
			return nil, fmt.Errorf("identifier contains unsupported key %q", name)
		}
		if len(entries) != 1 {
			return nil, fmt.Errorf("identifier key %q must appear exactly once", name)
		}
		if isManagementBlank(entries[0]) {
			return nil, fmt.Errorf("identifier key %q cannot be empty", name)
		}
	}
	for _, name := range required {
		if isManagementBlank(values.Get(name)) {
			return nil, fmt.Errorf("identifier must contain non-empty key %q", name)
		}
	}

	return values, nil
}

func optionalStringValue(value string) types.String {
	if value == "" {
		return types.StringNull()
	}

	return types.StringValue(value)
}

func setIdentityValue(values url.Values, name, value string) {
	if value != "" {
		values.Set(name, value)
	}
}

func accessSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}

	return result
}
