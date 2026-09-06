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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/require"
)

// Compare hand-written field references with the schema actually served over
// the Terraform protocol. Prose examples alone must not count as API coverage.
func TestDocumentationSchema(t *testing.T) {
	server := providerserver.NewProtocol6(New("docs")())()
	response, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)
	require.Empty(t, response.Diagnostics)
	pages := map[string]*tfprotov6.Schema{"index.md": response.Provider}
	for name, schema := range response.ResourceSchemas {
		pages[filepath.Join("resources", strings.TrimPrefix(name, "paimon_")+".md")] = schema
	}
	for name, schema := range response.DataSourceSchemas {
		pages[filepath.Join("data-sources", strings.TrimPrefix(name, "paimon_")+".md")] = schema
	}
	for page, schema := range pages {
		t.Run(page, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "docs", page))
			require.NoError(t, err, "every registered API must have a reference page")
			text := string(data)
			require.Equal(t, 1, strings.Count(text, "<!-- schema:begin -->"))
			require.Equal(t, 1, strings.Count(text, "<!-- schema:end -->"))
			_, section, _ := strings.Cut(text, "<!-- schema:begin -->")
			section, _, found := strings.Cut(section, "<!-- schema:end -->")
			require.True(t, found)
			require.Empty(t, schema.Block.BlockTypes, "document new nested blocks explicitly before extending this check")
			expected := make(map[string]documentedAttribute)
			collectDocumentedAttributes(t, schema.Block.Attributes, "", expected)
			// Exercise both checkout formats on every OS, including Unix runners.
			section = strings.ReplaceAll(section, "\r\n", "\n")
			for name, document := range map[string]string{
				"LF":   section,
				"CRLF": strings.ReplaceAll(section, "\n", "\r\n"),
			} {
				t.Run(name, func(t *testing.T) {
					actual := parseDocumentedAttributes(t, document)
					require.Equal(t, expected, actual, "update docs/%s to match the public schema", page)
				})
			}
		})
	}
}

type documentedAttribute struct{ Type, Mode, Sensitive string }

func parseDocumentedAttributes(t *testing.T, section string) map[string]documentedAttribute {
	t.Helper()
	attributes := make(map[string]documentedAttribute)
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		columns := strings.Split(strings.Trim(line, "|"), "|")
		require.Len(t, columns, 5, "schema rows have attribute, type, mode, sensitive and meaning columns")
		for i := range columns {
			columns[i] = strings.Trim(strings.TrimSpace(columns[i]), "`")
		}
		require.NotEmpty(t, columns[4], "every attribute needs a meaning/default description")
		_, duplicate := attributes[columns[0]]
		require.False(t, duplicate, "duplicate documented attribute: %s", columns[0])
		attributes[columns[0]] = documentedAttribute{Type: columns[1], Mode: columns[2], Sensitive: columns[3]}
	}

	return attributes
}

func collectDocumentedAttributes(t *testing.T, attributes []*tfprotov6.SchemaAttribute, prefix string, result map[string]documentedAttribute) {
	t.Helper()
	for _, attribute := range attributes {
		mode := "Computed"
		switch {
		case attribute.Required:
			mode = "Required"
		case attribute.Optional && attribute.Computed:
			mode = "Optional + computed"
		case attribute.Optional:
			mode = "Optional"
		}
		sensitive := "No"
		if attribute.Sensitive {
			sensitive = "Yes"
		}
		name := prefix + attribute.Name
		result[name] = documentedAttribute{Type: documentationType(t, attribute.ValueType()), Mode: mode, Sensitive: sensitive}
		if nested := attribute.NestedType; nested != nil {
			suffix := "."
			if nested.Nesting != tfprotov6.SchemaObjectNestingModeSingle {
				suffix = "[]."
			}
			collectDocumentedAttributes(t, nested.Attributes, name+suffix, result)
		}
	}
}

func documentationType(t *testing.T, value tftypes.Type) string {
	t.Helper()
	switch {
	case value.Is(tftypes.String):
		return "string"
	case value.Is(tftypes.Number):
		return "number"
	case value.Is(tftypes.Bool):
		return "bool"
	}
	switch typed := value.(type) {
	case tftypes.List:
		return "list(" + documentationType(t, typed.ElementType) + ")"
	case tftypes.Set:
		return "set(" + documentationType(t, typed.ElementType) + ")"
	case tftypes.Map:
		return "map(" + documentationType(t, typed.ElementType) + ")"
	case tftypes.Object:
		return "object"
	default:
		t.Fatalf("document the new schema type %s", value)

		return ""
	}
}
