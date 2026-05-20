package framework

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/alekc/terraform-provider-kubectl/kubernetes"
)

// serverVersionDataSource reads the Kubernetes apiserver version metadata via
// the discovery API. Schema mirrors the SDK v2 data source it replaces; the
// `id` attribute is declared explicitly here because the framework does not
// emit one automatically the way SDK v2 did.
type serverVersionDataSource struct {
	sdkV2Meta func() any
}

var (
	_ datasource.DataSource              = (*serverVersionDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*serverVersionDataSource)(nil)
)

func NewServerVersionDataSource() datasource.DataSource {
	return &serverVersionDataSource{}
}

type serverVersionDataModel struct {
	ID         types.String `tfsdk:"id"`
	Version    types.String `tfsdk:"version"`
	Major      types.String `tfsdk:"major"`
	Minor      types.String `tfsdk:"minor"`
	Patch      types.String `tfsdk:"patch"`
	GitVersion types.String `tfsdk:"git_version"`
	GitCommit  types.String `tfsdk:"git_commit"`
	BuildDate  types.String `tfsdk:"build_date"`
	Platform   types.String `tfsdk:"platform"`
}

func (d *serverVersionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_version"
}

func (d *serverVersionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the Kubernetes apiserver version metadata via the discovery API. " +
			"For a state-persisted variant that re-evaluates only on trigger changes, see the " +
			"`kubectl_server_version` resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "sha256 of the apiserver's reported version string.",
			},
			"version":     schema.StringAttribute{Computed: true},
			"major":       schema.StringAttribute{Computed: true},
			"minor":       schema.StringAttribute{Computed: true},
			"patch":       schema.StringAttribute{Computed: true},
			"git_version": schema.StringAttribute{Computed: true},
			"git_commit":  schema.StringAttribute{Computed: true},
			"build_date":  schema.StringAttribute{Computed: true},
			"platform":    schema.StringAttribute{Computed: true},
		},
	}
}

func (d *serverVersionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cb, ok := req.ProviderData.(func() any)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected data source configuration",
			fmt.Sprintf("expected func() any from provider data, got %T", req.ProviderData),
		)
		return
	}
	d.sdkV2Meta = cb
}

func (d *serverVersionDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.sdkV2Meta == nil {
		resp.Diagnostics.AddError(
			"provider not configured",
			"the SDK v2 provider must configure before the data source can run; this indicates a mux wiring bug",
		)
		return
	}
	provider, ok := d.sdkV2Meta().(*kubernetes.KubeProvider)
	if !ok {
		resp.Diagnostics.AddError(
			"provider type mismatch",
			fmt.Sprintf("expected *kubernetes.KubeProvider from SDKv2Meta, got %T", d.sdkV2Meta()),
		)
		return
	}

	info, err := kubernetes.FetchServerVersion(provider)
	if err != nil {
		resp.Diagnostics.AddError("kubectl_server_version: discovery failed", err.Error())
		return
	}

	data := serverVersionDataModel{
		ID:         types.StringValue(info.ID),
		Version:    types.StringValue(info.Version),
		Major:      types.StringValue(info.Major),
		Minor:      types.StringValue(info.Minor),
		Patch:      types.StringValue(info.Patch),
		GitVersion: types.StringValue(info.GitVersion),
		GitCommit:  types.StringValue(info.GitCommit),
		BuildDate:  types.StringValue(info.BuildDate),
		Platform:   types.StringValue(info.Platform),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
