package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestYAMLBodyAwareUseStateForUnknown_PlanModifyString covers the five
// branches of yamlBodyAwareUseStateForUnknown: null state (Create),
// already-known plan, plan.yaml_body Unknown, plan.yaml_body Known and
// differs from state, plan.yaml_body Known and matches state. The
// matching branch is the only one that returns state into the plan;
// every other branch must leave the framework's default Unknown alone.
//
// Regression target: issue #49 (cross-resource Unknown interpolation)
// and TestAccInconsistentPlanning's timestamp() pattern.
func TestYAMLBodyAwareUseStateForUnknown_PlanModifyString(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := yamlBodyAwareUseStateForUnknown{}

	// Build the resource schema once; every case reuses it for the
	// tfsdk.Plan and tfsdk.State terraform-types.
	r := &manifestResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("building schema: %+v", schemaResp.Diagnostics)
	}
	s := schemaResp.Schema
	tfType := s.Type().TerraformType(ctx)

	mkObj := func(yamlBody types.String) tftypes.Value {
		model := baseModel()
		model.YAMLBody = yamlBody
		raw := tftypes.NewValue(tfType, nil)
		plan := tfsdk.Plan{Schema: s, Raw: raw}
		if diags := plan.Set(ctx, &model); diags.HasError() {
			t.Fatalf("seeding tfsdk.Plan: %+v", diags)
		}
		return plan.Raw
	}

	cases := []struct {
		name          string
		stateValue    types.String
		planValue     types.String
		planYAMLBody  types.String
		stateYAMLBody types.String
		wantPlanValue types.String
		wantUnchanged bool
	}{
		{
			name:          "state null (Create) leaves plan unknown",
			stateValue:    types.StringNull(),
			planValue:     types.StringUnknown(),
			planYAMLBody:  types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
			stateYAMLBody: types.StringNull(),
			wantUnchanged: true,
		},
		{
			name:          "plan already known is left alone",
			stateValue:    types.StringValue("sha-OLD"),
			planValue:     types.StringValue("sha-FORCED"),
			planYAMLBody:  types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
			stateYAMLBody: types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
			wantUnchanged: true,
		},
		{
			name:          "plan.yaml_body Unknown leaves plan unknown",
			stateValue:    types.StringValue("sha-OLD"),
			planValue:     types.StringUnknown(),
			planYAMLBody:  types.StringUnknown(),
			stateYAMLBody: types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
			wantUnchanged: true,
		},
		{
			name:          "plan.yaml_body differs from state leaves plan unknown",
			stateValue:    types.StringValue("sha-OLD"),
			planValue:     types.StringUnknown(),
			planYAMLBody:  types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  k: NEW\n"),
			stateYAMLBody: types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
			wantUnchanged: true,
		},
		{
			name:          "plan.yaml_body matches state copies state into plan",
			stateValue:    types.StringValue("sha-OLD"),
			planValue:     types.StringUnknown(),
			planYAMLBody:  types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
			stateYAMLBody: types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
			wantPlanValue: types.StringValue("sha-OLD"),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			planRaw := mkObj(tc.planYAMLBody)
			stateRaw := mkObj(tc.stateYAMLBody)

			// State null overrides: if the case wants null state,
			// stub the State raw to a null Value so the modifier's
			// req.StateValue null check fires before any GetAttribute.
			if tc.stateValue.IsNull() {
				stateRaw = tftypes.NewValue(tfType, nil)
			}

			req := planmodifier.StringRequest{
				Path:       path.Root("yaml_incluster"),
				StateValue: tc.stateValue,
				PlanValue:  tc.planValue,
				Plan:       tfsdk.Plan{Schema: s, Raw: planRaw},
				State:      tfsdk.State{Schema: s, Raw: stateRaw},
			}
			resp := &planmodifier.StringResponse{PlanValue: tc.planValue}
			m.PlanModifyString(ctx, req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %+v", resp.Diagnostics)
			}
			if tc.wantUnchanged {
				if !resp.PlanValue.Equal(tc.planValue) {
					t.Errorf("expected PlanValue unchanged, got %v (was %v)", resp.PlanValue, tc.planValue)
				}
				return
			}
			if !resp.PlanValue.Equal(tc.wantPlanValue) {
				t.Errorf("PlanValue: got %v, want %v", resp.PlanValue, tc.wantPlanValue)
			}
		})
	}
}

// TestYAMLBodyAwareUseStateForUnknown_DescriptionsAreNonEmpty pins the
// trivial getters so the planmodifier.String interface remains
// satisfied even after future field renames. The interface check
// itself is a compile-time assertion.
func TestYAMLBodyAwareUseStateForUnknown_DescriptionsAreNonEmpty(t *testing.T) {
	t.Parallel()
	var _ planmodifier.String = yamlBodyAwareUseStateForUnknown{}
	m := yamlBodyAwareUseStateForUnknown{}
	if m.Description(context.Background()) == "" {
		t.Error("Description must be non-empty")
	}
	if m.MarkdownDescription(context.Background()) == "" {
		t.Error("MarkdownDescription must be non-empty")
	}
}
