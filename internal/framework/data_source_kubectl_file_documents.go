package framework

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/alekc/terraform-provider-kubectl/yaml"
)

// fileDocumentsDataSource takes a multi-document YAML string and splits it
// into individual documents + a self-link keyed map. No filesystem or cluster
// access; pure-string transformation.
type fileDocumentsDataSource struct{}

var (
	_ datasource.DataSource = (*fileDocumentsDataSource)(nil)
)

func NewFileDocumentsDataSource() datasource.DataSource {
	return &fileDocumentsDataSource{}
}

type fileDocumentsModel struct {
	ID        types.String `tfsdk:"id"`
	Content   types.String `tfsdk:"content"`
	Documents types.List   `tfsdk:"documents"`
	Manifests types.Map    `tfsdk:"manifests"`
}

func (d *fileDocumentsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_file_documents"
}

func (d *fileDocumentsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Splits a multi-document YAML string (the inline `content` argument) into its constituent " +
			"documents and a self-link keyed map. The split + normalisation are pure-Go; no cluster access.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "sha256 of the input content.",
			},
			"content": schema.StringAttribute{
				Required:    true,
				Description: "Multi-document YAML string. Documents are separated by `---` per YAML 1.2.",
			},
			"documents": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "The individual non-empty YAML documents extracted from `content`, in source order.",
			},
			"manifests": schema.MapAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Map keyed by each document's Kubernetes self-link (apiVersion/kind/namespace/name " +
					"derived). Useful for indexing manifests by stable identifier rather than position.",
			},
		},
	}
}

func (d *fileDocumentsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data fileDocumentsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	content := data.Content.ValueString()
	documents, err := yaml.SplitMultiDocumentYAML(content)
	if err != nil {
		resp.Diagnostics.AddError("kubectl_file_documents: failed to split multi-document YAML", err.Error())
		return
	}

	manifests := map[string]string{}
	for _, doc := range documents {
		manifest, err := yaml.ParseYAML(doc)
		if err != nil {
			resp.Diagnostics.AddError("kubectl_file_documents: failed to parse YAML manifest", err.Error())
			return
		}
		parsed, err := manifest.AsYAML()
		if err != nil {
			resp.Diagnostics.AddError("kubectl_file_documents: failed to re-encode manifest as YAML", err.Error())
			return
		}
		key := manifest.GetSelfLink()
		if _, exists := manifests[key]; exists {
			resp.Diagnostics.AddError(
				"kubectl_file_documents: duplicate manifest",
				fmt.Sprintf("two documents resolve to the same self-link %q", key),
			)
			return
		}
		manifests[key] = parsed
	}

	docsVal, diags := types.ListValueFrom(ctx, types.StringType, documents)
	resp.Diagnostics.Append(diags...)
	manifestsVal, diags := types.MapValueFrom(ctx, types.StringType, manifests)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(fmt.Sprintf("%x", sha256.Sum256([]byte(content))))
	data.Documents = docsVal
	data.Manifests = manifestsVal
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
