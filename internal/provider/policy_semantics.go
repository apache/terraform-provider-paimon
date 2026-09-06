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
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/apache/terraform-provider-paimon/internal/client"
)

// Follow Java's TableQueryAuthResult.remapPredicate and RESTCatalogServerUtils:
// field references resolve by name; a one-child AND/OR reduces to its child.
// Only known AST positions are normalized. Literals, cast targets and unknown
// members retain their meaning and must still compare equal.
func equivalentPolicyContent(ctx context.Context, api *client.Client, spec policySpec, left, right string) (bool, error) {
	if equivalentJSON(left, right) {
		return true, nil
	}
	if left == "" || right == "" {
		return false, nil
	}
	var before, after any
	for _, item := range []struct {
		text   string
		target *any
	}{{left, &before}, {right, &after}} {
		decoder := json.NewDecoder(strings.NewReader(item.text))
		decoder.UseNumber()
		if err := decoder.Decode(item.target); err != nil {
			return false, nil
		}
	}
	var fields []client.Field
	loaded := false
	remap := func(ref map[string]any) error {
		name, ok := ref["name"].(string)
		if !ok {
			return errors.New("policy field reference has no name")
		}
		if !loaded {
			if api == nil {
				return errors.New("a configured catalog is required to resolve policy field references")
			}
			table, err := api.GetTable(ctx, spec.database, spec.table)
			if err != nil {
				return fmt.Errorf("resolve policy references against table schema: %w", err)
			}
			fields, loaded = table.Schema.Fields, true
		}
		for index, field := range fields {
			if field.Name != name {
				continue
			}
			encoded, err := json.Marshal(field.Type)
			if err != nil {
				return fmt.Errorf("resolve policy field type: %w", err)
			}
			var dataType any
			decoder := json.NewDecoder(strings.NewReader(string(encoded)))
			decoder.UseNumber()
			if err := decoder.Decode(&dataType); err != nil {
				return fmt.Errorf("resolve policy field type: %w", err)
			}
			ref["index"], ref["type"] = json.Number(strconv.Itoa(index)), dataType

			return nil
		}

		return errors.New("policy field reference does not resolve in the current table schema")
	}
	predicate := spec.policyType == client.PolicyTypeRowFilter
	var err error
	before, err = normalizePolicyAST(before, predicate, remap)
	if err != nil {
		return false, err
	}
	after, err = normalizePolicyAST(after, predicate, remap)
	if err != nil {
		return false, err
	}

	return reflect.DeepEqual(before, after), nil
}

func normalizePolicyAST(value any, predicate bool, remap func(map[string]any) error) (any, error) {
	node, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	if predicate {
		switch node["kind"] {
		case "COMPOUND":
			children, ok := node["children"].([]any)
			if !ok {
				return value, nil
			}
			for i, child := range children {
				normalized, err := normalizePolicyAST(child, true, remap)
				if err != nil {
					return nil, err
				}
				children[i] = normalized
			}
			if len(children) == 1 && len(node) == 3 && (node["function"] == "AND" || node["function"] == "OR") {
				return children[0], nil
			}
		case "LEAF":
			normalized, err := normalizePolicyAST(node["transform"], false, remap)
			if err != nil {
				return nil, err
			}
			node["transform"] = normalized
		}

		return node, nil
	}
	switch node["name"] {
	case "FIELD_REF", "CAST":
		if ref, ok := node["fieldRef"].(map[string]any); ok {
			if err := remap(ref); err != nil {
				return nil, err
			}
		}
		if node["name"] == "CAST" {
			if target, ok := node["type"].(string); ok && !client.IsCompositeDataType(client.DataType(target)) {
				// Java serializes atomic aliases such as INTEGER as INT.
				// Keep the cast target's type and nullability significant.
				encoded, err := json.Marshal(client.DataType(target))
				if err != nil {
					return nil, err
				}
				var canonical string
				if err := json.Unmarshal(encoded, &canonical); err != nil {
					return nil, err
				}
				node["type"] = canonical
			}
		}
	case "CONCAT", "CONCAT_WS", "UPPER", "LOWER", "SUBSTRING", "TRIM":
		if inputs, ok := node["inputs"].([]any); ok {
			for _, input := range inputs {
				if ref, ok := input.(map[string]any); ok {
					if err := remap(ref); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	return node, nil
}

func (s policySpec) matchesWithSchema(ctx context.Context, api *client.Client, policy client.DataPolicy) (bool, error) {
	switch s.policyType {
	case client.PolicyTypeRowFilter:
		if policy.RowFilter != nil {
			return equivalentPolicyContent(ctx, api, s, s.content, policy.RowFilter.Predicate)
		}
	case client.PolicyTypeColumnMasking:
		if policy.ColumnMask != nil && policy.ColumnMask.OnColumn == s.column {
			return equivalentPolicyContent(ctx, api, s, s.content, policy.ColumnMask.Transform)
		}
	}

	return false, nil
}

func observePolicy(ctx context.Context, api *client.Client, spec policySpec) (policyLookupObservation, error) {
	policy, found, err := lookupPolicy(ctx, api, spec)
	if err != nil || !found {
		return policyLookupObservation{found: found}, err
	}
	matches, err := spec.matchesWithSchema(ctx, api, policy)

	return policyLookupObservation{policy: policy, found: true, matches: matches}, err
}
