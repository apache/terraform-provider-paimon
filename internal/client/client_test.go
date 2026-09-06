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

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientConfigAndDatabaseLifecycle(t *testing.T) {
	var configCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		assert.Equal(t, "client-value", r.Header.Get("X-Client"))

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config":
			configCalls.Add(1)
			assert.Equal(t, "warehouse-a", r.URL.Query().Get("warehouse"))
			writeJSON(t, w, map[string]any{
				"defaults":  map[string]string{"prefix": "default", "header.X-Server": "server-value"},
				"overrides": map[string]string{"prefix": "catalog"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/catalog/databases":
			assert.Equal(t, "server-value", r.Header.Get("X-Server"))
			var request createDatabaseRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, "analytics", request.Name)
			assert.Equal(t, map[string]string{"owner": "data"}, request.Options)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/catalog/databases/analytics":
			assert.Equal(t, "server-value", r.Header.Get("X-Server"))
			writeJSON(t, w, Database{ID: "db-1", Name: "analytics", Options: map[string]string{"owner": "data"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(Config{
		URI:       server.URL + "/api/",
		Warehouse: "warehouse-a",
		Token:     "secret",
		Prefix:    "client-prefix",
		Headers:   map[string]string{"X-Client": "client-value"},
	})
	require.NoError(t, err)

	require.NoError(t, api.CreateDatabase(context.Background(), "analytics", map[string]string{"owner": "data"}))
	database, err := api.GetDatabase(context.Background(), "analytics")
	require.NoError(t, err)
	assert.Equal(t, "db-1", database.ID)
	assert.Equal(t, int32(1), configCalls.Load())
}

func TestClientReturnsTypedAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		w.WriteHeader(http.StatusNotFound)
		writeJSON(t, w, map[string]any{"message": "missing", "resourceType": "TABLE", "code": 404})
	}))
	defer server.Close()

	api, err := New(Config{URI: server.URL})
	require.NoError(t, err)
	_, err = api.GetTable(context.Background(), "db", "unknown")
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "missing", apiErr.Message)
	assert.NotContains(t, err.Error(), "missing")
}

func TestClientDoesNotExposeRemoteErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(t, w, map[string]any{"message": "reflected Bearer secret-token", "code": 50001})
	}))
	defer server.Close()

	api, err := New(Config{URI: server.URL, Token: "secret-token"})
	require.NoError(t, err)
	_, err = api.GetDatabase(context.Background(), "analytics")
	require.Error(t, err)
	assert.EqualError(t, err, "Paimon REST API returned HTTP 500 with code 50001")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestClientRetriesRetryableReads(t *testing.T) {
	var configCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/config":
			if configCalls.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)

				return
			}
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
		case "/v1/catalog/databases/analytics":
			writeJSON(t, w, Database{ID: "db-1", Name: "analytics"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(Config{URI: server.URL})
	require.NoError(t, err)
	database, err := api.GetDatabase(context.Background(), "analytics")
	require.NoError(t, err)
	assert.Equal(t, "db-1", database.ID)
	assert.Equal(t, int32(2), configCalls.Load())
}

func TestClientTableLifecycleRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/config":
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/databases/db/tables":
			var request createTableRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, Identifier{Database: "db", Object: "events"}, request.Identifier)
			require.Len(t, request.Schema.Fields, 1)
			assert.Equal(t, DataType("BIGINT NOT NULL"), request.Schema.Fields[0].Type)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/databases/db/tables/events":
			var request alterTableRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.Len(t, request.Changes, 1)
			assert.Equal(t, "setOption", request.Changes[0]["action"])
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/catalog/databases/db/tables/events":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(Config{URI: server.URL})
	require.NoError(t, err)
	require.NoError(t, api.CreateTable(context.Background(), "db", "events", Schema{
		Fields: []Field{{ID: 0, Name: "id", Type: DataType("BIGINT NOT NULL")}},
	}))
	require.NoError(t, api.AlterTable(context.Background(), "db", "events", []SchemaChange{{"action": "setOption", "key": "bucket", "value": "4"}}))
	require.NoError(t, api.DropTable(context.Background(), "db", "events"))
}

func TestClientPermissionManagementRequests(t *testing.T) {
	expireTime := "2026-09-01T00:00:00.123Z"
	assignment := PermissionAssignment{
		Resource:   PermissionResource{Type: ResourceTypeColumn, Database: "analytics", Table: "events"},
		Access:     PermissionAccessSelect,
		Principal:  "user:alice@example.com",
		Columns:    &PermissionColumns{ColumnNames: []string{"event_id", "event_time"}},
		ExpireTime: &expireTime,
	}
	grantCalls, listCalls, revokeCalls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/config":
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/permissions/grant":
			grantCalls++
			var request PermissionAssignment
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, assignment, request)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/catalog/permissions":
			listCalls++
			assert.Equal(t, ResourceTypeColumn, r.URL.Query().Get("resourceType"))
			assert.Equal(t, "analytics", r.URL.Query().Get("database"))
			assert.Equal(t, "events", r.URL.Query().Get("table"))
			assert.Equal(t, PermissionAccessSelect, r.URL.Query().Get("access"))
			assert.Equal(t, "user:alice@example.com", r.URL.Query().Get("principal"))
			assert.Equal(t, "2", r.URL.Query().Get("maxResults"))
			assert.Equal(t, "next/opaque", r.URL.Query().Get("pageToken"))
			writeJSON(t, w, ListPermissionsResponse{Permissions: []PermissionAssignment{assignment}, NextPageToken: "after"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/permissions/revoke":
			revokeCalls++
			var request struct {
				Resource  PermissionResource `json:"resource"`
				Access    string             `json:"access"`
				Principal string             `json:"principal"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, assignment.Resource, request.Resource)
			assert.Equal(t, assignment.Access, request.Access)
			assert.Equal(t, assignment.Principal, request.Principal)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(Config{URI: server.URL})
	require.NoError(t, err)
	require.NoError(t, api.GrantPermission(context.Background(), assignment))
	response, err := api.ListPermissions(context.Background(), ListPermissionsRequest{
		Resource:   assignment.Resource,
		Principal:  assignment.Principal,
		Access:     assignment.Access,
		PageToken:  "next/opaque",
		MaxResults: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []PermissionAssignment{assignment}, response.Permissions)
	assert.Equal(t, "after", response.NextPageToken)
	require.NoError(t, api.RevokePermission(context.Background(), assignment.Resource, assignment.Access, assignment.Principal))
	assert.Equal(t, 1, grantCalls)
	assert.Equal(t, 1, listCalls)
	assert.Equal(t, 1, revokeCalls)
}

func TestClientPolicyManagementRequests(t *testing.T) {
	rowRequest := PolicyRequest{RowFilter: &RowFilter{Predicate: `{"field":"tenant_id"}`}, Principal: "role:analyst"}
	maskRequest := PolicyRequest{ColumnMask: &ColumnMask{OnColumn: "email", Transform: `{"type":"null"}`}, Principal: "role:analyst"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/config":
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			var request PolicyRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			if request.RowFilter != nil {
				assert.Equal(t, rowRequest, request)
			} else {
				assert.Equal(t, maskRequest, request)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies":
			assert.Equal(t, PolicyTypeColumnMasking, r.URL.Query().Get("type"))
			assert.Equal(t, "role:analyst", r.URL.Query().Get("principal"))
			assert.Equal(t, "email", r.URL.Query().Get("column"))
			assert.Equal(t, "page-2", r.URL.Query().Get("pageToken"))
			assert.Equal(t, "10", r.URL.Query().Get("maxResults"))
			writeJSON(t, w, ListPoliciesResponse{Policies: []DataPolicy{{
				Resource:   PermissionResource{Type: ResourceTypeTable, Database: "analytics", Table: "events"},
				ColumnMask: maskRequest.ColumnMask,
				Principal:  maskRequest.Principal,
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/databases/analytics/tables/events/policies/drop":
			var request dropPolicyRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, dropPolicyRequest{Type: PolicyTypeColumnMasking, Principal: "role:analyst", Column: "email"}, request)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(Config{URI: server.URL})
	require.NoError(t, err)
	require.NoError(t, api.CreatePolicy(context.Background(), "analytics", "events", rowRequest))
	require.NoError(t, api.CreatePolicy(context.Background(), "analytics", "events", maskRequest))
	response, err := api.ListPolicies(context.Background(), ListPoliciesRequest{
		Database:   "analytics",
		Table:      "events",
		Type:       PolicyTypeColumnMasking,
		Principal:  "role:analyst",
		Column:     "email",
		PageToken:  "page-2",
		MaxResults: 10,
	})
	require.NoError(t, err)
	require.Len(t, response.Policies, 1)
	assert.Equal(t, maskRequest.ColumnMask, response.Policies[0].ColumnMask)
	require.NoError(t, api.DropPolicy(context.Background(), "analytics", "events", PolicyTypeColumnMasking, "role:analyst", "email"))
}

func TestDataTypeStructuredJSON(t *testing.T) {
	input := []byte(`{"type":"ROW NOT NULL","fields":[{"id":1,"name":"item","type":{"type":"ARRAY","element":"STRING NOT NULL"}}]}`)
	var dataType DataType
	require.NoError(t, json.Unmarshal(input, &dataType))
	assert.Equal(t, DataType("ROW<item ARRAY<STRING NOT NULL>> NOT NULL"), dataType)

	encoded, err := json.Marshal(dataType)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"ROW NOT NULL",
		"fields":[{"id":0,"name":"item","type":{"type":"ARRAY","element":"STRING NOT NULL"}}]
	}`, string(encoded))
}

func TestSchemaMarshalAssignsUniqueNestedFieldIDs(t *testing.T) {
	schema := Schema{
		Fields: []Field{
			{ID: 2, Name: "id", Type: DataType("BIGINT NOT NULL")},
			{
				ID:   7,
				Name: "payload",
				Type: DataType("ROW<`item name` ARRAY<MAP<STRING NOT NULL, ROW<value VECTOR<DOUBLE, 3>>>> COMMENT 'item''s label'>"),
			},
		},
	}

	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"fields":[
			{"id":2,"name":"id","type":"BIGINT NOT NULL"},
			{"id":7,"name":"payload","type":{
				"type":"ROW",
				"fields":[{
					"id":8,
					"name":"item name",
					"type":{"type":"ARRAY","element":{"type":"MAP","key":"STRING NOT NULL","value":{"type":"ROW","fields":[{"id":9,"name":"value","type":{"type":"VECTOR","element":"DOUBLE","length":3}}]}}},
					"description":"item's label"
				}]
			}}
		],
		"partitionKeys":[],
		"primaryKeys":[],
		"options":{}
	}`, string(encoded))
}

func TestSchemaRoundTripPreservesNestedFieldIDs(t *testing.T) {
	input := []byte(`{
		"fields":[{"id":7,"name":"payload","type":{"type":"ROW","fields":[
			{"id":42,"name":"item/name","type":"STRING"},
			{"id":43,"name":"nested","type":{"type":"ROW","fields":[{"id":44,"name":"value","type":"BIGINT"}]}}
		]}}],
		"partitionKeys":[],"primaryKeys":[],"options":{}
	}`)
	var schema Schema
	require.NoError(t, json.Unmarshal(input, &schema))
	require.Len(t, schema.Fields, 1)
	assert.Equal(t, map[string]int{
		"/fields/item~1name":          42,
		"/fields/nested":              43,
		"/fields/nested/fields/value": 44,
	}, schema.Fields[0].NestedFieldIDs)

	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	assert.JSONEq(t, string(input), string(encoded))
}

func TestSchemaChangeDataTypePreservesNestedFieldIDs(t *testing.T) {
	encoded, err := json.Marshal(SchemaChangeDataType{
		Type: DataType("ROW<item STRING, nested ROW<value BIGINT>>"),
		NestedFieldIDs: map[string]int{
			"/fields/item":                42,
			"/fields/nested":              43,
			"/fields/nested/fields/value": 44,
		},
		UsedFieldIDs: []int{0, 7, 41},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"ROW","fields":[
			{"id":42,"name":"item","type":"STRING"},
			{"id":43,"name":"nested","type":{"type":"ROW","fields":[{"id":44,"name":"value","type":"BIGINT"}]}}
		]
	}`, string(encoded))
}

func TestSchemaRoundTripDistinguishesMapKeyAndValueNestedFields(t *testing.T) {
	input := []byte(`{
		"fields":[{"id":1,"name":"labels","type":{"type":"MAP",
			"key":{"type":"ROW","fields":[{"id":2,"name":"name","type":"STRING"}]},
			"value":{"type":"ROW","fields":[{"id":3,"name":"name","type":"STRING"}]}
		}}],"partitionKeys":[],"primaryKeys":[],"options":{}
	}`)
	var schema Schema
	require.NoError(t, json.Unmarshal(input, &schema))
	assert.Equal(t, map[string]int{
		"/key/fields/name":   2,
		"/value/fields/name": 3,
	}, schema.Fields[0].NestedFieldIDs)

	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	assert.JSONEq(t, string(input), string(encoded))
}

func TestDataTypeMarshalRejectsMalformedComposite(t *testing.T) {
	_, err := json.Marshal(DataType("MAP<STRING>"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected key and value types")
}

func TestDataTypeMarshalSupportsEmptyRow(t *testing.T) {
	encoded, err := json.Marshal(DataType("ROW<> NOT NULL"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"ROW NOT NULL","fields":[]}`, string(encoded))
}

// Lexical preservation only: these opaque expressions are intentionally not
// examples of Java default conversion. The Catalog casts constant strings and
// rejects expressions that cannot be cast to the field's type.
func TestDataTypeMarshalKeepsComparisonsInNestedDefaults(t *testing.T) {
	for _, test := range []struct {
		name           string
		dataType       DataType
		greaterDefault string
		lesserDefault  string
	}{
		{
			name:           "parenthesized",
			dataType:       DataType("ROW<greater BOOLEAN DEFAULT (1 > 0), lesser BOOLEAN DEFAULT (1 < 2)>"),
			greaterDefault: "(1 > 0)",
			lesserDefault:  "(1 < 2)",
		},
		{
			name:           "unparenthesized",
			dataType:       DataType("ROW<greater BOOLEAN DEFAULT 1 > 0, lesser BOOLEAN DEFAULT 1 < 2>"),
			greaterDefault: "1 > 0",
			lesserDefault:  "1 < 2",
		},
		{
			name:           "opaque composite keyword",
			dataType:       DataType("ROW<greater BOOLEAN DEFAULT MAP > 0, lesser BOOLEAN DEFAULT MAP < 2>"),
			greaterDefault: "MAP > 0",
			lesserDefault:  "MAP < 2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.dataType)
			require.NoError(t, err)
			var structured struct {
				Fields []struct {
					DefaultValue string `json:"defaultValue"`
				} `json:"fields"`
			}
			require.NoError(t, json.Unmarshal(encoded, &structured))
			require.Len(t, structured.Fields, 2)
			assert.Equal(t, test.greaterDefault, structured.Fields[0].DefaultValue)
			assert.Equal(t, test.lesserDefault, structured.Fields[1].DefaultValue)
		})
	}
}

func TestEquivalentDataTypesNormalizesCompositeSpelling(t *testing.T) {
	assert.True(t, EquivalentDataTypes(DataType("MAP<STRING,STRING>"), DataType("MAP<STRING, STRING>")))
	assert.True(t, EquivalentDataTypes(DataType("ROW<`item` STRING>"), DataType("ROW<item STRING>")))
	assert.False(t, EquivalentDataTypes(DataType("MAP<STRING, STRING>"), DataType("MAP<STRING, BIGINT>")))
}

func TestEquivalentDataTypesNormalizesServerAcceptedAtomicSpelling(t *testing.T) {
	for _, test := range []struct {
		configured DataType
		canonical  DataType
	}{
		{configured: "INTEGER", canonical: "INT"},
		{configured: "integer", canonical: "INT"},
		{configured: "integer not  null", canonical: "INT NOT NULL"},
		{configured: "DEC", canonical: "DECIMAL(10, 0)"},
		{configured: "NUMERIC(12)", canonical: "DECIMAL(12, 0)"},
		{configured: "DOUBLE PRECISION", canonical: "DOUBLE"},
		{configured: "CHAR", canonical: "CHAR(1)"},
		{configured: "VARCHAR", canonical: "VARCHAR(1)"},
		{configured: "BYTES", canonical: "VARBINARY(2147483647)"},
		{configured: "TIME", canonical: "TIME(0)"},
		{configured: "TIMESTAMP", canonical: "TIMESTAMP(6)"},
		{configured: "TIMESTAMP NULL", canonical: "TIMESTAMP(6)"},
		{configured: "TIMESTAMP_LTZ", canonical: "TIMESTAMP(6) WITH LOCAL TIME ZONE"},
		{configured: "geometry(ogc:crs84) not null", canonical: "GEOMETRY(OGC:CRS84) NOT NULL"},
		{configured: "GEOGRAPHY(EPSG:4326)", canonical: "GEOGRAPHY(EPSG:4326, spherical)"},
		{configured: "geography('epsg:4326', 'KARNEY')", canonical: "GEOGRAPHY(EPSG:4326, karney)"},
		{configured: "MAP<integer, ARRAY<dec>>", canonical: "MAP<INT, ARRAY<DECIMAL(10, 0)>>"},
		{configured: "INTEGER ARRAY", canonical: "ARRAY<INT>"},
		{configured: "integer not null array not null", canonical: "ARRAY<INT NOT NULL> NOT NULL"},
	} {
		t.Run(string(test.configured), func(t *testing.T) {
			assert.True(t, EquivalentDataTypes(test.configured, test.canonical))
		})
	}
}

func TestDataTypeMarshalKeepsComparisonInNestedRowDefault(t *testing.T) {
	encoded, err := json.Marshal(DataType("ROW<nested ROW<flag BOOLEAN DEFAULT 1 > 0>, tail BOOLEAN DEFAULT 1 < 2>"))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"ROW",
		"fields":[
			{"id":0,"name":"nested","type":{"type":"ROW","fields":[
				{"id":1,"name":"flag","type":"BOOLEAN","defaultValue":"1 > 0"}
			]}},
			{"id":2,"name":"tail","type":"BOOLEAN","defaultValue":"1 < 2"}
		]
	}`, string(encoded))
}

func TestSchemaMarshalRejectsInvalidFieldIDs(t *testing.T) {
	_, err := json.Marshal(Schema{Fields: []Field{
		{ID: maxPaimonFieldID, Name: "max", Type: DataType("STRING")},
	}})
	require.NoError(t, err)

	_, err = json.Marshal(Schema{Fields: []Field{
		{ID: 1, Name: "first", Type: DataType("STRING")},
		{ID: 1, Name: "duplicate", Type: DataType("STRING")},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field ID 1 is duplicated")

	_, err = json.Marshal(Schema{Fields: []Field{
		{ID: maxPaimonFieldID + 1, Name: "reserved", Type: DataType("STRING")},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be between 0 and")

	_, err = json.Marshal(Schema{Fields: []Field{
		{ID: maxPaimonFieldID, Name: "row", Type: DataType("ROW<nested STRING>")},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested field IDs exceed")
}

func TestNewRejectsInvalidURI(t *testing.T) {
	_, err := New(Config{URI: "localhost:8080"})
	require.EqualError(t, err, "Paimon REST URI must use http or https")
}

func TestClientRejectsRedirectWithoutForwardingCredentials(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		assert.Empty(t, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	for name, config := range map[string]Config{
		"bearer": {URI: redirect.URL, Token: "must-not-leak"},
		"dlf": {
			URI:          redirect.URL,
			AuthProvider: AuthProviderDLF,
			DLF: &DLFConfig{
				Region:           "cn-hangzhou",
				AccessKeyID:      "must-not-leak-id",
				AccessKeySecret:  "must-not-leak-secret",
				SecurityToken:    "must-not-leak-sts",
				SigningAlgorithm: DLFSigningDefault,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			api, err := New(config)
			require.NoError(t, err)
			_, err = api.GetDatabase(context.Background(), "analytics")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "redirects are not allowed")
			assert.NotContains(t, err.Error(), "must-not-leak")
			assert.Equal(t, int32(0), targetCalls.Load())
		})
	}
}

func TestClientRejectsOversizedSuccessfulResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", maxAPIResponseBodySize)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer server.Close()

	api, err := New(Config{URI: server.URL})
	require.NoError(t, err)
	_, err = api.GetDatabase(context.Background(), "analytics")
	require.EqualError(t, err, "Paimon REST response exceeded 16 MiB size limit")
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value))
}

func TestMutationSuccessDoesNotDependOnUnusedResponseBody(t *testing.T) {
	var created atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeJSON(t, w, ConfigResponse{})

			return
		}
		if r.Method == http.MethodPost {
			created.Store(true)
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))

			return
		}
		writeJSON(t, w, Database{Name: "analytics"})
	}))
	defer server.Close()
	api, err := New(Config{URI: server.URL})
	require.NoError(t, err)
	require.NoError(t, api.CreateDatabase(context.Background(), "analytics", nil))
	require.True(t, created.Load())
	database, err := api.GetDatabase(context.Background(), "analytics")
	require.NoError(t, err)
	require.Equal(t, "analytics", database.Name)
}

func TestRequestTimeoutPreservesUncertainMutationOutcome(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			writeJSON(t, w, ConfigResponse{})

			return
		}
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	input := &http.Client{Timeout: time.Minute}
	api, err := New(Config{URI: server.URL, HTTPClient: input, RequestTimeout: 50 * time.Millisecond, RecoveryTimeout: time.Second})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = api.CreateDatabase(ctx, "analytics", nil)
	require.Error(t, err)
	require.True(t, IsMutationOutcomeUncertain(err))
	require.NoError(t, ctx.Err(), "the configured request timeout must expire first")
	require.Equal(t, int32(1), calls.Load(), "an uncertain mutation must never be blindly retried")
	require.Equal(t, time.Minute, input.Timeout, "do not alter the caller's shared HTTP client")
	require.Equal(t, time.Second, api.RecoveryTimeout())
}

func TestAPIErrorsExposeOnlySafeRequestIDs(t *testing.T) {
	const requestID = "89f29f74-3a2a-43b9-b698-f24e6e9c872c"
	for _, test := range []struct {
		name, header, token, want string
	}{
		{name: "correlation", header: requestID, want: requestID},
		{name: "arbitrary response text", header: "secret-must-not-be-shown"},
		{name: "echoed bearer", header: requestID, token: requestID},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Acs-Request-Id", test.header)
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":403,"StatusCode":503,"message":"secret-response-body"}`))
			}))
			defer server.Close()
			api, err := New(Config{URI: server.URL, Token: test.token})
			require.NoError(t, err)
			_, err = api.GetDatabase(context.Background(), "analytics")
			require.Error(t, err)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, http.StatusForbidden, apiErr.StatusCode, "the error body must not override the HTTP outcome")
			require.False(t, IsMutationOutcomeUncertain(err))
			require.Equal(t, test.want, apiErr.RequestID)
			require.NotContains(t, err.Error(), "secret")
			if test.want == "" {
				require.NotContains(t, err.Error(), requestID)
			} else {
				require.Contains(t, err.Error(), requestID)
			}
		})
	}
}
