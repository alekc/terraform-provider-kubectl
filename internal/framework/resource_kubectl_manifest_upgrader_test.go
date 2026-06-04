package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/alekc/terraform-provider-kubectl/kubernetes"
)

// TestDriftEngineFromString covers the framework-side string -> enum
// mapping that buildApplyOptions and Read call. Empty / null / unknown
// must fall through to ClientDriftEngine so users on existing state
// (where the attribute may not be populated yet) see v2 semantics.
func TestDriftEngineFromString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   types.String
		want kubernetes.DriftEngine
	}{
		{"client", types.StringValue("client"), kubernetes.ClientDriftEngine},
		{"server", types.StringValue("server"), kubernetes.ServerDriftEngine},
		{"empty-string-falls-back-to-client", types.StringValue(""), kubernetes.ClientDriftEngine},
		{"null-falls-back-to-client", types.StringNull(), kubernetes.ClientDriftEngine},
		{"unknown-falls-back-to-client", types.StringUnknown(), kubernetes.ClientDriftEngine},
		{"unrecognised-string-falls-back-to-client", types.StringValue("magic"), kubernetes.ClientDriftEngine},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := driftEngineFromString(c.in)
			if got != c.want {
				t.Errorf("driftEngineFromString(%v): got %q want %q", c.in, got, c.want)
			}
		})
	}
}

// TestDriftModeFromString covers the show_drift_values enum mapping.
// Same fallback contract: anything weird collapses to ShowNone.
func TestDriftModeFromString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   types.String
		want kubernetes.ShowMode
	}{
		{"none", types.StringValue("none"), kubernetes.ShowNone},
		{"hash", types.StringValue("hash"), kubernetes.ShowHash},
		{"full", types.StringValue("full"), kubernetes.ShowFull},
		{"empty", types.StringValue(""), kubernetes.ShowNone},
		{"null", types.StringNull(), kubernetes.ShowNone},
		{"unknown", types.StringUnknown(), kubernetes.ShowNone},
		{"unrecognised", types.StringValue("verbose"), kubernetes.ShowNone},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := driftModeFromString(c.in)
			if got != c.want {
				t.Errorf("driftModeFromString(%v): got %q want %q", c.in, got, c.want)
			}
		})
	}
}

// TestPromoteV1ToV2 verifies the state-shape promotion is faithful:
// every v1 field round-trips to the v2 model verbatim, the four new
// drift attributes get their safe defaults, and the two legacy
// fingerprint attributes are dropped (not present on the v2 model).
func TestPromoteV1ToV2(t *testing.T) {
	t.Parallel()
	v1 := manifestResourceModelV1{
		ID:                    types.StringValue("/api/v1/configmaps/x"),
		UID:                   types.StringValue("u-1"),
		LiveUID:               types.StringValue("u-1"),
		YAMLInCluster:         types.StringValue("legacy-fp-1"),
		LiveManifestInCluster: types.StringValue("legacy-fp-2"),
		APIVersion:            types.StringValue("v1"),
		Kind:                  types.StringValue("ConfigMap"),
		Name:                  types.StringValue("x"),
		Namespace:             types.StringValue("default"),
		OverrideNamespace:     types.StringValue("override"),
		YAMLBody:              types.StringValue("apiVersion: v1\nkind: ConfigMap\n"),
		YAMLBodyParsed:        types.StringValue("apiVersion: v1\nkind: ConfigMap\n"),
		SensitiveFields:       types.ListNull(types.StringType),
		ForceNew:              types.BoolValue(true),
		UpgradeAPIVersion:     types.BoolValue(true),
		ServerSideApply:       types.BoolValue(true),
		FieldManager:          types.StringValue("alekc"),
		ForceConflicts:        types.BoolValue(true),
		ApplyOnly:             types.BoolValue(true),
		IgnoreFields:          types.ListNull(types.StringType),
		Wait:                  types.BoolValue(true),
		WaitForRollout:        types.BoolValue(false),
		ValidateSchema:        types.BoolValue(false),
		DeleteCascade:         types.StringValue("Foreground"),
		WaitFor:               types.ListNull(waitForObjectType()),
	}
	got := promoteV1ToV2(v1)

	// Verbatim passthrough.
	if got.ID.ValueString() != v1.ID.ValueString() {
		t.Errorf("ID: got %q want %q", got.ID.ValueString(), v1.ID.ValueString())
	}
	if got.UID.ValueString() != v1.UID.ValueString() {
		t.Errorf("UID: got %q want %q", got.UID.ValueString(), v1.UID.ValueString())
	}
	if got.YAMLBody.ValueString() != v1.YAMLBody.ValueString() {
		t.Errorf("YAMLBody not preserved")
	}
	if !got.ForceNew.ValueBool() || !got.UpgradeAPIVersion.ValueBool() ||
		!got.ServerSideApply.ValueBool() || !got.ForceConflicts.ValueBool() ||
		!got.ApplyOnly.ValueBool() || !got.Wait.ValueBool() {
		t.Errorf("bool passthrough lost: %+v", got)
	}
	if got.WaitForRollout.ValueBool() {
		t.Errorf("WaitForRollout false should pass through as false")
	}
	if got.FieldManager.ValueString() != "alekc" {
		t.Errorf("FieldManager passthrough lost: %q", got.FieldManager.ValueString())
	}
	if got.DeleteCascade.ValueString() != "Foreground" {
		t.Errorf("DeleteCascade passthrough lost: %q", got.DeleteCascade.ValueString())
	}

	// v3 defaults populated.
	if got.Drift.ValueString() != "" {
		t.Errorf("Drift default after promotion: got %q want empty", got.Drift.ValueString())
	}
	if got.ShowDriftValues.ValueString() != string(kubernetes.ShowNone) {
		t.Errorf("ShowDriftValues default: got %q want %q",
			got.ShowDriftValues.ValueString(), kubernetes.ShowNone)
	}
	if got.DriftEngine.ValueString() != string(kubernetes.ClientDriftEngine) {
		t.Errorf("DriftEngine default: got %q want %q",
			got.DriftEngine.ValueString(), kubernetes.ClientDriftEngine)
	}
	if !got.MaskPaths.IsNull() {
		t.Errorf("MaskPaths default: should be null, got %+v", got.MaskPaths)
	}
}

// TestUpgradeState_V1ToV2_ExercisesUpgrader is the regression test
// for the production crash this PR's first iteration hit: the
// upgrader called req.State.Get without a PriorSchema declared, so
// the framework's State had no Schema and Get panicked with a nil
// pointer. The fix added priorManifestSchemaV1 as PriorSchema; this
// test exercises the upgrader end-to-end with a constructed v1 raw
// state so the same crash will reappear here before it reaches CI.
//
// The test is structured to:
//  1. Build a v1-shaped raw state via the prior schema's Terraform
//     type.
//  2. Construct UpgradeStateRequest with that raw state wrapped in
//     a tfsdk.State carrying the prior schema (mirroring how the
//     framework's UpgradeResourceState handler does it under
//     production conditions).
//  3. Call the upgrader callback the resource registers for v1.
//  4. Decode the response state into the current model and assert
//     legacy fingerprints are dropped and v3 defaults populated.
func TestUpgradeState_V1ToV2_ExercisesUpgrader(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &manifestResource{}

	// Build the current (v2) schema for the response side.
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("building v2 schema: %+v", schemaResp.Diagnostics)
	}
	currentSchema := schemaResp.Schema

	// Build the prior (v1) schema from the same source the
	// upgrader uses.
	priorSchema := priorManifestSchemaV1(ctx)

	// Seed a tfsdk.State at the prior schema with a representative
	// v1 model: the legacy fingerprints are populated; the four
	// new drift attributes are absent (the prior schema has no
	// such attributes, so they're not encoded into the raw value).
	priorState := tfsdk.State{
		Schema: priorSchema,
		Raw:    tftypes.NewValue(priorSchema.Type().TerraformType(ctx), nil),
	}
	v1 := manifestResourceModelV1{
		ID:                    types.StringValue("/api/v1/configmaps/x"),
		UID:                   types.StringValue("u-1"),
		LiveUID:               types.StringValue("u-1"),
		YAMLInCluster:         types.StringValue("legacy-fp-A"),
		LiveManifestInCluster: types.StringValue("legacy-fp-B"),
		APIVersion:            types.StringValue("v1"),
		Kind:                  types.StringValue("ConfigMap"),
		Name:                  types.StringValue("x"),
		Namespace:             types.StringValue("default"),
		YAMLBody:              types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
		YAMLBodyParsed:        types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
		SensitiveFields:       types.ListNull(types.StringType),
		IgnoreFields:          types.ListNull(types.StringType),
		WaitFor:               types.ListNull(waitForObjectType()),
		Timeouts:              nullTimeouts(),
	}
	if diags := priorState.Set(ctx, &v1); diags.HasError() {
		t.Fatalf("seeding prior state: %+v", diags)
	}

	// Call the registered v1 upgrader. UpgradeState builds the
	// upgrader map fresh on each call.
	upgraders := r.UpgradeState(ctx)
	entry, ok := upgraders[1]
	if !ok {
		t.Fatalf("UpgradeState missing entry for prior schema version 1")
	}
	if entry.PriorSchema == nil {
		t.Fatalf("PriorSchema must be declared on the v1 -> v2 upgrader; nil PriorSchema crashed UpgradeResourceState in production")
	}

	req := resource.UpgradeStateRequest{State: &priorState}
	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: currentSchema,
			Raw:    tftypes.NewValue(currentSchema.Type().TerraformType(ctx), nil),
		},
	}
	entry.StateUpgrader(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrader emitted diagnostics: %+v", resp.Diagnostics)
	}

	// Verify the response state matches the v2 shape: legacy
	// fingerprints gone, drift attrs populated with safe defaults,
	// passthrough fields preserved.
	var got manifestResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("reading upgraded state: %+v", diags)
	}

	if got.ID.ValueString() != "/api/v1/configmaps/x" {
		t.Errorf("id passthrough lost: %q", got.ID.ValueString())
	}
	if got.UID.ValueString() != "u-1" {
		t.Errorf("uid passthrough lost: %q", got.UID.ValueString())
	}
	if got.Drift.ValueString() != "" {
		t.Errorf("post-upgrade drift should be the in-sync sentinel; got %q", got.Drift.ValueString())
	}
	if got.ShowDriftValues.ValueString() != "none" {
		t.Errorf("show_drift_values default: got %q want %q", got.ShowDriftValues.ValueString(), "none")
	}
	if got.DriftEngine.ValueString() != "client" {
		t.Errorf("drift_engine default: got %q want %q", got.DriftEngine.ValueString(), "client")
	}
	if !got.MaskPaths.IsNull() {
		t.Errorf("mask_paths should be null after upgrade; got %+v", got.MaskPaths)
	}
}

// TestUpgradeState_RegistersBothVersions verifies the upgrader
// registers entries for both prior schema versions (0 and 1), each
// with a PriorSchema set. Catches the omission that crashed CI.
func TestUpgradeState_RegistersBothVersions(t *testing.T) {
	t.Parallel()
	r := &manifestResource{}
	upgraders := r.UpgradeState(context.Background())
	for _, v := range []int64{0, 1} {
		entry, ok := upgraders[v]
		if !ok {
			t.Errorf("UpgradeState missing entry for version %d", v)
			continue
		}
		if entry.PriorSchema == nil {
			t.Errorf("version %d upgrader missing PriorSchema (regression: nil-pointer panic in UpgradeResourceState)", v)
		}
		if entry.StateUpgrader == nil {
			t.Errorf("version %d upgrader missing StateUpgrader callback", v)
		}
	}
}
