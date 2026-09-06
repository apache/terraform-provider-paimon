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
	"fmt"
	"time"

	"github.com/apache/terraform-provider-paimon/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ provider.Provider                   = &paimonProvider{}
	_ provider.ProviderWithValidateConfig = &paimonProvider{}
)

func (p *paimonProvider) ValidateConfig(ctx context.Context, req provider.ValidateConfigRequest, resp *provider.ValidateConfigResponse) {
	var config paimonProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if !resp.Diagnostics.HasError() {
		validateAuthenticationConfig(config, &resp.Diagnostics)
	}
}

func validateAuthenticationConfig(config paimonProviderModel, diags *diag.Diagnostics) {
	if config.TokenProvider.IsUnknown() {
		return
	}
	if knownString(config.TokenProvider) == client.AuthProviderBearer && hasDLFConfiguration(config) {
		diags.AddError("Conflicting authentication configuration", "token_provider = bear cannot be combined with dlf_* configuration. Configure one authentication method.")
	}
	if knownString(config.Token) != "" && (knownString(config.TokenProvider) == client.AuthProviderDLF || hasDLFConfiguration(config)) {
		diags.AddError("Conflicting authentication configuration", "A bearer token cannot be combined with DLF authentication or dlf_* configuration.")
	}
}

type paimonProvider struct {
	version string
}

type paimonProviderModel struct {
	URI                    types.String `tfsdk:"uri"`
	Warehouse              types.String `tfsdk:"warehouse"`
	TokenProvider          types.String `tfsdk:"token_provider"`
	Token                  types.String `tfsdk:"token"`
	DLFRegion              types.String `tfsdk:"dlf_region"`
	DLFSigningAlgorithm    types.String `tfsdk:"dlf_signing_algorithm"`
	DLFAccessKeyID         types.String `tfsdk:"dlf_access_key_id"`
	DLFAccessKeySecret     types.String `tfsdk:"dlf_access_key_secret"`
	DLFSecurityToken       types.String `tfsdk:"dlf_security_token"`
	DLFTokenPath           types.String `tfsdk:"dlf_token_path"`
	DLFTokenLoader         types.String `tfsdk:"dlf_token_loader"`
	DLFECSMetadataURL      types.String `tfsdk:"dlf_ecs_metadata_url"`
	DLFECSRoleName         types.String `tfsdk:"dlf_ecs_role_name"`
	Prefix                 types.String `tfsdk:"prefix"`
	Headers                types.Map    `tfsdk:"headers"`
	RequestTimeoutSeconds  types.Int64  `tfsdk:"request_timeout_seconds"`
	RecoveryTimeoutSeconds types.Int64  `tfsdk:"recovery_timeout_seconds"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &paimonProvider{version: version}
	}
}

func (p *paimonProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "paimon"
	resp.Version = p.version
}

func (p *paimonProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Use Terraform to manage databases, tables, permissions, and data policies through an Apache Paimon REST Catalog.",
		Attributes: map[string]schema.Attribute{
			"request_timeout_seconds": schema.Int64Attribute{
				Description: "Timeout for each REST or metadata HTTP request, in seconds. Defaults to 30. Valid values: 1 to 3600.",
				Optional:    true,
				Validators:  []validator.Int64{int64validator.Between(1, 3600)},
			},
			"recovery_timeout_seconds": schema.Int64Attribute{
				Description: "Maximum reconciliation time after a mutation, in seconds. Defaults to 5. Set this to cover the catalog's visibility delay. Valid values: 1 to 3600.",
				Optional:    true,
				Validators:  []validator.Int64{int64validator.Between(1, 3600)},
			},
			"uri": schema.StringAttribute{
				Description: "Base URI of the Paimon REST Catalog server.",
				Required:    true,
			},
			"warehouse": schema.StringAttribute{
				Description: "Optional warehouse identifier sent to the REST Catalog /v1/config endpoint.",
				Optional:    true,
			},
			"token_provider": schema.StringAttribute{
				Description: "Authentication provider. Use bear for a bearer token or dlf for Alibaba Cloud DLF AK/STS request signing. When omitted, it is inferred from the configured credentials.",
				Optional:    true,
				Validators:  []validator.String{stringvalidator.OneOf(client.AuthProviderBearer, client.AuthProviderDLF)},
			},
			"token": schema.StringAttribute{
				Description: "Bearer token used when token_provider is bear.",
				Optional:    true,
				Sensitive:   true,
			},
			"dlf_region": schema.StringAttribute{
				Description: "Alibaba Cloud region used by DLF default signing. It is inferred from standard DLF endpoints when omitted.",
				Optional:    true,
			},
			"dlf_signing_algorithm": schema.StringAttribute{
				Description: "DLF signing algorithm: default for DLF VPC/default endpoints or openapi for DLFNext endpoints. It is inferred from the endpoint when omitted.",
				Optional:    true,
				Validators:  []validator.String{stringvalidator.OneOf(client.DLFSigningDefault, client.DLFSigningOpenAPI)},
			},
			"dlf_access_key_id": schema.StringAttribute{
				Description: "Alibaba Cloud access key ID for static DLF AK/STS authentication.",
				Optional:    true,
				Sensitive:   true,
			},
			"dlf_access_key_secret": schema.StringAttribute{
				Description: "Alibaba Cloud access key secret for static DLF AK/STS authentication.",
				Optional:    true,
				Sensitive:   true,
			},
			"dlf_security_token": schema.StringAttribute{
				Description: "Optional Alibaba Cloud STS security token used with the static access key pair.",
				Optional:    true,
				Sensitive:   true,
			},
			"dlf_token_path": schema.StringAttribute{
				Description: "Path to a rotating DLF AK/STS JSON token file. Selecting it implies the local_file loader.",
				Optional:    true,
			},
			"dlf_token_loader": schema.StringAttribute{
				Description: "Dynamic DLF credential loader: local_file or ecs.",
				Optional:    true,
				Validators:  []validator.String{stringvalidator.OneOf(client.DLFTokenLoaderLocalFile, client.DLFTokenLoaderECS)},
			},
			"dlf_ecs_metadata_url": schema.StringAttribute{
				Description: "Optional ECS RAM role metadata endpoint override. Intended for compatible metadata services and testing.",
				Optional:    true,
			},
			"dlf_ecs_role_name": schema.StringAttribute{
				Description: "Optional ECS RAM role name. When omitted, it is discovered from the metadata service.",
				Optional:    true,
			},
			"prefix": schema.StringAttribute{
				Description: "Optional catalog path prefix. Server overrides returned by /v1/config take precedence.",
				Optional:    true,
			},
			"headers": schema.MapAttribute{
				Description: "Additional HTTP headers sent to the REST Catalog. The token attribute takes precedence over an Authorization entry.",
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
			},
		},
	}
}

func (p *paimonProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data paimonProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	validateAuthenticationConfig(data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.URI.IsUnknown() {
		return
	}
	if data.URI.IsNull() || data.URI.ValueString() == "" {
		resp.Diagnostics.AddError("Missing Paimon REST URI", "The provider uri attribute must be configured.")

		return
	}

	headers := make(map[string]string)
	if !data.Headers.IsNull() && !data.Headers.IsUnknown() {
		resp.Diagnostics.Append(data.Headers.ElementsAs(ctx, &headers, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	authProvider := knownString(data.TokenProvider)
	if authProvider == "" {
		switch {
		case hasDLFConfiguration(data):
			authProvider = client.AuthProviderDLF
		case knownString(data.Token) != "":
			authProvider = client.AuthProviderBearer
		}
	}

	var dlfConfig *client.DLFConfig
	if authProvider == client.AuthProviderDLF {
		dlfConfig = &client.DLFConfig{
			Region:           knownString(data.DLFRegion),
			SigningAlgorithm: knownString(data.DLFSigningAlgorithm),
			AccessKeyID:      knownString(data.DLFAccessKeyID),
			AccessKeySecret:  knownString(data.DLFAccessKeySecret),
			SecurityToken:    knownString(data.DLFSecurityToken),
			TokenPath:        knownString(data.DLFTokenPath),
			TokenLoader:      knownString(data.DLFTokenLoader),
			ECSMetadataURL:   knownString(data.DLFECSMetadataURL),
			ECSRoleName:      knownString(data.DLFECSRoleName),
		}
	}

	api, err := client.New(client.Config{
		RequestTimeout:  time.Duration(data.RequestTimeoutSeconds.ValueInt64()) * time.Second,
		RecoveryTimeout: time.Duration(data.RecoveryTimeoutSeconds.ValueInt64()) * time.Second,
		URI:             data.URI.ValueString(),
		Warehouse:       knownString(data.Warehouse),
		AuthProvider:    authProvider,
		Token:           knownString(data.Token),
		DLF:             dlfConfig,
		Prefix:          knownString(data.Prefix),
		Headers:         headers,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Paimon provider configuration", fmt.Sprintf("Unable to create the REST Catalog client: %s", err))

		return
	}

	resp.DataSourceData = api
	resp.ResourceData = api
}

func (p *paimonProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewDatabaseDataSource,
		NewTableDataSource,
	}
}

func (p *paimonProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewDatabaseResource,
		NewTableResource,
		NewPermissionResource,
		NewRowFilterResource,
		NewColumnMaskResource,
	}
}

func knownString(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}

	return value.ValueString()
}

func hasDLFConfiguration(data paimonProviderModel) bool {
	values := []types.String{
		data.DLFRegion,
		data.DLFSigningAlgorithm,
		data.DLFAccessKeyID,
		data.DLFAccessKeySecret,
		data.DLFSecurityToken,
		data.DLFTokenPath,
		data.DLFTokenLoader,
		data.DLFECSMetadataURL,
		data.DLFECSRoleName,
	}
	for _, value := range values {
		if knownString(value) != "" {
			return true
		}
	}

	return false
}
