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
	"strconv"
	"strings"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Match Schema.normalizePrimaryKeys: trim tokens and discard empty entries.
func parsePrimaryKeyOption(value string) []string {
	keys := make([]string, 0)
	for _, key := range strings.Split(value, ",") {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}

	return keys
}

func tableOptionsMapWithPrimaryKeys(options map[string]string, keys []string) map[string]string {
	logical := make(map[string]string, len(options)+1)
	for key, value := range options {
		logical[key] = value
	}
	logical["primary-key"] = strings.Join(keys, ",")

	return logical
}

func tableOptionsWithPrimaryKeys(ctx context.Context, options types.Map, keys types.List, diags *diag.Diagnostics) types.Map {
	if options.IsUnknown() || keys.IsUnknown() {
		return types.MapUnknown(types.StringType)
	}

	return stringMapValue(ctx, tableOptionsMapWithPrimaryKeys(mapFromValue(ctx, options, diags), stringListFromValue(ctx, keys, diags)), diags)
}

func stabilizeTableKeys(ctx context.Context, config, state tableResourceModel, plan *tableResourceModel, diags *diag.Diagnostics) {
	value, configured, known := knownTableOption(config.Options, "primary-key")
	switch {
	case !known:
		plan.PrimaryKeys = types.ListUnknown(types.StringType)
	case configured:
		plan.PrimaryKeys = stringListValue(ctx, parsePrimaryKeyOption(value), diags)
	default:
		_, wasManaged, _ := knownTableOption(state.Options, "primary-key")
		if wasManaged || state.PrimaryKeys.IsNull() {
			plan.PrimaryKeys = stringListValue(ctx, nil, diags)
		} else {
			plan.PrimaryKeys = state.PrimaryKeys
		}
	}
	if config.PartitionKeys.IsNull() {
		if state.PartitionKeys.IsNull() {
			plan.PartitionKeys = stringListValue(ctx, nil, diags)
		} else {
			plan.PartitionKeys = state.PartitionKeys
		}
	}
}

func tablePlanWithServerDefaults(plan, state tableResourceModel) tableResourceModel {
	effective := plan
	values := state.ServerOptions.Elements()
	for _, key := range []string{"primary-key.nullable"} {
		_, beforeManaged, _ := knownTableOption(state.Options, key)
		_, afterManaged, known := knownTableOption(plan.Options, key)
		if beforeManaged && !afterManaged && known {
			delete(values, key)
		}
	}
	effective.ServerOptions = types.MapValueMust(types.StringType, values)

	return effective
}

func stabilizeFieldNullability(ctx context.Context, configured, previous, planned []tableFieldModel, plan, state tableResourceModel, diags *diag.Diagnostics) {
	if plan.PrimaryKeys.IsUnknown() {
		return
	}
	keys := stringListFromValue(ctx, plan.PrimaryKeys, diags)
	effective := tablePlanWithServerDefaults(plan, state)
	nullableValue, hasNullable, nullableKnown := knownTableOption(plan.Options, "primary-key.nullable")
	if !hasNullable && nullableKnown {
		nullableValue, _, nullableKnown = knownTableOption(effective.ServerOptions, "primary-key.nullable")
	}
	if !nullableKnown {
		return
	}
	pkNullable := false
	if nullableValue != "" {
		var err error
		pkNullable, err = strconv.ParseBool(nullableValue)
		if err != nil {
			diags.AddError("Invalid primary-key.nullable option", "Expected true or false.")

			return
		}
	}
	byID := make(map[int64]tableFieldModel, len(previous))
	for _, field := range previous {
		if !field.ID.IsNull() && !field.ID.IsUnknown() {
			byID[field.ID.ValueInt64()] = field
		}
	}
	for i := range planned {
		if !configured[i].Nullable.IsNull() || configured[i].Type.IsUnknown() || configured[i].Name.IsUnknown() {
			continue
		}
		switch {
		case slices.Contains(keys, planned[i].Name.ValueString()):
			planned[i].Nullable = types.BoolValue(pkNullable)
		case !planned[i].ID.IsNull() && !planned[i].ID.IsUnknown():
			if old, ok := byID[planned[i].ID.ValueInt64()]; ok {
				planned[i].Nullable = old.Nullable

				continue
			}
			_, nullable := splitFieldType(client.DataType(planned[i].Type.ValueString()))
			planned[i].Nullable = types.BoolValue(nullable)
		default:
			_, nullable := splitFieldType(client.DataType(planned[i].Type.ValueString()))
			planned[i].Nullable = types.BoolValue(nullable)
		}
	}
}

// Send the option through Java's normalization path without also declaring DDL keys.
func tableCreateSchema(ctx context.Context, plan tableResourceModel, normalized client.Schema, diags *diag.Diagnostics) client.Schema {
	options := mapFromValue(ctx, plan.Options, diags)
	if value, exists := options["primary-key"]; exists {
		normalized.Options = make(map[string]string, len(normalized.Options)+1)
		for key, option := range options {
			normalized.Options[key] = option
		}
		normalized.Options["primary-key"] = value
		normalized.PrimaryKeys = []string{}
	}

	return normalized
}

func retainPrimaryKeyOptionSpelling(managed, synced types.Map, remote map[string]string) types.Map {
	value, exists, known := knownTableOption(managed, "primary-key")
	if !exists || !known || !slices.Equal(parsePrimaryKeyOption(value), parsePrimaryKeyOption(remote["primary-key"])) {
		return synced
	}
	values := synced.Elements()
	values["primary-key"] = types.StringValue(value)

	return types.MapValueMust(types.StringType, values)
}

// An entire list or object may depend on another resource. Defer field planning
// until its shape is known; primitive unknown attributes remain inspectable.
func tableFieldsInspectable(fields types.List) bool {
	if fields.IsUnknown() || fields.IsNull() {
		return false
	}
	for _, field := range fields.Elements() {
		if field.IsUnknown() || field.IsNull() {
			return false
		}
	}

	return true
}
