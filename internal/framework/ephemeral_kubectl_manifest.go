package framework

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/alekc/terraform-provider-kubectl/kubernetes"
)

// manifestEphemeralResource fetches a Kubernetes object from the cluster and
// optionally extracts user-supplied fields. Values produced here never reach
// Terraform state — use this resource type when reading data that must not be
// persisted at rest (Secrets, tokens, certificates pulled live, etc.).
//
// The input/output shape mirrors `data "kubectl_manifest"`. The two share
// the same fetch helper (kubernetes.FetchManifest); the only difference is
// where the result lives.
type manifestEphemeralResource struct {
	kubeProvider *kubernetes.KubeProvider
}

var (
	_ ephemeral.EphemeralResource              = (*manifestEphemeralResource)(nil)
	_ ephemeral.EphemeralResourceWithConfigure = (*manifestEphemeralResource)(nil)
)

func NewManifestEphemeralResource() ephemeral.EphemeralResource {
	return &manifestEphemeralResource{}
}

type manifestEphemeralModel struct {
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

func (r *manifestEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_manifest"
}

func (r *manifestEphemeralResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a single Kubernetes object from the cluster by apiVersion + kind + name (+ namespace) " +
			"and optionally extracts user-supplied fields by dot-path. Values produced by this ephemeral " +
			"resource are never persisted to Terraform state and can only be referenced during apply " +
			"(via write-only attributes on other resources, check blocks, or provisioners). " +
			"For state-persisting reads see the `kubectl_manifest` data source.",
		Attributes: map[string]schema.Attribute{
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
				Description: "Map of result-key to dot-and-bracket path expressions to extract from the fetched object. " +
					"Same grammar as the `kubectl_manifest` data source's `fields`: plain dotted keys, `[N]` for " +
					"array indices, and `[\"key.with.dots\"]` for map keys containing dots or other reserved characters. " +
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

func (r *manifestEphemeralResource) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	kp, ok := req.ProviderData.(*kubernetes.KubeProvider)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected ephemeral resource configuration",
			fmt.Sprintf("expected *kubernetes.KubeProvider from provider data, got %T", req.ProviderData),
		)
		return
	}
	r.kubeProvider = kp
}

func (r *manifestEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data manifestEphemeralModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if r.kubeProvider == nil {
		resp.Diagnostics.AddError(
			"provider not configured",
			"the framework provider's Configure pass must run before the ephemeral resource; this indicates a wiring bug",
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

	result, err := kubernetes.FetchManifest(
		ctx,
		r.kubeProvider,
		data.APIVersion.ValueString(),
		data.Kind.ValueString(),
		data.Name.ValueString(),
		data.Namespace.ValueString(),
		fields,
	)
	if err != nil {
		if errors.Is(err, kubernetes.ErrManifestNotFound) {
			resp.Diagnostics.AddError("kubectl_manifest ephemeral: resource not found", err.Error())
			return
		}
		resp.Diagnostics.AddError("kubectl_manifest ephemeral: read failed", err.Error())
		return
	}

	data.YAML = types.StringValue(result.YAML)
	data.JSON = types.StringValue(result.JSON)
	data.UID = types.StringValue(result.UID)
	resultsMap, diags := types.MapValueFrom(ctx, types.StringType, result.Results)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Results = resultsMap

	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
}
