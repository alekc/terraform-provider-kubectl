package framework

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/alekc/terraform-provider-kubectl/kubernetes"
	"github.com/alekc/terraform-provider-kubectl/yaml"
)

// pathDocumentsDataSource expands a glob pattern against the local filesystem,
// optionally renders each file as an HCL template using user-supplied vars,
// and splits the result into individual YAML documents. The template
// renderer ships the same function surface as Terraform's own `templatefile`
// (vendored from terraform/lang/funcs in this provider).
type pathDocumentsDataSource struct{}

var (
	_ datasource.DataSource = (*pathDocumentsDataSource)(nil)
)

func NewPathDocumentsDataSource() datasource.DataSource {
	return &pathDocumentsDataSource{}
}

type pathDocumentsModel struct {
	ID              types.String `tfsdk:"id"`
	Pattern         types.String `tfsdk:"pattern"`
	Vars            types.Map    `tfsdk:"vars"`
	SensitiveVars   types.Map    `tfsdk:"sensitive_vars"`
	DisableTemplate types.Bool   `tfsdk:"disable_template"`
	Documents       types.List   `tfsdk:"documents"`
	Manifests       types.Map    `tfsdk:"manifests"`
}

func (d *pathDocumentsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_path_documents"
}

func (d *pathDocumentsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Expands a filesystem glob pattern, renders each file through Terraform's template engine " +
			"(unless `disable_template` is true), splits each rendered file into individual YAML documents, " +
			"and returns the flattened result plus a self-link keyed map of manifests.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "sha256 of the concatenated post-template documents.",
			},
			"pattern": schema.StringAttribute{
				Required:    true,
				Description: "Glob pattern, evaluated relative to the Terraform working directory.",
			},
			"vars": schema.MapAttribute{
				Optional: true,
				// Computed so Read can populate the empty-map default
				// when the user didn't set the attribute; without
				// Computed the framework rejects state-vs-config
				// divergence as "Provider produced inconsistent result
				// after apply".
				Computed:    true,
				ElementType: types.StringType,
				Description: "Variables to substitute into each loaded document's HCL template. The map's " +
					"element type is string, so the framework rejects lists or maps at config-validate time " +
					"with a 'string required, but have tuple/object' error. Defaults to an empty map.",
			},
			"sensitive_vars": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				Description: "Same as `vars` but the values are marked sensitive so they don't leak into plan / " +
					"apply output. Defaults to an empty map.",
			},
			"disable_template": schema.BoolAttribute{
				Optional: true,
				Description: "When true, files are loaded as-is without template rendering. Useful for raw YAML " +
					"that contains `${...}` literals that would otherwise be interpreted by Terraform. Defaults " +
					"to false.",
			},
			"documents": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "The individual non-empty YAML documents extracted from every matched file, in glob-" +
					"order then in-file source order.",
			},
			"manifests": schema.MapAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Map keyed by each document's Kubernetes self-link. Duplicate self-links fail the read.",
			},
		},
	}
}

func (d *pathDocumentsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Deferred actions: defer this data source when the client allows
	// deferral and its configuration is not yet fully known (e.g. an input
	// interpolated from a not-yet-applied resource in a deferral-aware run).
	// Gated on the deferral client capability so the classic read path is
	// unchanged. See #356.
	if req.ClientCapabilities.DeferralAllowed && !req.Config.Raw.IsFullyKnown() {
		resp.Deferred = &datasource.Deferred{
			Reason: datasource.DeferredReasonDataSourceConfigUnknown,
		}
		return
	}

	var data pathDocumentsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build the var map by merging sensitive into non-sensitive (sensitive
	// wins on collision, matching the SDK v2 behaviour).
	vars := map[string]string{}
	if !data.Vars.IsNull() && !data.Vars.IsUnknown() {
		raw := map[string]string{}
		resp.Diagnostics.Append(data.Vars.ElementsAs(ctx, &raw, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		for k, v := range raw {
			vars[k] = v
		}
	}
	if !data.SensitiveVars.IsNull() && !data.SensitiveVars.IsUnknown() {
		raw := map[string]string{}
		resp.Diagnostics.Append(data.SensitiveVars.ElementsAs(ctx, &raw, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		for k, v := range raw {
			vars[k] = v
		}
	}
	disableTemplate := !data.DisableTemplate.IsNull() && data.DisableTemplate.ValueBool()

	items, err := filepath.Glob(data.Pattern.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("kubectl_path_documents: invalid glob pattern", err.Error())
		return
	}
	sort.Strings(items)

	allDocuments := []string{}
	for _, item := range items {
		content, err := os.ReadFile(item)
		if err != nil {
			resp.Diagnostics.AddError(
				"kubectl_path_documents: failed to load document from file",
				fmt.Sprintf("%s: %s", item, err),
			)
			return
		}

		rendered := string(content)
		if !disableTemplate {
			rendered, err = kubernetes.ParsePathTemplate(rendered, vars)
			if err != nil {
				resp.Diagnostics.AddError(
					"kubectl_path_documents: failed to render template",
					fmt.Sprintf("%s: %s", item, err),
				)
				return
			}
		}

		documents, err := yaml.SplitMultiDocumentYAML(rendered)
		if err != nil {
			resp.Diagnostics.AddError(
				"kubectl_path_documents: failed to split multi-document YAML",
				fmt.Sprintf("%s: %s", item, err),
			)
			return
		}
		allDocuments = append(allDocuments, documents...)
	}

	manifests := map[string]string{}
	for _, doc := range allDocuments {
		manifest, err := yaml.ParseYAML(doc)
		if err != nil {
			resp.Diagnostics.AddError("kubectl_path_documents: failed to parse YAML manifest", err.Error())
			return
		}
		parsed, err := manifest.AsYAML()
		if err != nil {
			resp.Diagnostics.AddError("kubectl_path_documents: failed to re-encode manifest as YAML", err.Error())
			return
		}
		key := manifest.GetSelfLink()
		if _, exists := manifests[key]; exists {
			resp.Diagnostics.AddError(
				"kubectl_path_documents: duplicate manifest",
				fmt.Sprintf("two documents resolve to the same self-link %q", key),
			)
			return
		}
		manifests[key] = parsed
	}

	docsVal, diags := types.ListValueFrom(ctx, types.StringType, allDocuments)
	resp.Diagnostics.Append(diags...)
	manifestsVal, diags := types.MapValueFrom(ctx, types.StringType, manifests)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(allDocuments, "")))))
	data.Documents = docsVal
	data.Manifests = manifestsVal
	// `vars` and `sensitive_vars` are Optional+Computed so Read fills
	// the empty-map default when the user did not set them. SDK v2
	// declared both with `Default: make(map[string]interface{})` so
	// downstream `keys(data.x.vars)` returned `[]`; the post-Phase-F
	// `types.MapNull` regression (#328) broke that contract because
	// `keys(null)` errors with "argument must not be null". Restoring
	// the empty-map default realigns v3 with v2 and matches the
	// schema description ("Defaults to an empty map.").
	emptyStringMap := types.MapValueMust(types.StringType, map[string]attr.Value{})
	if data.Vars.IsNull() || data.Vars.IsUnknown() {
		data.Vars = emptyStringMap
	}
	if data.SensitiveVars.IsNull() || data.SensitiveVars.IsUnknown() {
		data.SensitiveVars = emptyStringMap
	}
	// `disable_template` stays Optional-only and null-by-default. The
	// audit (#328) limited the regression scope to maps because
	// `length`/`keys` on null is the failure mode; a null bool has no
	// equivalent footgun via terraform's standard library functions.
	if data.DisableTemplate.IsNull() || data.DisableTemplate.IsUnknown() {
		data.DisableTemplate = types.BoolNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
