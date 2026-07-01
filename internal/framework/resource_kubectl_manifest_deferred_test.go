package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestManifestResourceModifyPlanDeferredActions pins the resource-level
// deferred-actions behaviour for issue #356 on kubectl_manifest. When the
// client allows deferral and the resource configuration is not fully known
// (e.g. yaml_body interpolates a not-yet-applied value), ModifyPlan must
// return a deferral with DeferredReasonResourceConfigUnknown. The capability
// gate must keep the classic plan path unchanged.
func TestManifestResourceModifyPlanDeferredActions(t *testing.T) {
	ctx := context.Background()
	r := NewManifestResource()
	rmp, ok := r.(resource.ResourceWithModifyPlan)
	if !ok {
		t.Fatalf("manifest resource does not implement ResourceWithModifyPlan")
	}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	objType, ok := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("resource schema type is %T, want tftypes.Object", schemaResp.Schema.Type().TerraformType(ctx))
	}

	// Config wholly unknown -> not fully known; the deferral guard runs
	// before the plan is decoded.
	unknownCfg := tftypes.NewValue(objType, tftypes.UnknownValue)

	// Plan: a known object (non-null, so it passes the destroy early-return)
	// with yaml_body unknown, so the capability-off path returns cleanly at
	// the existing "yaml_body is unknown" early-return rather than parsing.
	planAttrs := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for n, at := range objType.AttributeTypes {
		planAttrs[n] = tftypes.NewValue(at, nil)
	}
	planAttrs["yaml_body"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	planVal := tftypes.NewValue(objType, planAttrs)

	nullState := tftypes.NewValue(objType, nil)

	modifyPlan := func(deferralAllowed bool) *resource.ModifyPlanResponse {
		req := resource.ModifyPlanRequest{
			Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: unknownCfg},
			Plan:               tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
			State:              tfsdk.State{Schema: schemaResp.Schema, Raw: nullState},
			ClientCapabilities: resource.ModifyPlanClientCapabilities{DeferralAllowed: deferralAllowed},
		}
		resp := &resource.ModifyPlanResponse{
			Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		}
		rmp.ModifyPlan(ctx, req, resp)
		return resp
	}

	t.Run("deferral allowed defers", func(t *testing.T) {
		resp := modifyPlan(true)
		if resp.Deferred == nil {
			t.Fatalf("expected a deferred plan, got none")
		}
		if resp.Deferred.Reason != resource.DeferredReasonResourceConfigUnknown {
			t.Errorf("deferred reason = %s, want Resource Config Unknown", resp.Deferred.Reason)
		}
		if resp.Diagnostics.HasError() {
			t.Errorf("unexpected diagnostics on deferral: %s", resp.Diagnostics)
		}
	})

	t.Run("capability off does not defer", func(t *testing.T) {
		resp := modifyPlan(false)
		if resp.Deferred != nil {
			t.Errorf("did not expect a deferred plan without the capability, got %s", resp.Deferred.Reason)
		}
	})
}
