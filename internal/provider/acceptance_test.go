// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information regarding
// copyright ownership. The ASF licenses this file to You under the Apache
// License, Version 2.0 (the "License"); you may not use this file except in
// compliance with the License. You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/stretchr/testify/assert"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"paimon": providerserver.NewProtocol6WithError(New("test")()),
}

func TestAccDatabaseAndTableLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	catalog := &acceptanceCatalog{}
	server := httptest.NewServer(catalog)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccConfig(server.URL, false, false, false),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("paimon_database.analytics", "id", "analytics"),
					resource.TestCheckResourceAttr("paimon_table.events", "id", "database=analytics&table=events"),
					resource.TestCheckResourceAttr("paimon_table.events", "fields.#", "2"),
				),
			},
			{
				Config: testAccConfig(server.URL, true, false, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("paimon_database.analytics", "options.owner", "platform"),
					resource.TestCheckResourceAttr("paimon_table.events", "fields.#", "3"),
					resource.TestCheckResourceAttr("paimon_table.events", "comment", "updated"),
				),
			},
			{
				Config: testAccConfig(server.URL, true, true, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("paimon_table.events", "fields.0.name", "event_time"),
					resource.TestCheckResourceAttr("paimon_table.events", "fields.1.name", "id"),
				),
			},
			{
				Config: testAccConfig(server.URL, true, true, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("paimon_table.events", "fields.2.name", "category"),
				),
			},
			{
				ResourceName:            "paimon_table.events",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"options"},
			},
			{
				ResourceName:            "paimon_database.analytics",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"options"},
			},
		},
	})
	assert.Equal(t, 1, catalog.databaseCreates, "database updates and imports must not recreate the resource")
	assert.Equal(t, 1, catalog.tableCreates, "field additions and reordering must update the table in place")
}

func TestAccTableReplacementBoundaries(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	catalog := &acceptanceCatalog{}
	server := httptest.NewServer(catalog)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccReplacementConfig(server.URL, "BIGINT", false),
			},
			{
				Config: testAccReplacementConfig(server.URL, "INT", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionReplace),
					},
				},
			},
			{
				Config: testAccReplacementConfig(server.URL, "INT", true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("paimon_table.events", plancheck.ResourceActionReplace),
					},
				},
				Check: resource.TestCheckResourceAttr("paimon_table.events", "fields.2.name", "required_value"),
			},
		},
	})
	assert.Equal(t, 3, catalog.tableCreates, "key type changes and non-nullable additions must replace the table")
}

func testAccConfig(uri string, updated, reordered, replaced bool) string {
	owner := "data"
	comment := "initial"
	fields := `
    {
      name     = "id"
      type     = "BIGINT"
      nullable = false
    },
    {
      name     = "event_time"
      type     = "TIMESTAMP(3)"
      nullable = true
    }`
	if updated {
		owner = "platform"
		comment = "updated"
		fields += `,
    {
      name     = "payload"
      type     = "STRING"
      nullable = true
    }`
	}
	if reordered {
		fields = `
    {
      name     = "event_time"
      type     = "TIMESTAMP(3)"
      nullable = true
    },
    {
      name     = "id"
      type     = "BIGINT"
      nullable = false
    },
    {
      name     = "payload"
      type     = "STRING"
      nullable = true
    }`
	}
	if replaced {
		fields = strings.Replace(fields, "payload", "category", 1)
	}

	return fmt.Sprintf(`
provider "paimon" {
  uri = %q
}

resource "paimon_database" "analytics" {
  name = "analytics"
  options = {
    owner = %q
  }
}

resource "paimon_table" "events" {
  database = paimon_database.analytics.name
  name     = "events"
  fields   = [%s
  ]
  options = { "primary-key" = "id" }
  comment      = %q
}
`, uri, owner, fields, comment)
}

func testAccReplacementConfig(uri, keyType string, addRequired bool) string {
	requiredField := ""
	if addRequired {
		requiredField = `,
    {
      name     = "required_value"
      type     = "STRING"
      nullable = false
    }`
	}

	return fmt.Sprintf(`
provider "paimon" {
  uri = %q
}

resource "paimon_database" "analytics" {
  name = "analytics"
}

resource "paimon_table" "events" {
  database = paimon_database.analytics.name
  name     = "events"
  fields = [
    {
      name     = "id"
      type     = %q
      nullable = false
    },
    {
      name     = "event_time"
      type     = "TIMESTAMP(3)"
      nullable = true
    }%s
  ]
  options = { "primary-key" = "id" }
  allow_replacement = true
}
`, uri, keyType, requiredField)
}

type acceptanceCatalog struct {
	mu               sync.Mutex
	database         *client.Database
	table            *client.Table
	databaseCreates  int
	tableCreates     int
	tableAlters      int
	nextTableFieldID int
}

func (c *acceptanceCatalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/config":
		writeAcceptanceJSON(w, client.ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
	case r.URL.Path == "/v1/catalog/databases" && r.Method == http.MethodPost:
		var request struct {
			Name    string            `json:"name"`
			Options map[string]string `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		c.databaseCreates++
		c.database = &client.Database{ID: "db-1", Name: request.Name, Options: request.Options}
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/v1/catalog/databases/analytics" && r.Method == http.MethodGet:
		if c.database == nil {
			http.NotFound(w, r)

			return
		}
		writeAcceptanceJSON(w, c.database)
	case r.URL.Path == "/v1/catalog/databases/analytics" && r.Method == http.MethodPost:
		var request struct {
			Removals []string          `json:"removals"`
			Updates  map[string]string `json:"updates"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		for _, key := range request.Removals {
			delete(c.database.Options, key)
		}
		for key, value := range request.Updates {
			c.database.Options[key] = value
		}
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/v1/catalog/databases/analytics" && r.Method == http.MethodDelete:
		c.database = nil
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/v1/catalog/databases/analytics/tables" && r.Method == http.MethodPost:
		var request struct {
			Identifier client.Identifier `json:"identifier"`
			Schema     client.Schema     `json:"schema"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if primaryKey, configured := request.Schema.Options["primary-key"]; configured {
			if len(request.Schema.PrimaryKeys) != 0 {
				http.Error(w, "duplicate DDL and option primary keys", http.StatusBadRequest)

				return
			}
			for _, key := range strings.Split(primaryKey, ",") {
				if key = strings.TrimSpace(key); key != "" {
					request.Schema.PrimaryKeys = append(request.Schema.PrimaryKeys, key)
				}
			}
			delete(request.Schema.Options, "primary-key")
		}
		c.tableCreates++
		c.table = &client.Table{ID: "table-1", Database: request.Identifier.Database, Name: request.Identifier.Object, SchemaID: 1, Schema: request.Schema}
		c.nextTableFieldID = nextAcceptanceFieldID(request.Schema.Fields)
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/v1/catalog/databases/analytics/tables/events" && r.Method == http.MethodGet:
		if c.table == nil {
			http.NotFound(w, r)

			return
		}
		writeAcceptanceJSON(w, c.table)
	case r.URL.Path == "/v1/catalog/databases/analytics/tables/events" && r.Method == http.MethodPost:
		c.tableAlters++
		var request struct {
			Changes []map[string]json.RawMessage `json:"changes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		for _, change := range request.Changes {
			var action string
			_ = json.Unmarshal(change["action"], &action)
			switch action {
			case "setOption":
				var key, value string
				_ = json.Unmarshal(change["key"], &key)
				_ = json.Unmarshal(change["value"], &value)
				if c.table.Schema.Options == nil {
					c.table.Schema.Options = make(map[string]string)
				}
				c.table.Schema.Options[key] = value
			case "removeOption":
				var key string
				_ = json.Unmarshal(change["key"], &key)
				delete(c.table.Schema.Options, key)
			case "updateComment":
				_ = json.Unmarshal(change["comment"], &c.table.Schema.Comment)
			case "addColumn":
				var names []string
				var dataType client.DataType
				var description *string
				_ = json.Unmarshal(change["fieldNames"], &names)
				_ = json.Unmarshal(change["dataType"], &dataType)
				_ = json.Unmarshal(change["comment"], &description)
				if len(names) == 1 {
					c.table.Schema.Fields = append(c.table.Schema.Fields, client.Field{ID: c.nextTableFieldID, Name: names[0], Type: dataType, Description: description})
					c.nextTableFieldID++
				}
			case "dropColumn":
				var names []string
				_ = json.Unmarshal(change["fieldNames"], &names)
				if len(names) == 1 {
					c.table.Schema.Fields = dropAcceptanceField(c.table.Schema.Fields, names[0])
				}
			case "updateColumnPosition":
				var move struct {
					FieldName          string `json:"fieldName"`
					Type               string `json:"type"`
					ReferenceFieldName string `json:"referenceFieldName"`
				}
				_ = json.Unmarshal(change["move"], &move)
				c.table.Schema.Fields = moveAcceptanceField(c.table.Schema.Fields, move.FieldName, move.Type, move.ReferenceFieldName)
			}
		}
		c.table.SchemaID++
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/v1/catalog/databases/analytics/tables/events" && r.Method == http.MethodDelete:
		c.table = nil
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func writeAcceptanceJSON(w http.ResponseWriter, value any) {
	_ = json.NewEncoder(w).Encode(value)
}

func nextAcceptanceFieldID(fields []client.Field) int {
	next := 0
	for _, field := range fields {
		if field.ID >= next {
			next = field.ID + 1
		}
	}

	return next
}

func dropAcceptanceField(fields []client.Field, name string) []client.Field {
	for index := range fields {
		if fields[index].Name == name {
			return append(fields[:index], fields[index+1:]...)
		}
	}

	return fields
}

func moveAcceptanceField(fields []client.Field, name, moveType, reference string) []client.Field {
	from := -1
	for index := range fields {
		if fields[index].Name == name {
			from = index

			break
		}
	}
	if from < 0 {
		return fields
	}
	field := fields[from]
	fields = append(fields[:from], fields[from+1:]...)
	to := 0
	if moveType == "AFTER" {
		to = len(fields)
		for index := range fields {
			if fields[index].Name == reference {
				to = index + 1

				break
			}
		}
	}
	fields = append(fields, client.Field{})
	copy(fields[to+1:], fields[to:])
	fields[to] = field

	return fields
}
