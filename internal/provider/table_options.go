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
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var immutableTableOptions = map[string]struct{}{
	"aggregation.remove-record-on-delete":            {},
	"blob-descriptor-field":                          {},
	"blob-field":                                     {},
	"blob-view-field":                                {},
	"bucket-function.type":                           {},
	"bucket-key":                                     {},
	"data-evolution.enabled":                         {},
	"data-file.path-directory":                       {},
	"dynamic-bucket.initial-buckets":                 {},
	"force-lookup":                                   {},
	"index-file-in-data-file-dir":                    {},
	"merge-engine":                                   {},
	"partial-update.remove-record-on-delete":         {},
	"partial-update.remove-record-on-sequence-group": {},
	"partition":                                      {},
	"pk-clustering-override":                         {},
	"primary-key":                                    {},
	"primary-key.nullable":                           {},
	"row-tracking.enabled":                           {},
	"rowkind.field":                                  {},
	"sequence.snapshot-ordering":                     {},
	"type":                                           {},
	"video-frame-field":                              {},
}

type reservedTableOptionsValidator struct{}

func (reservedTableOptionsValidator) Description(context.Context) string {
	return "must configure partitions with partition_keys"
}

func (reservedTableOptionsValidator) MarkdownDescription(context.Context) string {
	return "must configure partitions with `partition_keys`"
}

func (reservedTableOptionsValidator) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	reserved := make([]string, 0, 2)
	for _, key := range []string{"partition"} {
		if _, exists := req.ConfigValue.Elements()[key]; exists {
			reserved = append(reserved, key)
		}
	}
	if len(reserved) > 0 {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Reserved Paimon table option",
			"Do not configure "+strings.Join(reserved, ", ")+" in options. Use partition_keys instead; primary keys use options[\"primary-key\"].",
		)
	}
}

func immutableTableOptionsRequiresReplace(ctx context.Context, req planmodifier.MapRequest, resp *mapplanmodifier.RequiresReplaceIfFuncResponse) {
	var remote types.Map
	var allowed types.Bool
	var keys types.List
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("server_options"), &remote)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("allow_replacement"), &allowed)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("primary_keys"), &keys)...)
	before := effectiveManagedTableOptions(req.StateValue, req.PlanValue, tableOptionsWithPrimaryKeys(ctx, remote, keys, &resp.Diagnostics))
	if immutableTableOptionsChanged(before, req.PlanValue) {
		if !allowed.ValueBool() {
			resp.Diagnostics.AddAttributeError(req.Path, "Destructive table change is disabled", "Changing an immutable option requires dropping and recreating the table and can delete its data. Use a data migration, or explicitly set allow_replacement = true to permit replacement.")

			return
		}
		resp.RequiresReplace = true
	}
}

// Starting to manage an existing option is not a remote change. Only include
// newly managed keys so unrelated server options never become removal targets.
func effectiveManagedTableOptions(before, after, remote types.Map) types.Map {
	if before.IsUnknown() || after.IsUnknown() || remote.IsUnknown() {
		return before
	}
	values := before.Elements()
	remoteValues := remote.Elements()
	for key := range after.Elements() {
		if _, managed := values[key]; !managed {
			if value, exists := remoteValues[key]; exists {
				values[key] = value
			}
		}
	}

	return types.MapValueMust(types.StringType, values)
}

func immutableTableOptionsChanged(before, after types.Map) bool {
	for key := range immutableTableOptions {
		beforeValue, beforeExists, beforeKnown := knownTableOption(before, key)
		afterValue, afterExists, afterKnown := knownTableOption(after, key)
		if !beforeKnown || !afterKnown {
			continue
		}
		if key == "primary-key" {
			if !slices.Equal(parsePrimaryKeyOption(beforeValue), parsePrimaryKeyOption(afterValue)) {
				return true
			}

			continue
		}
		if key == "type" {
			if !beforeExists {
				beforeValue = "table"
			}
			if !afterExists {
				afterValue = "table"
			}
			if !strings.EqualFold(beforeValue, afterValue) {
				return true
			}

			continue
		}
		if beforeExists != afterExists || beforeExists && beforeValue != afterValue {
			return true
		}
	}

	return false
}

func knownTableOption(options types.Map, key string) (string, bool, bool) {
	if options.IsUnknown() {
		return "", false, false
	}
	if options.IsNull() {
		return "", false, true
	}
	element, exists := options.Elements()[key]
	if !exists {
		return "", false, true
	}
	value, ok := element.(types.String)
	if !ok || value.IsNull() || value.IsUnknown() {
		return "", true, false
	}

	return value.ValueString(), true, true
}

func diffTableOptions(before, after map[string]string) ([]string, map[string]string) {
	filteredBefore := make(map[string]string, len(before))
	filteredAfter := make(map[string]string, len(after))
	for key, value := range before {
		if key != "primary-key" {
			filteredBefore[key] = value
		}
	}
	for key, value := range after {
		if key != "primary-key" {
			filteredAfter[key] = value
		}
	}
	removals, updates := diffOptions(filteredBefore, filteredAfter)
	beforeType, beforeExists := before["type"]
	if !beforeExists {
		beforeType = "table"
	}
	afterType, afterExists := after["type"]
	if !afterExists {
		afterType = "table"
	}
	if strings.EqualFold(beforeType, afterType) {
		delete(updates, "type")
		for index, key := range removals {
			if key == "type" {
				removals = append(removals[:index], removals[index+1:]...)

				break
			}
		}
	}

	return removals, updates
}

func syncManagedTableOptions(ctx context.Context, managed types.Map, remote map[string]string, diags *diag.Diagnostics) types.Map {
	synced := syncManagedOptions(ctx, managed, remote, diags)
	if managed.IsNull() || managed.IsUnknown() || diags.HasError() {
		return synced
	}
	synced = retainPrimaryKeyOptionSpelling(managed, synced, remote)
	managedOptions := mapFromValue(ctx, managed, diags)
	configuredType, managesType := managedOptions["type"]
	if !managesType || diags.HasError() {
		return synced
	}
	remoteType, hasRemoteType := remote["type"]
	if !hasRemoteType {
		remoteType = "table"
	}
	if !strings.EqualFold(configuredType, remoteType) {
		return synced
	}
	syncedOptions := mapFromValue(ctx, synced, diags)
	if diags.HasError() {
		return synced
	}
	syncedOptions["type"] = configuredType

	return stringMapValue(ctx, syncedOptions, diags)
}
