package framework

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	internaltypes "github.com/alekc/terraform-provider-kubectl/internal/types"
)

// wait_for_blocks.go: shared decoder for the read-side `wait_for`
// block (data source + ephemeral resource). The resource path
// uses its own extractWaitFor in resource_kubectl_manifest.go;
// future PR can collapse that one onto this helper too, but is
// out of scope for #179.
//
// The data source and the ephemeral resource each declare their
// own schema (different container types in the framework), but
// the inner block shape is identical to the resource's, so all
// three share `waitForBlockModel` / `waitForConditionModel` /
// `waitForFieldModel` (defined in resource_kubectl_manifest.go,
// package-public lowercase).

// Surface labels for extractWaitForBlock's error wording. Lifted
// to consts so a call-site typo cannot silently produce
// misleading diagnostics like "date source".
const (
	waitForSurfaceDataSource = "data source"
	waitForSurfaceEphemeral  = "ephemeral resource"
)

// extractWaitForBlock materialises a list-of-wait_for-blocks into
// an *internaltypes.WaitFor pointer for the kubernetes helper.
// Returns nil if the block is null or unknown.
//
// The "at most one wait_for block" invariant is enforced by
// listvalidator.SizeAtMost(1) on the schema, so under normal
// operation the len(blocks) > 1 branch below is unreachable.
// The runtime check stays as a backstop: if a future schema
// refactor drops the validator or the schema is consumed via a
// non-Terraform path that bypasses validators, the helper
// continues to refuse multi-block input rather than silently
// taking only the first.
//
// surface labels the calling schema in the error wording. Pass
// one of waitForSurfaceDataSource / waitForSurfaceEphemeral.
func extractWaitForBlock(ctx context.Context, list types.List, surface string) (*internaltypes.WaitFor, diag.Diagnostics) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}
	var blocks []waitForBlockModel
	diags := list.ElementsAs(ctx, &blocks, false)
	if diags.HasError() {
		return nil, diags
	}
	if len(blocks) == 0 {
		return nil, diags
	}
	if len(blocks) > 1 {
		diags.AddError(
			"too many wait_for blocks",
			fmt.Sprintf("the kubectl_manifest %s supports at most one wait_for block; got %d", surface, len(blocks)),
		)
		return nil, diags
	}
	b := blocks[0]
	wf := &internaltypes.WaitFor{}
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
	return wf, diags
}
