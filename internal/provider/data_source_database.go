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

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &databaseDataSource{}

type databaseDataSource struct {
	client *client.Client
}

type databaseDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	ServerID  types.String `tfsdk:"server_id"`
	Name      types.String `tfsdk:"name"`
	Options   types.Map    `tfsdk:"options"`
	Location  types.String `tfsdk:"location"`
	Owner     types.String `tfsdk:"owner"`
	CreatedAt types.Int64  `tfsdk:"created_at"`
	CreatedBy types.String `tfsdk:"created_by"`
	UpdatedAt types.Int64  `tfsdk:"updated_at"`
	UpdatedBy types.String `tfsdk:"updated_by"`
}

func NewDatabaseDataSource() datasource.DataSource {
	return &databaseDataSource{}
}

func (d *databaseDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (d *databaseDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing database from a Paimon REST Catalog.",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Description: "Terraform identifier, equal to the database name.", Computed: true},
			"server_id": schema.StringAttribute{Description: "Server-assigned database identifier.", Computed: true},
			"name":      schema.StringAttribute{Description: "Database name.", Required: true},
			"options": schema.MapAttribute{
				Description: "All database options returned by the REST Catalog.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"location":   schema.StringAttribute{Description: "Database location returned by the server.", Computed: true},
			"owner":      schema.StringAttribute{Description: "Database owner returned by the server.", Computed: true},
			"created_at": schema.Int64Attribute{Description: "Creation timestamp in milliseconds since the Unix epoch.", Computed: true},
			"created_by": schema.StringAttribute{Description: "Principal that created the database.", Computed: true},
			"updated_at": schema.Int64Attribute{Description: "Last update timestamp in milliseconds since the Unix epoch.", Computed: true},
			"updated_by": schema.StringAttribute{Description: "Principal that last updated the database.", Computed: true},
		},
	}
}

func (d *databaseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	clientFromProviderData(req.ProviderData, &d.client, &resp.Diagnostics, "paimon_database data source")
}

func (d *databaseDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data databaseDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	database, err := d.client.GetDatabase(ctx, data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to read Paimon database", err.Error())

		return
	}
	data.ID = types.StringValue(database.Name)
	data.ServerID = types.StringValue(database.ID)
	data.Name = types.StringValue(database.Name)
	data.Options = stringMapValue(ctx, database.Options, &resp.Diagnostics)
	data.Location = types.StringValue(database.Location)
	data.Owner = types.StringValue(database.Owner)
	data.CreatedAt = types.Int64Value(database.CreatedAt)
	data.CreatedBy = types.StringValue(database.CreatedBy)
	data.UpdatedAt = types.Int64Value(database.UpdatedAt)
	data.UpdatedBy = types.StringValue(database.UpdatedBy)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}
