package framework

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/datasource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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
	kubeProvider *kubernetes.KubeProvider
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
	WaitFor    types.List   `tfsdk:"wait_for"`

	YAML     types.String   `tfsdk:"yaml"`
	JSON     types.String   `tfsdk:"json"`
	UID      types.String   `tfsdk:"uid"`
	Results  types.Map      `tfsdk:"results"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
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
func (d *manifestDataSource) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a single Kubernetes object from the cluster by apiVersion + kind + name (+ namespace) " +
			"and optionally extracts user-supplied fields by dot-path. " +
			"Outputs are not marked sensitive at the schema level; callers needing redaction should set " +
			"`sensitive = true` on the consuming output block or wrap references with `sensitive(...)`. " +
			"For guaranteed non-persistence to state, use the `kubectl_manifest` ephemeral resource instead. " +
			"When `wait_for` is set, the read blocks until the object exists AND any supplied `field` / " +
			"`condition` predicates match, bounded by `timeouts.read` (default 5m). See issue #179.",
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
				Description: "Map of result-key to dot-and-bracket path expressions to extract from the fetched " +
					"object. Plain dotted keys (`replicas = \"spec.replicas\"`), array indices via " +
					"`[N]`, `.[N]`, or bare `.N` (`image = \"spec.containers[0].image\"`), and quoted " +
					"bracketed segments for keys containing dots, slashes, or other reserved characters " +
					"(`app = \"metadata.labels[\\\"app.kubernetes.io/name\\\"]\"`). Each path must " +
					"resolve; missing paths produce an error naming the offending key.",
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
		Blocks: map[string]schema.Block{
			"wait_for": schema.ListNestedBlock{
				Description: "Block the read until the target object exists and any supplied " +
					"predicates match. Mirrors the `kubectl_manifest` resource's `wait_for` " +
					"shape: `field` blocks compare dot-and-bracket path values; `condition` " +
					"blocks match `status.conditions[type=X,status=Y]` entries. A single " +
					"wait_for block is supported; multiple are rejected.",
				Validators: []validator.List{
					listvalidator.SizeAtMost(1),
				},
				NestedObject: schema.NestedBlockObject{
					Blocks: map[string]schema.Block{
						"condition": schema.ListNestedBlock{
							NestedObject: schema.NestedBlockObject{
								Attributes: map[string]schema.Attribute{
									"type": schema.StringAttribute{
										Required:    true,
										Description: "The .status.conditions[].type to match.",
									},
									"status": schema.StringAttribute{
										Required:    true,
										Description: "The .status.conditions[].status value to wait for (typically `True`).",
									},
								},
							},
						},
						"field": schema.ListNestedBlock{
							NestedObject: schema.NestedBlockObject{
								Attributes: map[string]schema.Attribute{
									"key": schema.StringAttribute{
										Required:    true,
										Description: "Dot-and-bracket path into the live object (e.g. `status.phase` or `metadata.labels[\"app.kubernetes.io/name\"]`).",
									},
									"value": schema.StringAttribute{
										Required:    true,
										Description: "Expected value at `key`. Compared as a string.",
									},
									"value_type": schema.StringAttribute{
										Optional:    true,
										Computed:    true,
										Description: "How to compare `value`: `eq` for equality (default) or `regex` for a regular-expression match.",
										Validators: []validator.String{
											stringOneOfValidator{allowed: []string{"eq", "regex"}},
										},
									},
								},
							},
						},
					},
				},
			},
			"timeouts": timeouts.Block(ctx),
		},
	}
}

// Configure caches the *kubernetes.KubeProvider produced by the framework
// provider's Configure pass so Read can use it directly without callback
// indirection. Implements datasource.DataSourceWithConfigure.
func (d *manifestDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	kp, ok := req.ProviderData.(*kubernetes.KubeProvider)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected data source configuration",
			fmt.Sprintf("expected *kubernetes.KubeProvider from provider data, got %T", req.ProviderData),
		)
		return
	}
	d.kubeProvider = kp
}

// Read fetches the target object from the cluster via
// kubernetes.FetchManifest, extracts any caller-supplied field paths, and
// writes the result to state. Errors surface ErrManifestNotFound as a
// distinct diagnostic so callers can pattern-match the "not found" case
// without parsing free-form text. Implements datasource.DataSource.
func (d *manifestDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if deferIfConfigUnknown(req, resp) {
		return
	}

	var data manifestDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if d.kubeProvider == nil {
		resp.Diagnostics.AddError(
			"provider not configured",
			"the framework provider's Configure pass must run before the data source; this indicates a wiring bug",
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

	// If the user declared a wait_for block (with or without
	// inner field/condition predicates), block on
	// WaitForManifest before fetching. An empty wait_for block
	// means "wait until the object exists, no predicate
	// checks". The timeouts block (Read) bounds both phases
	// (existence poll + condition watch) of the helper.
	if !data.WaitFor.IsNull() && !data.WaitFor.IsUnknown() {
		waitFor, waitDiags := extractWaitForBlock(ctx, data.WaitFor, waitForSurfaceDataSource)
		resp.Diagnostics.Append(waitDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		timeout, timeoutDiags := data.Timeouts.Read(ctx, kubernetes.DefaultManifestWaitTimeout)
		resp.Diagnostics.Append(timeoutDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		// Defence-in-depth coercion. The plugin-framework
		// timeouts package parses durations during Config decode
		// and rejects non-positive literals at plan time, so under
		// normal use this branch is unreachable. We keep it (and
		// degrade to the package default with a warning instead of
		// hard-failing) so a future schema regression or a caller
		// that constructs Timeouts programmatically cannot
		// silently feed an already-cancelled context to Phase A.
		// Not unit-tested because the only way to drive it from a
		// public entry point is to bypass the validator, which a
		// future reader should NOT do just to satisfy coverage.
		if timeout <= 0 {
			resp.Diagnostics.AddWarning(
				"kubectl_manifest: non-positive timeouts.read, using default",
				fmt.Sprintf("timeouts.read = %v is not a valid wait duration; falling back to %v.", timeout, kubernetes.DefaultManifestWaitTimeout),
			)
			timeout = kubernetes.DefaultManifestWaitTimeout
		}
		if err := kubernetes.WaitForManifest(ctx, d.kubeProvider, kubernetes.WaitForManifestOptions{
			APIVersion: apiVersion,
			Kind:       kind,
			Name:       name,
			Namespace:  namespace,
			WaitFor:    waitFor,
			Timeout:    timeout,
		}); err != nil {
			resp.Diagnostics.AddError("kubectl_manifest: wait_for did not complete", err.Error())
			return
		}
	}

	result, err := kubernetes.FetchManifest(ctx, d.kubeProvider, apiVersion, kind, name, namespace, fields)
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

	// `wait_for` and `timeouts` are inputs only: leave them on
	// the model unchanged so resp.State.Set round-trips the
	// user's configuration verbatim. The framework does not
	// require populating Computed-only branches we did not
	// declare.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

