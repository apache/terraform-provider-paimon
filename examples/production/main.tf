// Licensed to the Apache Software Foundation (ASF) under one or more
// contributor license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright ownership.
// The ASF licenses this file to You under the Apache License, Version 2.0
// (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

terraform {
  required_providers {
    paimon = {
      source = "apache/paimon"
    }
  }
}

variable "catalog_uri" {
  type = string
}

variable "warehouse" {
  type = string
}

variable "catalog_token" {
  type      = string
  sensitive = true
}

provider "paimon" {
  uri                      = var.catalog_uri
  warehouse                = var.warehouse
  token_provider           = "bear"
  token                    = var.catalog_token
  request_timeout_seconds  = 30
  recovery_timeout_seconds = 60
}

resource "paimon_database" "analytics" {
  name = "analytics"

  lifecycle {
    prevent_destroy = true
  }
}

resource "paimon_table" "events" {
  database          = paimon_database.analytics.name
  name              = "events"
  allow_replacement = false
  fields = [
    { name = "id", type = "BIGINT", nullable = false },
    { name = "payload", type = "STRING" }
  ]

  options = {
    "primary-key" = "id"

    "bucket"       = "4"
    "merge-engine" = "deduplicate"
  }

  lifecycle {
    prevent_destroy = true
  }
}
