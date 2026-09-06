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
	"reflect"
	"strings"
	"time"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	mutationReadRetryDelay = 50 * time.Millisecond
)

func clientFromProviderData(data any, target **client.Client, diags *diag.Diagnostics, kind string) {
	if data == nil {
		return
	}
	api, ok := data.(*client.Client)
	if !ok {
		diags.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client while configuring %s, got %T. Please report this issue to the provider developers.", kind, data),
		)

		return
	}
	*target = api
}

func mapFromValue(ctx context.Context, value types.Map, diags *diag.Diagnostics) map[string]string {
	result := make(map[string]string)
	if value.IsNull() || value.IsUnknown() {
		return result
	}
	diags.Append(value.ElementsAs(ctx, &result, false)...)

	return result
}

func stringListFromValue(ctx context.Context, value types.List, diags *diag.Diagnostics) []string {
	result := make([]string, 0)
	if value.IsNull() || value.IsUnknown() {
		return result
	}
	diags.Append(value.ElementsAs(ctx, &result, false)...)

	return result
}

func stringSetFromValue(ctx context.Context, value types.Set, diags *diag.Diagnostics) []string {
	result := make([]string, 0)
	if value.IsNull() || value.IsUnknown() {
		return result
	}
	diags.Append(value.ElementsAs(ctx, &result, false)...)

	return result
}

func stringMapValue(ctx context.Context, value map[string]string, diags *diag.Diagnostics) types.Map {
	if value == nil {
		value = map[string]string{}
	}
	result, newDiags := types.MapValueFrom(ctx, types.StringType, value)
	diags.Append(newDiags...)

	return result
}

func stringListValue(ctx context.Context, value []string, diags *diag.Diagnostics) types.List {
	if value == nil {
		value = []string{}
	}
	result, newDiags := types.ListValueFrom(ctx, types.StringType, value)
	diags.Append(newDiags...)

	return result
}

func stringSetValue(ctx context.Context, value []string, diags *diag.Diagnostics) types.Set {
	if value == nil {
		return types.SetNull(types.StringType)
	}
	result, newDiags := types.SetValueFrom(ctx, types.StringType, value)
	diags.Append(newDiags...)

	return result
}

func equivalentJSON(left, right string) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(strings.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(strings.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil || !json.Valid([]byte(left)) || !json.Valid([]byte(right)) {
		return left == right
	}

	return reflect.DeepEqual(leftValue, rightValue)
}

func mutationRecoveryContext(ctx context.Context, api *client.Client) (context.Context, context.CancelFunc) {
	// A canceled apply context can still follow a mutation that committed remotely.
	// Keep request-scoped values, but give reconciliation and repair a fresh bound.
	return context.WithTimeout(context.WithoutCancel(ctx), api.RecoveryTimeout())
}

func retryLookupUntil[T any](ctx context.Context, lookup func(context.Context) (T, bool, error), ready func(T) bool) (T, bool, bool, error) {
	var zero T
	var lastValue T
	var lastFound bool
	var lastErr error
	for {
		value, found, err := lookup(ctx)
		if err == nil {
			lastErr = nil
			if found {
				lastValue = value
				lastFound = true
				if ready == nil || ready(value) {
					return value, true, true, nil
				}
			}
		} else {
			lastErr = err
		}
		timer := time.NewTimer(mutationReadRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastFound {
				return lastValue, true, false, lastErr
			}

			return zero, false, false, lastErr
		case <-timer.C:
		}
	}
}

func syncManagedOptions(ctx context.Context, managed types.Map, remote map[string]string, diags *diag.Diagnostics) types.Map {
	if managed.IsNull() || managed.IsUnknown() {
		return managed
	}
	current := mapFromValue(ctx, managed, diags)
	if diags.HasError() {
		return managed
	}
	updated := make(map[string]string)
	for key := range current {
		if value, ok := remote[key]; ok {
			updated[key] = value
		}
	}

	return stringMapValue(ctx, updated, diags)
}

func diffOptions(before, after map[string]string) ([]string, map[string]string) {
	removals := make([]string, 0)
	updates := make(map[string]string)
	for key := range before {
		if _, ok := after[key]; !ok {
			removals = append(removals, key)
		}
	}
	for key, value := range after {
		if previous, ok := before[key]; !ok || previous != value {
			updates[key] = value
		}
	}

	return removals, updates
}

// optionsConverged checks both halves of an update. A missing key is not an
// empty value, and a removed key must no longer be present in the catalog.
func optionsConverged(remote, planned map[string]string, removals []string) bool {
	for key, value := range planned {
		if observed, exists := remote[key]; !exists || observed != value {
			return false
		}
	}
	for _, key := range removals {
		if _, exists := remote[key]; exists {
			return false
		}
	}

	return true
}
