package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestUpdate_PreservesStateOnApplyError pins the fix for issue #60.
// When ApplyManifest errors partway through Update, the prior state
// must be restored verbatim to resp.State so the next plan compares
// config against the actual (pre-failed-apply) state and reports the
// intended change again. Without the restoration, Terraform persists
// the planned but unapplied yaml_body and the next plan reports a
// false "no changes".
//
// The test drives Update down its earliest error branch by leaving
// the manifestResource's kubeProviderCache unset; the production code
// then surfaces "provider not configured: kubeProviderCache unset" and
// must restore prior state before returning. The branch is exercised
// without a real cluster connection or fake kubernetes client, which
// keeps the test deterministic and Go-only.
func TestUpdate_PreservesStateOnApplyError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	priorYAML := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  k: old\n"
	plannedYAML := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  k: new\n"

	const priorDrift = "metadata:\n  annotations:\n    prior: <drift>\n"

	priorState := baseModel()
	priorState.YAMLBody = types.StringValue(priorYAML)
	priorState.UID = types.StringValue("uid-existing")
	priorState.LiveUID = types.StringValue("uid-existing")
	priorState.Drift = types.StringValue(priorDrift)
	priorState.APIVersion = types.StringValue("v1")
	priorState.Kind = types.StringValue("ConfigMap")
	priorState.Name = types.StringValue("x")

	plan := baseModel()
	plan.YAMLBody = types.StringValue(plannedYAML)

	r := &manifestResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("building schema: %+v", schemaResp.Diagnostics)
	}
	s := schemaResp.Schema
	tfType := s.Type().TerraformType(ctx)

	stateBlob := tfsdk.State{Schema: s, Raw: tftypes.NewValue(tfType, nil)}
	if diags := stateBlob.Set(ctx, &priorState); diags.HasError() {
		t.Fatalf("seeding prior state: %+v", diags)
	}
	planBlob := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(tfType, nil)}
	if diags := planBlob.Set(ctx, &plan); diags.HasError() {
		t.Fatalf("seeding plan: %+v", diags)
	}

	req := resource.UpdateRequest{State: stateBlob, Plan: planBlob}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(tfType, nil)}}

	r.Update(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected an error diagnostic from Update with no kubeProvider, got none")
	}

	var got manifestResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("reading post-Update state: %+v", diags)
	}

	if got.YAMLBody.ValueString() != priorYAML {
		t.Errorf("yaml_body should be the prior value after a failed Update; got %q want %q",
			got.YAMLBody.ValueString(), priorYAML)
	}
	if got.YAMLBody.ValueString() == plannedYAML {
		t.Errorf("yaml_body must NOT be the planned value after a failed Update; #60 regression")
	}
	if got.Drift.ValueString() != priorDrift {
		t.Errorf("drift after failed Update: expected %q, got %q",
			priorDrift, got.Drift.ValueString())
	}
	if got.UID.ValueString() != "uid-existing" {
		t.Errorf("uid should be the prior value after a failed Update; got %q",
			got.UID.ValueString())
	}
}
