package framework

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// filenameListDataSource expands a glob pattern against the local filesystem
// and returns the matching paths plus their basenames. No cluster access.
type filenameListDataSource struct{}

var (
	_ datasource.DataSource = (*filenameListDataSource)(nil)
)

func NewFilenameListDataSource() datasource.DataSource {
	return &filenameListDataSource{}
}

type filenameListModel struct {
	ID        types.String `tfsdk:"id"`
	Pattern   types.String `tfsdk:"pattern"`
	Matches   types.List   `tfsdk:"matches"`
	Basenames types.List   `tfsdk:"basenames"`
}

func (d *filenameListDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_filename_list"
}

func (d *filenameListDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Returns paths that match a glob pattern on the Terraform host filesystem, plus their basenames. " +
			"Useful for fanning out a `for_each` over a directory of manifests.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "sha256 of the ordered match list (stable across re-reads of the same filesystem state).",
			},
			"pattern": schema.StringAttribute{
				Required:    true,
				Description: "Glob pattern, evaluated relative to the Terraform working directory.",
			},
			"matches": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Full paths of every file matching `pattern`, sorted lexicographically.",
			},
			"basenames": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Basename (last path segment) of each entry in `matches`, in the same order.",
			},
		},
	}
}

func (d *filenameListDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
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

	var data filenameListModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	pattern := data.Pattern.ValueString()
	items, err := filepath.Glob(pattern)
	if err != nil {
		resp.Diagnostics.AddError("kubectl_filename_list: invalid glob pattern", err.Error())
		return
	}
	sort.Strings(items)

	// Build the id preimage with explicit length prefixes so different
	// (i, s) sequences cannot collide: concatenating "i" + "s" without a
	// separator lets `["1", "0"]` and `["", "10"]` both hash to "0110".
	// Streaming through sha256.New + length-prefixed encoding makes the
	// preimage unambiguous regardless of which characters appear in `s`.
	idHash := sha256.New()
	basenames := make([]string, 0, len(items))
	for i, s := range items {
		// sha256.Hash.Write never errors; explicit discard keeps errcheck
		// quiet without changing behaviour.
		_, _ = fmt.Fprintf(idHash, "%d:%d:%s\n", i, len(s), s)
		basenames = append(basenames, filepath.Base(s))
	}

	matchesVal, diags := types.ListValueFrom(ctx, types.StringType, items)
	resp.Diagnostics.Append(diags...)
	basenamesVal, diags := types.ListValueFrom(ctx, types.StringType, basenames)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(fmt.Sprintf("%x", idHash.Sum(nil)))
	data.Matches = matchesVal
	data.Basenames = basenamesVal

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
