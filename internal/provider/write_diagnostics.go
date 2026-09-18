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
	"github.com/apache/terraform-provider-paimon/internal/client"
)

const (
	permissionReferences = "the principal or the catalog, database, table, function, view, or columns the permission targets"
	rowFilterReferences  = "the principal or the table the row filter targets"
	columnMaskReferences = "the principal, the table, or the column the column mask targets"
)

// explainNotFound adds a hint when a write was refused with HTTP 404; a 404
// from loading the config is not about the write and gets none.
func explainNotFound(detail string, cause error, referenced string) string {
	if !explainableNotFound(cause) {
		return detail
	}

	return detail + "\n\nThe REST Catalog answered HTTP 404 to this write. That usually means it could not find " + referenced +
		": check that each of them exists on the catalog exactly as written, and that the principal is already known to the catalog's identity provider." +
		" A catalog that does not serve this management API answers the same way."
}

// explainableNotFound: a 404 from the operation, not from loading the config.
func explainableNotFound(err error) bool {
	return client.IsNotFound(err) && !client.IsConfigurationFailure(err)
}
