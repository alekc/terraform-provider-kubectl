package framework

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	internaltypes "github.com/alekc/terraform-provider-kubectl/internal/types"
	"github.com/alekc/terraform-provider-kubectl/kubernetes"
	"github.com/alekc/terraform-provider-kubectl/yaml"
)

// manifestResource is the plugin-framework port of the SDK v2
// `kubectl_manifest` resource. The schema, semantics, and state shape are
// designed to be byte-compatible with the SDK v2 implementation so existing
// users see no state churn after the cutover. See the Phase D plan in
// work/standalone/terraform-provider-kubectl-123/plan.md and ADR-0001.
//
// CRUD methods delegate to kubernetes.ApplyManifest / ReadManifest /
// DeleteManifest (the SDK-v2-agnostic helpers extracted in Phase D.0).
// Plan-time concerns (CustomizeDiff, DiffSuppressFunc, UpgradeState,
// MoveState) land in follow-up commits on this branch; until the atomic
// cutover commit, the resource is NOT registered in provider.Resources()
// so the SDK v2 implementation remains the live one.
type manifestResource struct {
	kubeProviderCache *kubernetes.KubeProvider
}

var (
	_ resource.Resource                 = (*manifestResource)(nil)
	_ resource.ResourceWithConfigure    = (*manifestResource)(nil)
	_ resource.ResourceWithModifyPlan   = (*manifestResource)(nil)
	_ resource.ResourceWithUpgradeState = (*manifestResource)(nil)
	_ resource.ResourceWithImportState  = (*manifestResource)(nil)
)

// NewManifestResource is the constructor registered in
// kubectlFrameworkProvider.Resources() at cutover time.
func NewManifestResource() resource.Resource {
	return &manifestResource{}
}

// manifestResourceModel mirrors the SDK v2 schema. Field ordering matches
// the SDK v2 file for diff review. The tfsdk tags are the source of truth
// for attribute names.
type manifestResourceModel struct {
	ID                    types.String   `tfsdk:"id"`
	UID                   types.String   `tfsdk:"uid"`
	LiveUID               types.String   `tfsdk:"live_uid"`
	YAMLInCluster         types.String   `tfsdk:"yaml_incluster"`
	LiveManifestInCluster types.String   `tfsdk:"live_manifest_incluster"`
	APIVersion            types.String   `tfsdk:"api_version"`
	Kind                  types.String   `tfsdk:"kind"`
	Name                  types.String   `tfsdk:"name"`
	Namespace             types.String   `tfsdk:"namespace"`
	OverrideNamespace     types.String   `tfsdk:"override_namespace"`
	YAMLBody              types.String   `tfsdk:"yaml_body"`
	YAMLBodyParsed        types.String   `tfsdk:"yaml_body_parsed"`
	SensitiveFields       types.List     `tfsdk:"sensitive_fields"`
	ForceNew              types.Bool     `tfsdk:"force_new"`
	UpgradeAPIVersion     types.Bool     `tfsdk:"upgrade_api_version"`
	ServerSideApply       types.Bool     `tfsdk:"server_side_apply"`
	FieldManager          types.String   `tfsdk:"field_manager"`
	ForceConflicts        types.Bool     `tfsdk:"force_conflicts"`
	ApplyOnly             types.Bool     `tfsdk:"apply_only"`
	IgnoreFields          types.List     `tfsdk:"ignore_fields"`
	Wait                  types.Bool     `tfsdk:"wait"`
	WaitForRollout        types.Bool     `tfsdk:"wait_for_rollout"`
	ValidateSchema        types.Bool     `tfsdk:"validate_schema"`
	DeleteCascade         types.String   `tfsdk:"delete_cascade"`
	WaitFor               types.List     `tfsdk:"wait_for"`
	Timeouts              timeouts.Value `tfsdk:"timeouts"`
}

// waitForBlockModel is the inline shape of a single wait_for block.
type waitForBlockModel struct {
	Condition []waitForConditionModel `tfsdk:"condition"`
	Field     []waitForFieldModel     `tfsdk:"field"`
}

type waitForConditionModel struct {
	Type   types.String `tfsdk:"type"`
	Status types.String `tfsdk:"status"`
}

type waitForFieldModel struct {
	Key       types.String `tfsdk:"key"`
	Value     types.String `tfsdk:"value"`
	ValueType types.String `tfsdk:"value_type"`
}

// defaultLifecycleTimeout is used for Create / Update / Delete until the
// timeouts {} block plumbing lands (separate Phase D follow-up commit).
const defaultLifecycleTimeout = 10 * time.Minute

// Metadata sets the Terraform type name for this resource to
// "<provider>_manifest" (e.g. "kubectl_manifest"). Implements
// resource.Resource.
func (r *manifestResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_manifest"
}

// Schema returns the resource schema (Version 1) for kubectl_manifest.
// The attribute set, types, and default values are byte-compatible with
// the SDK v2 implementation so existing state round-trips without churn
// after the cutover. Implements resource.Resource.
func (r *manifestResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: 1,
		Description: "Apply a Kubernetes manifest from raw YAML. Tracks the live resource by UID; reapplies " +
			"on drift; surfaces full apply semantics (server-side apply, field manager, force conflicts, " +
			"wait-for-rollout, wait_for conditions). Cross-provider state move from `gavinbunney/kubectl` " +
			"is supported via `moved {}` blocks; see README \"Migrating from gavinbunney/kubectl\".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Self-link of the Kubernetes object, used as the Terraform resource ID. Carries through unchanged from SDK v2 state for round-trip compatibility.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"uid": schema.StringAttribute{
				Computed:    true,
				Description: "UID of the Kubernetes object at the time of the last apply.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"live_uid": schema.StringAttribute{
				Computed:    true,
				Description: "UID of the Kubernetes object as observed during the most recent Read.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"yaml_incluster": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Fingerprint of the canonical YAML at the time of the last apply. Drift detection compares this to live_manifest_incluster.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"live_manifest_incluster": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Fingerprint of the canonical YAML as observed during the most recent Read.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"api_version": schema.StringAttribute{
				Computed: true,
				Description: "apiVersion of the manifest, derived from `yaml_body`. By default a change to this " +
					"value re-creates the resource; set `upgrade_api_version = true` to allow an in-place update.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"kind": schema.StringAttribute{
				Computed:    true,
				Description: "kind of the manifest, derived from `yaml_body`. Changing it forces resource replacement.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Computed:    true,
				Description: "metadata.name of the manifest, derived from `yaml_body`. Changing it forces resource replacement.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"namespace": schema.StringAttribute{
				Computed:    true,
				Description: "metadata.namespace of the manifest. Changing it forces resource replacement.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"override_namespace": schema.StringAttribute{
				Optional:    true,
				Description: "Override the namespace declared in `yaml_body`. Useful when applying a generic manifest into a parameterised namespace.",
			},
			"yaml_body": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "The Kubernetes manifest as a YAML string. Multi-document YAML is not supported on this resource; use `kubectl_path_documents` or a `for_each` with `kubectl_file_documents` to fan out.",
			},
			"yaml_body_parsed": schema.StringAttribute{
				Computed:    true,
				Description: "The YAML body as applied to the cluster, with any field listed in `sensitive_fields` replaced by `(sensitive value)`. Surfaced for plan-output review without leaking secret values.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"sensitive_fields": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Dot-paths into the manifest whose values should be masked in `yaml_body_parsed`. For `Kind: Secret` resources, defaults to `[\"data\", \"stringData\"]` when unset.",
			},
			"force_new": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "When true, any change to `yaml_body` re-creates the resource instead of updating it in place. Default false.",
			},
			"upgrade_api_version": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "When true, a change to `api_version` is applied as an in-place update rather than a delete-and-recreate. Relies on the Kubernetes API server's ability to represent the same object across multiple API versions. Default false.",
			},
			"server_side_apply": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "When true, use Kubernetes server-side apply instead of the default client-side apply. Pairs with `field_manager` and `force_conflicts`.",
			},
			"field_manager": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("kubectl"),
				Description: "Override the field manager name for server-side apply. Only consulted when `server_side_apply = true`. Default `kubectl`.",
			},
			"force_conflicts": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "When true, server-side apply takes ownership of conflicting fields. Only consulted when `server_side_apply = true`. Default false.",
			},
			"apply_only": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "When true, Delete is a no-op. Useful for resources that other systems own the lifecycle of but Terraform still asserts the spec. Default false.",
			},
			"ignore_fields": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Dot-paths into the manifest whose drift should be ignored. Set for fields managed by Operators or other controllers that mutate values after apply.",
			},
			"wait": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "When true, Delete blocks until the resource is gone from the cluster (Foreground propagation). Default false.",
			},
			"wait_for_rollout": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, Apply blocks until rollout completes for Deployment / DaemonSet / StatefulSet / APIService kinds. Default true.",
			},
			"validate_schema": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "When true, validate the YAML against the cluster's OpenAPI schema before applying. Default true.",
			},
			"delete_cascade": schema.StringAttribute{
				Optional:    true,
				Description: "Propagation policy for Delete. One of `Background` or `Foreground`. When unset, defaults to `Foreground` if `wait = true`, otherwise `Background`.",
				Validators: []validator.String{
					stringOneOfValidator{allowed: []string{"Background", "Foreground"}},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"wait_for": schema.ListNestedBlock{
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
										Description: "Dot-path into the live object (e.g. `status.phase`).",
									},
									"value": schema.StringAttribute{
										Required:    true,
										Description: "Expected value at `key`. Compared as a string.",
									},
									"value_type": schema.StringAttribute{
										Optional:    true,
										Computed:    true,
										Default:     stringdefault.StaticString("eq"),
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
				Description: "Wait for cluster-side conditions or field values after Apply. A single block is supported; nested `condition` and `field` blocks combine (all must be satisfied) before the apply completes. MaxItems = 1 is not enforced in the schema (framework limitation) and is checked in ModifyPlan instead.",
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

// Configure caches the *kubernetes.KubeProvider produced by the framework
// provider's Configure pass. Resource CRUD methods read it directly via
// kubeProvider() — there is no callback indirection now that the SDK v2
// half is gone (#297). Implements resource.ResourceWithConfigure.
func (r *manifestResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	kp, ok := req.ProviderData.(*kubernetes.KubeProvider)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected resource configuration",
			fmt.Sprintf("expected *kubernetes.KubeProvider from provider data, got %T", req.ProviderData),
		)
		return
	}
	r.kubeProviderCache = kp
}

// kubeProvider returns the cached *kubernetes.KubeProvider, or an error
// if Configure has not yet run. The error path is defensive: the framework
// guarantees Configure precedes CRUD for resources that implement
// ResourceWithConfigure, so reaching it indicates a deeper wiring bug.
func (r *manifestResource) kubeProvider() (*kubernetes.KubeProvider, error) {
	if r.kubeProviderCache == nil {
		return nil, fmt.Errorf("provider not configured: kubeProviderCache unset")
	}
	return r.kubeProviderCache, nil
}

// extractStringList materialises a types.List of strings into a plain
// []string. Null and unknown lists become nil.
func extractStringList(ctx context.Context, list types.List) ([]string, diag.Diagnostics) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}
	var out []string
	d := list.ElementsAs(ctx, &out, false)
	return out, d
}

// extractWaitFor materialises the wait_for ListNestedBlock into a pointer
// to internal/types.WaitFor (nil if the block is absent). Only the first
// block is read; the MaxItems = 1 constraint is enforced upstream in
// ModifyPlan.
func extractWaitFor(ctx context.Context, list types.List) (*internaltypes.WaitFor, diag.Diagnostics) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}
	var blocks []waitForBlockModel
	d := list.ElementsAs(ctx, &blocks, false)
	if d.HasError() || len(blocks) == 0 {
		return nil, d
	}
	b := blocks[0]
	wf := internaltypes.WaitFor{}
	for _, c := range b.Condition {
		wf.Condition = append(wf.Condition, internaltypes.WaitForStatusCondition{
			Type:   c.Type.ValueString(),
			Status: c.Status.ValueString(),
		})
	}
	for _, f := range b.Field {
		wf.Field = append(wf.Field, internaltypes.WaitForField{
			Key:       f.Key.ValueString(),
			Value:     f.Value.ValueString(),
			ValueType: f.ValueType.ValueString(),
		})
	}
	return &wf, d
}

// buildApplyOptions constructs an ApplyManifestOptions struct from the
// plan model. Shared between Create and Update. The timeout argument
// is resolved by the caller (Create vs Update each pulls a different
// timeouts attribute and falls back to defaultLifecycleTimeout) so the
// helper does not need to know which lifecycle phase is running.
// Returns any decoding diagnostics so the caller can short-circuit on
// error.
func (r *manifestResource) buildApplyOptions(ctx context.Context, data manifestResourceModel, timeout time.Duration) (kubernetes.ApplyManifestOptions, diag.Diagnostics) {
	var allDiags diag.Diagnostics
	waitFor, d := extractWaitFor(ctx, data.WaitFor)
	allDiags.Append(d...)
	ignoreFields, d := extractStringList(ctx, data.IgnoreFields)
	allDiags.Append(d...)
	sensitiveFields, d := extractStringList(ctx, data.SensitiveFields)
	allDiags.Append(d...)
	// Drop empty / whitespace-only entries so a misconfigured
	// sensitive_fields = [""] does not suppress the Secret v1 default
	// masking inside BuildObfuscatedYAML.
	sensitiveFields = kubernetes.NormalizeSensitiveFields(sensitiveFields)
	return kubernetes.ApplyManifestOptions{
		YAMLBody:          data.YAMLBody.ValueString(),
		OverrideNamespace: data.OverrideNamespace.ValueString(),
		ValidateSchema:    boolOrTrue(data.ValidateSchema),
		ServerSideApply:   data.ServerSideApply.ValueBool(),
		FieldManager:      stringOrDefault(data.FieldManager, "kubectl"),
		ForceConflicts:    data.ForceConflicts.ValueBool(),
		WaitForRollout:    boolOrTrue(data.WaitForRollout),
		WaitFor:           waitFor,
		Timeout:           timeout,
		IgnoreFields:      ignoreFields,
		SensitiveFields:   sensitiveFields,
	}, allDiags
}

// applyResultToModel writes the ApplyManifest output values onto the
// resource model.
func applyResultToModel(result *kubernetes.ApplyManifestResult, data *manifestResourceModel) {
	data.ID = types.StringValue(result.SelfLink)
	data.UID = types.StringValue(result.UID)
	data.LiveUID = types.StringValue(result.LiveUID)
	data.YAMLInCluster = types.StringValue(result.YAMLInClusterFingerprint)
	data.LiveManifestInCluster = types.StringValue(result.LiveManifestInClusterFingerprint)
}

// Create applies the manifest to the cluster and persists the resulting
// fingerprints (uid, live_uid, yaml_incluster, live_manifest_incluster)
// to state. Delegates to kubernetes.ApplyManifest, so behaviour matches
// the SDK v2 half line-for-line. Implements resource.Resource.
func (r *manifestResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data manifestResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	provider, err := r.kubeProvider()
	if err != nil {
		resp.Diagnostics.AddError("kubectl_manifest Create: provider unavailable", err.Error())
		return
	}

	timeout, d := data.Timeouts.Create(ctx, defaultLifecycleTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The terraform-plugin-framework-timeouts package returns the
	// duration only; unlike SDK v2's Resource.Timeouts the framework
	// does NOT auto-apply that timeout to ctx. Wrap explicitly so
	// downstream code (WaitForRollout, WaitForConditions, etc.) that
	// loops on ctx.Done() observes the user-configured deadline.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts, d := r.buildApplyOptions(ctx, data, timeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, applyErr := kubernetes.ApplyManifest(ctx, provider, opts)
	if applyErr != nil {
		resp.Diagnostics.AddError("kubectl_manifest Create: apply failed", applyErr.Error())
		return
	}

	applyResultToModel(result, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Read refreshes live_uid and live_manifest_incluster from the cluster.
// If the resource has disappeared (ReadManifest reports !Found, or the
// REST mapper rejects the kind), the resource is removed from state so
// the next plan recreates it. Implements resource.Resource.
func (r *manifestResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data manifestResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	provider, err := r.kubeProvider()
	if err != nil {
		resp.Diagnostics.AddError("kubectl_manifest Read: provider unavailable", err.Error())
		return
	}

	ignoreFields, d := extractStringList(ctx, data.IgnoreFields)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := kubernetes.ReadManifestOptions{
		YAMLBody:          data.YAMLBody.ValueString(),
		OverrideNamespace: data.OverrideNamespace.ValueString(),
		IgnoreFields:      ignoreFields,
	}

	result, readErr := kubernetes.ReadManifest(ctx, provider, opts)
	if readErr != nil {
		resp.Diagnostics.AddError("kubectl_manifest Read: read failed", readErr.Error())
		return
	}
	if result.InvalidType || !result.Found {
		// Resource no longer exists in the cluster; remove from state by
		// not setting it again. The framework treats unset state as removed.
		resp.State.RemoveResource(ctx)
		return
	}

	data.LiveUID = types.StringValue(result.LiveUID)
	data.LiveManifestInCluster = types.StringValue(result.LiveManifestInClusterFingerprint)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Update reapplies the manifest in place. The shared kubernetes.ApplyManifest
// helper is idempotent, so Create and Update share the same code path; the
// only difference is which framework request type the plan arrives on.
//
// On any error before or during the apply, the prior state captured from
// req.State is restored verbatim to resp.State. Without this, a failed
// apply would leave Terraform thinking the new yaml_body has landed
// (because resp.State.Set was the only state-mutating call and we'd
// return without it), and the next plan would compare config to the
// planned-but-unapplied state and report no changes (#60). HashiCorp's
// framework guidance is explicit on this: providers must reset state to
// the prior value on error rather than relying on a default. Implements
// resource.Resource.
func (r *manifestResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var priorData manifestResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &priorData)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var data manifestResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	provider, err := r.kubeProvider()
	if err != nil {
		resp.Diagnostics.AddError("kubectl_manifest Update: provider unavailable", err.Error())
		resp.Diagnostics.Append(resp.State.Set(ctx, &priorData)...)
		return
	}

	timeout, d := data.Timeouts.Update(ctx, defaultLifecycleTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &priorData)...)
		return
	}

	// See Create: framework-timeouts returns the duration only, the
	// framework does not auto-apply it to ctx like SDK v2 did.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts, d := r.buildApplyOptions(ctx, data, timeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &priorData)...)
		return
	}

	result, applyErr := kubernetes.ApplyManifest(ctx, provider, opts)
	if applyErr != nil {
		resp.Diagnostics.AddError("kubectl_manifest Update: apply failed", applyErr.Error())
		resp.Diagnostics.Append(resp.State.Set(ctx, &priorData)...)
		return
	}

	applyResultToModel(result, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Delete removes the manifest from the cluster, honouring apply_only,
// wait, and delete_cascade. Delegates to kubernetes.DeleteManifest; with
// apply_only = true the call is a no-op so the resource is simply
// dropped from state. Implements resource.Resource.
func (r *manifestResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data manifestResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	provider, err := r.kubeProvider()
	if err != nil {
		resp.Diagnostics.AddError("kubectl_manifest Delete: provider unavailable", err.Error())
		return
	}

	sensitiveFields, d := extractStringList(ctx, data.SensitiveFields)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	sensitiveFields = kubernetes.NormalizeSensitiveFields(sensitiveFields)

	timeout, td := data.Timeouts.Delete(ctx, defaultLifecycleTimeout)
	resp.Diagnostics.Append(td...)
	if resp.Diagnostics.HasError() {
		return
	}

	// See Create: framework-timeouts returns the duration only, the
	// framework does not auto-apply it to ctx like SDK v2 did.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := kubernetes.DeleteManifestOptions{
		YAMLBody:          data.YAMLBody.ValueString(),
		OverrideNamespace: data.OverrideNamespace.ValueString(),
		ApplyOnly:         data.ApplyOnly.ValueBool(),
		Wait:              data.Wait.ValueBool(),
		DeleteCascade:     data.DeleteCascade.ValueString(),
		Timeout:           timeout,
		SensitiveFields:   sensitiveFields,
	}

	if delErr := kubernetes.DeleteManifest(ctx, provider, opts); delErr != nil {
		resp.Diagnostics.AddError("kubectl_manifest Delete: delete failed", delErr.Error())
		return
	}
}

// ModifyPlan ports the SDK v2 CustomizeDiff logic to the framework's
// plan-modification phase. Responsibilities, in order:
//
//  1. Honour force_new: any yaml_body change replaces the resource.
//  2. Skip when yaml_body is interpolated (Unknown); the framework already
//     leaves Computed attributes Unknown by default.
//  3. Parse the YAML, apply override_namespace, and push the resulting
//     api_version / kind / name / namespace into the plan.
//  4. If upgrade_api_version is false and api_version changed, require
//     replacement.
//  5. UID divergence between state.uid and state.live_uid means the
//     cluster-side object was recreated; mark uid as Unknown so it is
//     refreshed during Apply.
//  6. Drift detection: yaml_incluster vs live_manifest_incluster
//     mismatch means an external change occurred; mark yaml_incluster
//     Unknown.
//  7. Build yaml_body_parsed by obfuscating sensitive_fields (or the
//     default Secret v1 fields) and write it into the plan.
func (r *manifestResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		// Destroy plan; nothing to modify.
		return
	}

	var plan manifestResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ForceNew.ValueBool() {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("yaml_body"))
	}

	// Enforce MaxItems = 1 on wait_for here, since the schema's ListNestedBlock
	// cannot express it (framework limitation). Runs for both create and update
	// plans because ModifyPlan fires for both.
	if !plan.WaitFor.IsNull() && !plan.WaitFor.IsUnknown() && len(plan.WaitFor.Elements()) > 1 {
		resp.Diagnostics.AddAttributeError(
			path.Root("wait_for"),
			"too many wait_for blocks",
			"only a single wait_for block is allowed (MaxItems=1)",
		)
		return
	}

	// Reject force_conflicts = true without server_side_apply = true.
	// kubectl's apply.ApplyOptions.Validate() rejects this combination
	// upstream, but ApplyOptions.Run() (the path the provider uses as
	// a library consumer) never calls Validate(), so on the client-side
	// apply branch ForceConflicts is silently dropped. Catching it
	// here surfaces a clear diagnostic during `terraform plan` rather
	// than at apply time and prevents a no-op flag from masquerading
	// as configured behaviour. Unknown values on either side fall
	// through; the check re-runs once they resolve.
	if !plan.ForceConflicts.IsUnknown() && !plan.ServerSideApply.IsUnknown() &&
		plan.ForceConflicts.ValueBool() && !plan.ServerSideApply.ValueBool() {
		resp.Diagnostics.AddAttributeError(
			path.Root("force_conflicts"),
			"force_conflicts requires server_side_apply",
			"force_conflicts = true has no effect with the default client-side "+
				"apply (server_side_apply = false). Set server_side_apply = true "+
				"if you want server-side apply with conflict overrides, or remove "+
				"force_conflicts.",
		)
		return
	}

	if plan.YAMLBody.IsUnknown() {
		return
	}

	parsed, err := yaml.ParseYAML(plan.YAMLBody.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("yaml_body"),
			"kubectl_manifest plan: failed to parse yaml_body",
			err.Error(),
		)
		return
	}

	overrideNs := plan.OverrideNamespace.ValueString()
	if overrideNs != "" {
		parsed.SetNamespace(overrideNs)
	}

	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("api_version"), parsed.GetAPIVersion())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("kind"), parsed.GetKind())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("name"), parsed.GetName())...)
	// Cluster-scoped kinds (Namespace, ClusterRole, ...) have no
	// metadata.namespace. parsed.GetNamespace() returns "" in that case,
	// but the SDK v2 schema stored a null. Coerce empty string to a typed
	// null so v2.x to v3 state round-trips cleanly (caught by
	// upgrade_path_smoke: Namespace state had namespace=null and the
	// framework rewriting it to "" produced a no-op-looking diff).
	if ns := parsed.GetNamespace(); ns == "" {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("namespace"), types.StringNull())...)
	} else {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("namespace"), ns)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if !req.State.Raw.IsNull() {
		var state manifestResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}

		if !plan.UpgradeAPIVersion.ValueBool() && state.APIVersion.ValueString() != parsed.GetAPIVersion() {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("api_version"))
		}

		if state.LiveUID.ValueString() != state.UID.ValueString() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("uid"), types.StringUnknown())...)
		}

		if state.YAMLInCluster.ValueString() != state.LiveManifestInCluster.ValueString() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("yaml_incluster"), types.StringUnknown())...)
			// Drift-driven applies recompute both fingerprints; pinning
			// live_manifest_incluster to the stale state value triggers
			// the post-apply consistency check when Apply returns the
			// new value.
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("live_manifest_incluster"), types.StringUnknown())...)
		}

		// When inputs that feed Apply differ between state and plan,
		// the computed fingerprints Apply returns cannot be predicted
		// at plan time: yaml_incluster / live_manifest_incluster are
		// GetLiveManifestFields hashes over the new manifest against
		// the post-apply cluster state. The SDK v2 diff engine marked
		// these "(known after apply)" automatically when relevant
		// inputs changed; the framework requires it to be explicit.
		// Without this, the framework's post-apply consistency check
		// raises "Provider produced inconsistent result after apply"
		// whenever Apply's recomputed values differ from what state
		// preserved via UseStateForUnknown.
		inputsChanged := plan.YAMLBody.ValueString() != state.YAMLBody.ValueString() ||
			plan.OverrideNamespace.ValueString() != state.OverrideNamespace.ValueString() ||
			!plan.IgnoreFields.Equal(state.IgnoreFields)
		if inputsChanged {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("yaml_incluster"), types.StringUnknown())...)
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("live_manifest_incluster"), types.StringUnknown())...)
		}

		// id is the self-link (apiVersion + kind + namespace + name) of
		// the applied object. It must go Unknown whenever any of those
		// four identifier components changes, regardless of whether
		// the change happens through the in-place upgrade_api_version
		// path or the force-replace path: in both cases the self-link
		// recomputes during Apply. The earlier gate on
		// plan.UpgradeAPIVersion was wrong for the replace path. The
		// "was known, but now unknown" final-plan inconsistency check
		// still passes because Terraform skips known-to-Unknown
		// validation when the resource is being replaced.
		parsedNamespace := parsed.GetNamespace()
		idChanged := state.APIVersion.ValueString() != parsed.GetAPIVersion() ||
			state.Kind.ValueString() != parsed.GetKind() ||
			state.Name.ValueString() != parsed.GetName() ||
			state.Namespace.ValueString() != parsedNamespace
		if idChanged {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
		}

		if resp.Diagnostics.HasError() {
			return
		}
	}

	sensitiveFields, d := extractStringList(ctx, plan.SensitiveFields)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	obfuscated, obfErr := kubernetes.BuildObfuscatedYAML(plan.YAMLBody.ValueString(), overrideNs, sensitiveFields)
	if obfErr != nil {
		resp.Diagnostics.AddError(
			"kubectl_manifest plan: failed to obfuscate yaml_body",
			obfErr.Error(),
		)
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("yaml_body_parsed"), obfuscated)...)
}

// UpgradeState ports the SDK v2 v0 -> v1 state upgrader. v0 stored the
// raw canonicalised YAML strings in yaml_incluster and
// live_manifest_incluster; v1 stores their sha256 fingerprints. The
// upgrader simply hashes both fields if they look unhashed. The v0
// schema was structurally identical to v1 so we reuse the v1 model for
// decoding; only the value transform changes.
func (r *manifestResource) UpgradeState(_ context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var data manifestResourceModel
				resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
				if resp.Diagnostics.HasError() {
					return
				}
				if !data.YAMLInCluster.IsNull() && !data.YAMLInCluster.IsUnknown() {
					data.YAMLInCluster = types.StringValue(
						kubernetes.GetFingerprint(data.YAMLInCluster.ValueString()))
				}
				if !data.LiveManifestInCluster.IsNull() && !data.LiveManifestInCluster.IsUnknown() {
					data.LiveManifestInCluster = types.StringValue(
						kubernetes.GetFingerprint(data.LiveManifestInCluster.ValueString()))
				}
				resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			},
		},
	}
}

// stringOneOfValidator is a small inline validator mirroring SDK v2's
// validate.StringInSlice. Kept here to avoid pulling in the
// terraform-plugin-framework-validators module for two call sites; if more
// validators are needed in follow-up commits, swap to that module wholesale.
type stringOneOfValidator struct {
	allowed []string
}

// Description returns a one-line plaintext summary of the allowed
// values. Implements validator.String.
func (v stringOneOfValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be one of %v", v.allowed)
}

// MarkdownDescription returns the same summary as Description; no
// Markdown is needed for a list of allowed scalar values. Implements
// validator.String.
func (v stringOneOfValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateString rejects any non-null, non-unknown value that does not
// appear in the allowed slice. Null and unknown pass through so this
// validator composes cleanly with Optional + Computed attributes.
// Implements validator.String.
func (v stringOneOfValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	got := req.ConfigValue.ValueString()
	for _, a := range v.allowed {
		if got == a {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(
		req.Path,
		"invalid value",
		fmt.Sprintf("expected one of %v, got %q", v.allowed, got),
	)
}

// boolOrTrue returns the value of the bool, or true if it is null/unknown
// (used for Optional+Computed booleans whose Default is true).
func boolOrTrue(b types.Bool) bool {
	if b.IsNull() || b.IsUnknown() {
		return true
	}
	return b.ValueBool()
}

// stringOrDefault returns the string value, or fallback if null/unknown.
func stringOrDefault(s types.String, fallback string) string {
	if s.IsNull() || s.IsUnknown() {
		return fallback
	}
	v := s.ValueString()
	if v == "" {
		return fallback
	}
	return v
}

// ImportState parses req.ID as `apiVersion//kind//name[//namespace]`,
// fetches the live object via the dynamic client, and reconstructs the
// resource model. Mirrors the SDK v2 Importer.StateContext that was lost
// when the resource was ported to the plugin framework in #318. The
// "//" delimiter is intentional: apiVersion can contain a single slash
// (e.g. `apps/v1`), so a single-slash split would be ambiguous.
//
// Implements resource.ResourceWithImportState.
func (r *manifestResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	apiVersion, kind, name, namespace, parseErr := parseManifestImportID(req.ID)
	if parseErr != nil {
		resp.Diagnostics.AddError(
			"kubectl_manifest Import: malformed ID",
			parseErr.Error(),
		)
		return
	}

	provider, providerErr := r.kubeProvider()
	if providerErr != nil {
		resp.Diagnostics.AddError(
			"kubectl_manifest Import: provider unavailable",
			providerErr.Error(),
		)
		return
	}

	stub := buildManifestImportYAMLStub(apiVersion, kind, name, namespace)
	manifest, parseStubErr := yaml.ParseYAML(stub)
	if parseStubErr != nil {
		resp.Diagnostics.AddError(
			"kubectl_manifest Import: parse stub failed",
			fmt.Sprintf("failed to parse synthesized YAML for %s/%s/%s: %v", apiVersion, kind, name, parseStubErr),
		)
		return
	}

	// Use the ctx-aware discovery variant (added in PR #324) so a
	// cancelled `terraform import` (Ctrl-C, parent operation timeout)
	// short-circuits the 60s discovery timer instead of hanging on it.
	restClient := kubernetes.GetRestClientFromUnstructuredWithContext(ctx, manifest, provider)
	if restClient.Error != nil {
		resp.Diagnostics.AddError(
			"kubectl_manifest Import: discovery failed",
			fmt.Sprintf("failed to create kubernetes rest client for import of %s/%s/%s: %v", apiVersion, kind, name, restClient.Error),
		)
		return
	}

	rawLive, getErr := restClient.ResourceInterface.Get(ctx, manifest.GetName(), meta_v1.GetOptions{})
	if getErr != nil {
		resp.Diagnostics.AddError(
			"kubectl_manifest Import: get failed",
			fmt.Sprintf("failed to fetch %s/%s/%s from kubernetes: %v", apiVersion, kind, name, getErr),
		)
		return
	}

	live := yaml.NewFromUnstructured(rawLive)
	if live.GetUID() == "" {
		resp.Diagnostics.AddError(
			"kubectl_manifest Import: live object has no UID",
			fmt.Sprintf("fetched %s/%s/%s but the returned object had an empty UID; refusing to import an unidentifiable object", apiVersion, kind, name),
		)
		return
	}

	// Strip the same metadata that the SDK v2 importer stripped, so
	// `yaml_body` represents user-controllable fields only. Without
	// this, the next plan against an unchanged config would see drift
	// on `creationTimestamp`, `resourceVersion`, etc.
	meta_v1_unstruct.RemoveNestedField(live.Raw.Object, "metadata", "creationTimestamp")
	meta_v1_unstruct.RemoveNestedField(live.Raw.Object, "metadata", "resourceVersion")
	meta_v1_unstruct.RemoveNestedField(live.Raw.Object, "metadata", "selfLink")
	meta_v1_unstruct.RemoveNestedField(live.Raw.Object, "metadata", "uid")
	meta_v1_unstruct.RemoveNestedField(live.Raw.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	if len(live.Raw.GetAnnotations()) == 0 {
		meta_v1_unstruct.RemoveNestedField(live.Raw.Object, "metadata", "annotations")
	}

	yamlStripped, renderErr := live.AsYAML()
	if renderErr != nil {
		resp.Diagnostics.AddError(
			"kubectl_manifest Import: render YAML failed",
			fmt.Sprintf("failed to convert live %s/%s/%s to yaml: %v", apiVersion, kind, name, renderErr),
		)
		return
	}

	// Reuse the same fingerprint helper Create uses, with no ignore
	// list (the user has no yaml_body yet to compare against).
	fingerprint := kubernetes.GetLiveManifestFields(nil, live, live)

	var data manifestResourceModel
	data.ID = types.StringValue(live.GetSelfLink())
	data.UID = types.StringValue(live.GetUID())
	data.LiveUID = types.StringValue(live.GetUID())
	data.YAMLInCluster = types.StringValue(fingerprint)
	data.LiveManifestInCluster = types.StringValue(fingerprint)
	data.APIVersion = types.StringValue(live.GetAPIVersion())
	data.Kind = types.StringValue(live.GetKind())
	data.Name = types.StringValue(live.GetName())
	if live.GetNamespace() != "" {
		data.Namespace = types.StringValue(live.GetNamespace())
	} else {
		data.Namespace = types.StringNull()
	}
	data.YAMLBody = types.StringValue(yamlStripped)
	data.YAMLBodyParsed = types.StringValue(yamlStripped)

	// Set Optional+Computed booleans / strings to their schema-default
	// values so the next plan against an unmodified config sees no
	// drift.
	data.ForceNew = types.BoolValue(false)
	data.UpgradeAPIVersion = types.BoolValue(false)
	data.ServerSideApply = types.BoolValue(false)
	data.FieldManager = types.StringValue("kubectl")
	data.ForceConflicts = types.BoolValue(false)
	data.ApplyOnly = types.BoolValue(false)
	data.Wait = types.BoolValue(false)
	data.WaitForRollout = types.BoolValue(true)
	data.ValidateSchema = types.BoolValue(true)

	// Fields without a schema default still need typed-null values
	// rather than the Go zero value: types.List / types.Object /
	// timeouts.Value zero values carry no element / attribute type and
	// the framework rejects them at State.Set with a Value Conversion
	// Error. Mirror the typed-null construction used by MoveState in
	// resource_kubectl_manifest_move.go.
	data.OverrideNamespace = types.StringNull()
	data.SensitiveFields = types.ListNull(types.StringType)
	data.IgnoreFields = types.ListNull(types.StringType)
	data.DeleteCascade = types.StringNull()
	data.WaitFor = types.ListNull(waitForObjectType())
	data.Timeouts = timeouts.Value{
		Object: types.ObjectNull(map[string]attr.Type{
			"create": types.StringType,
			"update": types.StringType,
			"delete": types.StringType,
		}),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// parseManifestImportID splits a kubectl_manifest import ID of the form
// `apiVersion//kind//name[//namespace]` into its components. Returns an
// error if the ID has the wrong number of parts or any component is
// empty. The double-slash delimiter handles apiVersions that contain a
// single slash (e.g. `apps/v1`, `cert-manager.io/v1`).
func parseManifestImportID(id string) (apiVersion, kind, name, namespace string, err error) {
	parts := strings.Split(id, "//")
	if len(parts) != 3 && len(parts) != 4 {
		return "", "", "", "", fmt.Errorf(
			"expected ID in format apiVersion//kind//name or apiVersion//kind//name//namespace, got %q",
			id,
		)
	}
	apiVersion, kind, name = parts[0], parts[1], parts[2]
	if len(parts) == 4 {
		namespace = parts[3]
	}
	if apiVersion == "" || kind == "" || name == "" {
		return "", "", "", "", fmt.Errorf(
			"apiVersion, kind, and name must all be non-empty, got %q",
			id,
		)
	}
	return apiVersion, kind, name, namespace, nil
}

// buildManifestImportYAMLStub returns the minimal YAML body that
// yaml.ParseYAML needs to discover the GVK and namespace so the dynamic
// client can fetch the live object. The full manifest is rebuilt from
// that live object after the Get.
func buildManifestImportYAMLStub(apiVersion, kind, name, namespace string) string {
	if namespace != "" {
		return fmt.Sprintf(
			"apiVersion: %s\nkind: %s\nmetadata:\n  namespace: %s\n  name: %s\n",
			apiVersion, kind, namespace, name,
		)
	}
	return fmt.Sprintf(
		"apiVersion: %s\nkind: %s\nmetadata:\n  name: %s\n",
		apiVersion, kind, name,
	)
}
