package framework

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// nullTimeouts returns a typed-null timeouts.Value whose Object knows the
// create/update/delete attribute layout the schema block declares. Models
// going through tfsdk.Plan.Set need the Value's underlying ObjectType to
// match the schema or Set fails with a Value Conversion Error.
func nullTimeouts() timeouts.Value {
	return timeouts.Value{
		Object: types.ObjectNull(map[string]attr.Type{
			"create": types.StringType,
			"update": types.StringType,
			"delete": types.StringType,
		}),
	}
}

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

// newUpdatePlan is newCreatePlan with prior state seeded from stateModel.
func newUpdatePlan(ctx context.Context, t *testing.T, stateModel, planModel manifestResourceModel) (resource.ModifyPlanRequest, *resource.ModifyPlanResponse) {
	t.Helper()
	req, resp := newCreatePlan(ctx, t, planModel)
	if diags := req.State.Set(ctx, &stateModel); diags.HasError() {
		t.Fatalf("seeding state: %+v", diags)
	}
	return req, resp
}

func baseModel() manifestResourceModel {
	return manifestResourceModel{
		YAMLBody:        types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
		SensitiveFields: types.ListNull(types.StringType),
		IgnoreFields:    types.ListNull(types.StringType),
		MaskPaths:       types.ListNull(types.StringType),
		WaitFor:         types.ListNull(waitForObjectType()),
		Timeouts:        nullTimeouts(),
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

// TestModifyPlan_IdentityChangeRequiresReplace asserts a change to kind,
// metadata.name or namespace in yaml_body forces replacement (#365), and that
// an unchanged identity or a null kind/name in state does not.
func TestModifyPlan_IdentityChangeRequiresReplace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &manifestResource{}

	const rb = "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\n" +
		"metadata:\n  name: %s\n  namespace: %s\n" +
		"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: r\n"

	stateFor := func(yamlBody, kind, name, namespace types.String) manifestResourceModel {
		m := baseModel()
		m.YAMLBody = yamlBody
		m.APIVersion = types.StringValue("rbac.authorization.k8s.io/v1")
		m.Kind = kind
		m.Name = name
		m.Namespace = namespace
		m.UID = types.StringValue("uid-1")
		m.LiveUID = types.StringValue("uid-1")
		return m
	}
	body := func(name, ns string) types.String {
		return types.StringValue(fmt.Sprintf(rb, name, ns))
	}
	str := types.StringValue

	cases := []struct {
		name     string
		state    manifestResourceModel
		planBody types.String
		override string
		want     []string
	}{
		{
			name:     "unchanged",
			state:    stateFor(body("a", "ns1"), str("RoleBinding"), str("a"), str("ns1")),
			planBody: body("a", "ns1"),
		},
		{
			name:     "rename",
			state:    stateFor(body("a", "ns1"), str("RoleBinding"), str("a"), str("ns1")),
			planBody: body("b", "ns1"),
			want:     []string{"name"},
		},
		{
			name:     "namespace change",
			state:    stateFor(body("a", "ns1"), str("RoleBinding"), str("a"), str("ns1")),
			planBody: body("a", "ns2"),
			want:     []string{"namespace"},
		},
		{
			name:     "override_namespace change",
			state:    stateFor(body("a", "ns1"), str("RoleBinding"), str("a"), str("ns1")),
			planBody: body("a", "ns1"),
			override: "ns3",
			want:     []string{"namespace"},
		},
		{
			name:  "kind change",
			state: stateFor(body("a", "ns1"), str("RoleBinding"), str("a"), str("ns1")),
			planBody: types.StringValue(strings.Replace(
				fmt.Sprintf(rb, "a", "ns1"), "kind: RoleBinding", "kind: ClusterRoleBinding", 1)),
			want: []string{"kind"},
		},
		{
			name: "null kind and name in state",
			state: stateFor(body("a", "ns1"), types.StringNull(), types.StringNull(),
				str("ns1")),
			planBody: body("b", "ns1"),
		},
		{
			name: "cluster-scoped, null namespace",
			state: stateFor(str("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n"),
				str("Namespace"), str("a"), types.StringNull()),
			planBody: str("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n"),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pm := baseModel()
			pm.YAMLBody = tc.planBody
			if tc.override != "" {
				pm.OverrideNamespace = types.StringValue(tc.override)
			}
			if tc.state.Kind.ValueString() == "Namespace" {
				tc.state.APIVersion = types.StringValue("v1")
			}
			req, resp := newUpdatePlan(ctx, t, tc.state, pm)

			r.ModifyPlan(ctx, req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected error: %+v", resp.Diagnostics)
			}
			got := make([]string, 0, len(resp.RequiresReplace))
			for _, p := range resp.RequiresReplace {
				got = append(got, p.String())
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("RequiresReplace = %v, want %v", got, tc.want)
			}
		})
	}
}
