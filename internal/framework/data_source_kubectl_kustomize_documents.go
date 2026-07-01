package framework

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	kustomizetypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// kustomizeDocumentsDataSource runs `kustomize build` against a target
// directory and returns the rendered YAML documents. Backfills the
// `kubectl_kustomize_documents` data source that gavinbunney added in
// gavinbunney/terraform-provider-kubectl#113 (post-2022-fork). Ported here
// framework-native from day one rather than via SDK v2 -> framework migration.
//
// Pure filesystem operation - no Kubernetes API access required.
type kustomizeDocumentsDataSource struct{}

var (
	_ datasource.DataSource = (*kustomizeDocumentsDataSource)(nil)
)

func NewKustomizeDocumentsDataSource() datasource.DataSource {
	return &kustomizeDocumentsDataSource{}
}

type kustomizeDocumentsModel struct {
	ID                 types.String `tfsdk:"id"`
	Target             types.String `tfsdk:"target"`
	LoadRestrictor     types.String `tfsdk:"load_restrictor"`
	AddManagedByLabel  types.Bool   `tfsdk:"add_managed_by_label"`
	Documents          types.List   `tfsdk:"documents"`
}

func (d *kustomizeDocumentsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kustomize_documents"
}

func (d *kustomizeDocumentsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Runs `kustomize build` against the target directory and returns the resulting YAML " +
			"documents. Wraps sigs.k8s.io/kustomize/api so no external `kustomize` binary is required at apply " +
			"time.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "sha256 of the rendered document list.",
			},
			"target": schema.StringAttribute{
				Required:    true,
				Description: "Path to the kustomization directory, evaluated relative to the Terraform working " +
					"directory. Same semantic as `kustomize build <target>`.",
			},
			"load_restrictor": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Kustomize loader restriction. `rootOnly` (default) prevents bases from loading " +
					"files outside the target directory; `none` removes the restriction. Anything else is " +
					"rejected at config-validate time.",
				Validators: []validator.String{
					loadRestrictorValidator{},
				},
			},
			"add_managed_by_label": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "When true, kustomize stamps an `app.kubernetes.io/managed-by` label on every " +
					"rendered resource. Default false.",
			},
			"documents": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Rendered YAML documents in kustomize build order.",
			},
		},
	}
}

func (d *kustomizeDocumentsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if deferIfConfigUnknown(req, resp) {
		return
	}

	var data kustomizeDocumentsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	target := data.Target.ValueString()
	restrictor := "rootOnly"
	if !data.LoadRestrictor.IsNull() && !data.LoadRestrictor.IsUnknown() && data.LoadRestrictor.ValueString() != "" {
		restrictor = data.LoadRestrictor.ValueString()
	}
	addLabel := !data.AddManagedByLabel.IsNull() && data.AddManagedByLabel.ValueBool()

	opts := krusty.MakeDefaultOptions()
	switch restrictor {
	case "none":
		opts.LoadRestrictions = kustomizetypes.LoadRestrictionsNone
	case "rootOnly":
		opts.LoadRestrictions = kustomizetypes.LoadRestrictionsRootOnly
	default:
		resp.Diagnostics.AddAttributeError(
			path.Root("load_restrictor"),
			"invalid load_restrictor",
			fmt.Sprintf("expected 'rootOnly' or 'none', got %q", restrictor),
		)
		return
	}
	opts.AddManagedbyLabel = addLabel

	k := krusty.MakeKustomizer(opts)
	rm, err := k.Run(filesys.MakeFsOnDisk(), target)
	if err != nil {
		resp.Diagnostics.AddError("kubectl_kustomize_documents: kustomize render failed", err.Error())
		return
	}

	documents, err := docsFromResMap(rm)
	if err != nil {
		resp.Diagnostics.AddError("kubectl_kustomize_documents: failed to read rendered documents", err.Error())
		return
	}

	docsVal, diags := types.ListValueFrom(ctx, types.StringType, documents)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Hash the documents with length-prefixed framing so boundary positions
	// can't collide: `["ab", "c"]` and `["a", "bc"]` would otherwise share a
	// preimage. Mirrors the pattern used by kubectl_filename_list.
	h := sha256.New()
	for i, doc := range documents {
		_, _ = fmt.Fprintf(h, "%d:%d:%s\n", i, len(doc), doc)
	}
	data.ID = types.StringValue(fmt.Sprintf("%x", h.Sum(nil)))
	data.Documents = docsVal
	if data.LoadRestrictor.IsNull() {
		data.LoadRestrictor = types.StringValue(restrictor)
	}
	if data.AddManagedByLabel.IsNull() {
		data.AddManagedByLabel = types.BoolValue(addLabel)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// docsFromResMap serialises each resource in the resmap to YAML and returns
// the list in resmap order. Pure helper to keep the Read function focused.
func docsFromResMap(rm resmap.ResMap) ([]string, error) {
	out := make([]string, 0, rm.Size())
	for _, res := range rm.Resources() {
		b, err := res.AsYAML()
		if err != nil {
			return nil, err
		}
		out = append(out, string(b))
	}
	return out, nil
}

// loadRestrictorValidator pins the small set of values kustomize accepts.
type loadRestrictorValidator struct{}

func (loadRestrictorValidator) Description(_ context.Context) string {
	return "must be 'rootOnly' or 'none'"
}

func (l loadRestrictorValidator) MarkdownDescription(ctx context.Context) string {
	return l.Description(ctx)
}

func (loadRestrictorValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	v := req.ConfigValue.ValueString()
	if v != "" && v != "rootOnly" && v != "none" {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"invalid load_restrictor",
			fmt.Sprintf("expected one of 'rootOnly' or 'none', got %q", v),
		)
	}
}
