package framework

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// waitForListWith builds a wait_for list with n empty blocks (condition and
// field both null), typed to match the schema's ListNestedBlock element.
func waitForListWith(n int) types.List {
	blockType := waitForObjectType()
	condType := blockType.AttrTypes["condition"].(types.ListType).ElementType()
	fieldType := blockType.AttrTypes["field"].(types.ListType).ElementType()
	blocks := make([]attr.Value, 0, n)
	for i := 0; i < n; i++ {
		blocks = append(blocks, types.ObjectValueMust(blockType.AttrTypes, map[string]attr.Value{
			"condition": types.ListNull(condType),
			"field":     types.ListNull(fieldType),
		}))
	}
	return types.ListValueMust(blockType, blocks)
}

// newCreatePlan builds a ModifyPlanRequest / Response pair for a create plan
// (null prior state) carrying the given model.
func newCreatePlan(ctx context.Context, t *testing.T, model manifestResourceModel) (resource.ModifyPlanRequest, *resource.ModifyPlanResponse) {
	t.Helper()
	r := &manifestResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("building schema: %+v", schemaResp.Diagnostics)
	}
	s := schemaResp.Schema
	tfType := s.Type().TerraformType(ctx)

	plan := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(tfType, nil)}
	if diags := plan.Set(ctx, &model); diags.HasError() {
		t.Fatalf("seeding plan: %+v", diags)
	}
	req := resource.ModifyPlanRequest{
		Plan:  plan,
		State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(tfType, nil)},
	}
	resp := &resource.ModifyPlanResponse{Plan: plan}
	return req, resp
}

func baseModel() manifestResourceModel {
	return manifestResourceModel{
		YAMLBody:        types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
		SensitiveFields: types.ListNull(types.StringType),
		IgnoreFields:    types.ListNull(types.StringType),
		WaitFor:         types.ListNull(waitForObjectType()),
	}
}

// TestModifyPlan_WaitForMaxItems asserts ModifyPlan rejects more than one
// wait_for block (MaxItems=1, which the schema cannot express) and accepts a
// single block.
func TestModifyPlan_WaitForMaxItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &manifestResource{}

	t.Run("two blocks rejected", func(t *testing.T) {
		t.Parallel()
		m := baseModel()
		m.WaitFor = waitForListWith(2)
		req, resp := newCreatePlan(ctx, t, m)

		r.ModifyPlan(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatalf("expected an error for two wait_for blocks")
		}
		if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "single wait_for block") {
			t.Errorf("unexpected diagnostic: %s", resp.Diagnostics.Errors()[0].Detail())
		}
	})

	t.Run("one block accepted", func(t *testing.T) {
		t.Parallel()
		m := baseModel()
		m.WaitFor = waitForListWith(1)
		req, resp := newCreatePlan(ctx, t, m)

		r.ModifyPlan(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error for a single wait_for block: %+v", resp.Diagnostics)
		}
	})
}

// TestModifyPlan_ForceConflictsRequiresSSA asserts ModifyPlan rejects
// force_conflicts = true when server_side_apply = false (the silent
// no-op that drives #309) and accepts every other combination,
// including the Unknown-on-either-side case where the check cannot
// yet decide and must fall through.
func TestModifyPlan_ForceConflictsRequiresSSA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &manifestResource{}

	cases := []struct {
		name           string
		forceConflicts types.Bool
		serverSide     types.Bool
		wantErr        bool
		wantDetailSub  string
	}{
		{
			name:           "both false: ok",
			forceConflicts: types.BoolValue(false),
			serverSide:     types.BoolValue(false),
			wantErr:        false,
		},
		{
			name:           "ssa true, force false: ok",
			forceConflicts: types.BoolValue(false),
			serverSide:     types.BoolValue(true),
			wantErr:        false,
		},
		{
			name:           "ssa true, force true: ok",
			forceConflicts: types.BoolValue(true),
			serverSide:     types.BoolValue(true),
			wantErr:        false,
		},
		{
			name:           "force true, ssa false: rejected",
			forceConflicts: types.BoolValue(true),
			serverSide:     types.BoolValue(false),
			wantErr:        true,
			wantDetailSub:  "force_conflicts = true has no effect with the default client-side apply",
		},
		{
			name:           "force unknown: falls through (no error)",
			forceConflicts: types.BoolUnknown(),
			serverSide:     types.BoolValue(false),
			wantErr:        false,
		},
		{
			name:           "ssa unknown: falls through (no error)",
			forceConflicts: types.BoolValue(true),
			serverSide:     types.BoolUnknown(),
			wantErr:        false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := baseModel()
			m.ForceConflicts = tc.forceConflicts
			m.ServerSideApply = tc.serverSide
			req, resp := newCreatePlan(ctx, t, m)

			r.ModifyPlan(ctx, req, resp)

			if tc.wantErr {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected an error, got none")
				}
				if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), tc.wantDetailSub) {
					t.Errorf("unexpected diagnostic: %s", resp.Diagnostics.Errors()[0].Detail())
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error: %+v", resp.Diagnostics)
			}
		})
	}
}
