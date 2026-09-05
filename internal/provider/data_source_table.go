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

var _ datasource.DataSource = &tableDataSource{}

type tableDataSource struct {
	client *client.Client
}

type tableDataSourceModel struct {
	ID            types.String `tfsdk:"id"`
	ServerID      types.String `tfsdk:"server_id"`
	Database      types.String `tfsdk:"database"`
	Name          types.String `tfsdk:"name"`
	Fields        types.List   `tfsdk:"fields"`
	PartitionKeys types.List   `tfsdk:"partition_keys"`
	PrimaryKeys   types.List   `tfsdk:"primary_keys"`
	Options       types.Map    `tfsdk:"options"`
	Comment       types.String `tfsdk:"comment"`
	SchemaID      types.Int64  `tfsdk:"schema_id"`
	Path          types.String `tfsdk:"path"`
	IsExternal    types.Bool   `tfsdk:"is_external"`
	Owner         types.String `tfsdk:"owner"`
	CreatedAt     types.Int64  `tfsdk:"created_at"`
	CreatedBy     types.String `tfsdk:"created_by"`
	UpdatedAt     types.Int64  `tfsdk:"updated_at"`
	UpdatedBy     types.String `tfsdk:"updated_by"`
}

func NewTableDataSource() datasource.DataSource {
	return &tableDataSource{}
}

func (d *tableDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_table"
}

func (d *tableDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing table from a Paimon REST Catalog.",
		Attributes:  tableDataSourceAttributes(),
	}
}

func (d *tableDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	clientFromProviderData(req.ProviderData, &d.client, &resp.Diagnostics, "paimon_table data source")
}

func (d *tableDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data tableDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	table, err := d.client.GetTable(ctx, data.Database.ValueString(), data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to read Paimon table", err.Error())

		return
	}
	database := table.Database
	if database == "" {
		database = data.Database.ValueString()
	}
	name := table.Name
	if name == "" {
		name = data.Name.ValueString()
	}
	data.ID = types.StringValue(tableID(database, name))
	data.ServerID = types.StringValue(table.ID)
	data.Database = types.StringValue(database)
	data.Name = types.StringValue(name)
	data.Fields = fieldsValueFromRemote(ctx, table.Schema.Fields, &resp.Diagnostics)
	data.PartitionKeys = stringListValue(ctx, table.Schema.PartitionKeys, &resp.Diagnostics)
	data.PrimaryKeys = stringListValue(ctx, table.Schema.PrimaryKeys, &resp.Diagnostics)
	data.Options = stringMapValue(ctx, table.Schema.Options, &resp.Diagnostics)
	data.Comment = stringValueFromPointer(table.Schema.Comment)
	data.SchemaID = types.Int64Value(table.SchemaID)
	data.Path = types.StringValue(table.Path)
	data.IsExternal = types.BoolValue(table.IsExternal)
	data.Owner = types.StringValue(table.Owner)
	data.CreatedAt = types.Int64Value(table.CreatedAt)
	data.CreatedBy = types.StringValue(table.CreatedBy)
	data.UpdatedAt = types.Int64Value(table.UpdatedAt)
	data.UpdatedBy = types.StringValue(table.UpdatedBy)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}
