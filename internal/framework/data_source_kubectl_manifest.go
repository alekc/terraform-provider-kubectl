package framework

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/alekc/terraform-provider-kubectl/kubernetes"
)

// manifestDataSource fetches a single Kubernetes object from the cluster and
// optionally extracts user-supplied fields by dot-path. Results are persisted
// to Terraform state, so callers handling sensitive data should mark the
// consuming output sensitive or use the kubectl_manifest ephemeral resource
// instead. The input/output shape mirrors the ephemeral sibling; both share
// the kubernetes.FetchManifest helper.
type manifestDataSource struct {
	sdkV2Meta func() any
}

var (
	_ datasource.DataSource              = (*manifestDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*manifestDataSource)(nil)
)

// NewManifestDataSource is the constructor registered on the framework
// provider's DataSources list.
func NewManifestDataSource() datasource.DataSource {
	return &manifestDataSource{}
}

type manifestDataModel struct {
	ID         types.String `tfsdk:"id"`
	APIVersion types.String `tfsdk:"api_version"`
	Kind       types.String `tfsdk:"kind"`
	Name       types.String `tfsdk:"name"`
	Namespace  types.String `tfsdk:"namespace"`
	Fields     types.Map    `tfsdk:"fields"`

	YAML    types.String `tfsdk:"yaml"`
	JSON    types.String `tfsdk:"json"`
	UID     types.String `tfsdk:"uid"`
	Results types.Map    `tfsdk:"results"`
}

// Metadata sets the data source type name. Implements
// datasource.DataSource.
func (d *manifestDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_manifest"
}

// Schema declares the data source's input and output attributes. Mirrors
// the SDK v2 schema this data source replaced after Phase E (#296) so
// existing state files round-trip without a forced replace. Implements
// datasource.DataSource.
func (d *manifestDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a single Kubernetes object from the cluster by apiVersion + kind + name (+ namespace) " +
			"and optionally extracts user-supplied fields by dot-path. " +
			"Outputs are not marked sensitive at the schema level; callers needing redaction should set " +
			"`sensitive = true` on the consuming output block or wrap references with `sensitive(...)`. " +
			"For guaranteed non-persistence to state, use the `kubectl_manifest` ephemeral resource instead.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				Description: "Deterministic identifier of the fetched object in the form " +
					"`<apiVersion>/<namespace>/<kind>/<name>`. Cluster-scoped kinds collapse the namespace " +
					"segment to empty.",
			},
			"api_version": schema.StringAttribute{
				Required:    true,
				Description: "The API version of the resource to read (e.g. `v1`, `apps/v1`).",
			},
			"kind": schema.StringAttribute{
				Required:    true,
				Description: "The Kind of the resource to read (e.g. `ConfigMap`, `Deployment`).",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The metadata.name of the resource to read.",
			},
			"namespace": schema.StringAttribute{
				Optional: true,
				Description: "The metadata.namespace of the resource. Leave empty for cluster-scoped kinds; " +
					"for namespaced kinds an empty value defaults to `default`.",
			},
			"fields": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Map of result-key to gojsonq dot-path expressions to extract from the fetched " +
					"object (e.g. `replicas = \"spec.replicas\"`, `image = \"spec.template.spec.containers.[0].image\"`). " +
					"Array indices use the `[N]` form, e.g. `containers.[0]`. " +
					"Each path must resolve; missing paths produce an error.",
			},

			"yaml": schema.StringAttribute{
				Computed:    true,
				Description: "The fetched object serialised as YAML.",
			},
			"json": schema.StringAttribute{
				Computed:    true,
				Description: "The fetched object serialised as JSON.",
			},
			"uid": schema.StringAttribute{
				Computed:    true,
				Description: "The metadata.uid of the fetched object.",
			},
			"results": schema.MapAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Map of extracted field values keyed by the names declared in `fields`. " +
					"Scalar values are stringified; objects and arrays are JSON-encoded.",
			},
		},
	}
}

// Configure receives the muxed provider's late-bound *KubeProvider via the
// shared func() any callback registered in provider.go. Stored on the
// receiver so Read can resolve the client at request time. Implements
// datasource.DataSourceWithConfigure.
func (d *manifestDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read fetches the target object from the cluster via
// kubernetes.FetchManifest, extracts any caller-supplied field paths, and
// writes the result to state. Errors surface ErrManifestNotFound as a
// distinct diagnostic so callers can pattern-match the "not found" case
// without parsing free-form text. Implements datasource.DataSource.
func (d *manifestDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data manifestDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

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

	fields := map[string]string{}
	if !data.Fields.IsNull() && !data.Fields.IsUnknown() {
		raw := map[string]string{}
		resp.Diagnostics.Append(data.Fields.ElementsAs(ctx, &raw, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		fields = raw
	}

	apiVersion := data.APIVersion.ValueString()
	kind := data.Kind.ValueString()
	name := data.Name.ValueString()
	namespace := data.Namespace.ValueString()

	result, err := kubernetes.FetchManifest(ctx, provider, apiVersion, kind, name, namespace, fields)
	if err != nil {
		if errors.Is(err, kubernetes.ErrManifestNotFound) {
			resp.Diagnostics.AddError("kubectl_manifest: resource not found", err.Error())
			return
		}
		resp.Diagnostics.AddError("kubectl_manifest: read failed", err.Error())
		return
	}

	data.ID = types.StringValue(kubernetes.BuildSelfLinkID(apiVersion, namespace, kind, name))
	data.YAML = types.StringValue(result.YAML)
	data.JSON = types.StringValue(result.JSON)
	data.UID = types.StringValue(result.UID)
	resultsMap, diags := types.MapValueFrom(ctx, types.StringType, result.Results)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Results = resultsMap

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
