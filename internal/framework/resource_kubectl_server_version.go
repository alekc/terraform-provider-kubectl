package framework

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/alekc/terraform-provider-kubectl/kubernetes"
)

// serverVersionResource mirrors the SDK v2 `kubectl_server_version` resource.
// The resource exists as a refresh-on-trigger pattern: any change to the
// `triggers` map forces a destroy + recreate, which refetches the apiserver
// version into state. All other attributes are Computed.
type serverVersionResource struct {
	sdkV2Meta func() any
}

var (
	_ resource.Resource              = (*serverVersionResource)(nil)
	_ resource.ResourceWithConfigure = (*serverVersionResource)(nil)
)

func NewServerVersionResource() resource.Resource {
	return &serverVersionResource{}
}

type serverVersionResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Triggers   types.Map    `tfsdk:"triggers"`
	Version    types.String `tfsdk:"version"`
	Major      types.String `tfsdk:"major"`
	Minor      types.String `tfsdk:"minor"`
	Patch      types.String `tfsdk:"patch"`
	GitVersion types.String `tfsdk:"git_version"`
	GitCommit  types.String `tfsdk:"git_commit"`
	BuildDate  types.String `tfsdk:"build_date"`
	Platform   types.String `tfsdk:"platform"`
}

func (r *serverVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_version"
}

func (r *serverVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Stores the Kubernetes apiserver version metadata in Terraform state. " +
			"Useful as a dependency target for resources that should re-evaluate when the " +
			"cluster version changes (set a `triggers` value derived from your cluster " +
			"identifier). For ad-hoc reads use the `kubectl_server_version` data source " +
			"instead.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "sha256 of the apiserver's reported version string.",
			},
			"triggers": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Arbitrary map; any change forces re-creation and refreshes the " +
					"version fields. Use this to tie the resource to your cluster identity.",
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
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

func (r *serverVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cb, ok := req.ProviderData.(func() any)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected resource configuration",
			fmt.Sprintf("expected func() any from provider data, got %T", req.ProviderData),
		)
		return
	}
	r.sdkV2Meta = cb
}

func (r *serverVersionResource) fetch(ctx context.Context, data *serverVersionResourceModel) error {
	if r.sdkV2Meta == nil {
		return fmt.Errorf("provider not configured: SDK v2 meta missing (mux wiring bug)")
	}
	provider, ok := r.sdkV2Meta().(*kubernetes.KubeProvider)
	if !ok {
		return fmt.Errorf("provider type mismatch: expected *kubernetes.KubeProvider, got %T", r.sdkV2Meta())
	}
	info, err := kubernetes.FetchServerVersion(provider)
	if err != nil {
		return err
	}
	data.ID = types.StringValue(info.ID)
	data.Version = types.StringValue(info.Version)
	data.Major = types.StringValue(info.Major)
	data.Minor = types.StringValue(info.Minor)
	data.Patch = types.StringValue(info.Patch)
	data.GitVersion = types.StringValue(info.GitVersion)
	data.GitCommit = types.StringValue(info.GitCommit)
	data.BuildDate = types.StringValue(info.BuildDate)
	data.Platform = types.StringValue(info.Platform)
	return nil
}

func (r *serverVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data serverVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.fetch(ctx, &data); err != nil {
		resp.Diagnostics.AddError("kubectl_server_version: discovery failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serverVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data serverVersionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.fetch(ctx, &data); err != nil {
		resp.Diagnostics.AddError("kubectl_server_version: discovery failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serverVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// triggers carries RequiresReplace and every other attribute is Computed,
	// so the only path that reaches Update is a no-op refresh. Re-fetch so
	// the post-Update state matches a fresh Read.
	var data serverVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.fetch(ctx, &data); err != nil {
		resp.Diagnostics.AddError("kubectl_server_version: discovery failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serverVersionResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// Nothing to delete on the cluster; the framework removes the state entry
	// for us when this method returns without diagnostics.
}
