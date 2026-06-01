package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// newSourceState builds a tfsdk.State bound to the gavinbunney source schema
// and populated from src, mimicking what the framework hands a StateMover
// when SourceSchema is set.
func newSourceState(ctx context.Context, t *testing.T, src gavinbunneyManifestSourceModel) *tfsdk.State {
	t.Helper()
	srcSchema := gavinbunneyManifestSourceSchema()
	state := &tfsdk.State{
		Schema: *srcSchema,
		Raw:    tftypes.NewValue(srcSchema.Type().TerraformType(ctx), nil),
	}
	if diags := state.Set(ctx, &src); diags.HasError() {
		t.Fatalf("seeding source state: %+v", diags)
	}
	return state
}

// newTargetState builds an empty tfsdk.State bound to the real resource
// schema, mimicking the framework's pre-population of MoveStateResponse.
func newTargetState(ctx context.Context, t *testing.T) tfsdk.State {
	t.Helper()
	r := &manifestResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("building target schema: %+v", schemaResp.Diagnostics)
	}
	return tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
}

func sampleGavinbunneySource() gavinbunneyManifestSourceModel {
	return gavinbunneyManifestSourceModel{
		ID:                    types.StringValue("/api/v1/namespaces/default/configmaps/demo"),
		UID:                   types.StringValue("uid-abc"),
		LiveUID:               types.StringValue("uid-abc"),
		YAMLInCluster:         types.StringValue("fp-1"),
		LiveManifestInCluster: types.StringValue("fp-1"),
		APIVersion:            types.StringValue("v1"),
		Kind:                  types.StringValue("ConfigMap"),
		Name:                  types.StringValue("demo"),
		Namespace:             types.StringValue("default"),
		OverrideNamespace:     types.StringNull(),
		YAMLBody:              types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n"),
		YAMLBodyParsed:        types.StringValue("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n"),
		SensitiveFields:       types.ListValueMust(types.StringType, []attr.Value{}),
		ForceNew:              types.BoolValue(false),
		ServerSideApply:       types.BoolValue(true),
		ForceConflicts:        types.BoolValue(false),
		ApplyOnly:             types.BoolValue(false),
		IgnoreFields:          types.ListValueMust(types.StringType, []attr.Value{types.StringValue("status")}),
		Wait:                  types.BoolValue(false),
		WaitForRollout:        types.BoolValue(true),
		ValidateSchema:        types.BoolValue(true),
	}
}

// TestMoveFromGavinbunney_HappyPath asserts the 20 shared attributes pass
// through verbatim, id is preserved, and the 4 alekc-only attributes take
// their documented defaults.
func TestMoveFromGavinbunney_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	req := resource.MoveStateRequest{
		SourceTypeName:        gavinbunneyManifestTypeName,
		SourceProviderAddress: "registry.terraform.io/gavinbunney/kubectl",
		SourceSchemaVersion:   1,
		SourceState:           newSourceState(ctx, t, sampleGavinbunneySource()),
	}
	resp := &resource.MoveStateResponse{TargetState: newTargetState(ctx, t)}

	moveFromGavinbunneyManifest(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", resp.Diagnostics)
	}

	var got manifestResourceModel
	if diags := resp.TargetState.Get(ctx, &got); diags.HasError() {
		t.Fatalf("reading target state: %+v", diags)
	}

	// Shared passthrough spot-checks.
	if got.ID.ValueString() != "/api/v1/namespaces/default/configmaps/demo" {
		t.Errorf("id not preserved: %q", got.ID.ValueString())
	}
	if got.UID.ValueString() != "uid-abc" {
		t.Errorf("uid not preserved: %q", got.UID.ValueString())
	}
	if got.Kind.ValueString() != "ConfigMap" {
		t.Errorf("kind not preserved: %q", got.Kind.ValueString())
	}
	if !got.ServerSideApply.ValueBool() {
		t.Errorf("server_side_apply not preserved")
	}
	if got.IgnoreFields.IsNull() || len(got.IgnoreFields.Elements()) != 1 {
		t.Errorf("ignore_fields not preserved: %+v", got.IgnoreFields)
	}

	// alekc-only defaults.
	if got.UpgradeAPIVersion.ValueBool() != false {
		t.Errorf("upgrade_api_version default: got %v, want false", got.UpgradeAPIVersion.ValueBool())
	}
	if got.FieldManager.ValueString() != defaultFieldManager {
		t.Errorf("field_manager default: got %q, want %q", got.FieldManager.ValueString(), defaultFieldManager)
	}
	if !got.DeleteCascade.IsNull() {
		t.Errorf("delete_cascade should be null, got %q", got.DeleteCascade.ValueString())
	}
	if !got.WaitFor.IsNull() {
		t.Errorf("wait_for should be null, got %+v", got.WaitFor)
	}
}

// TestMoveFromGavinbunney_RecomputesYAMLFingerprint asserts the mover
// replaces the gavinbunney-stored yaml_incluster (and live_manifest_incluster)
// with an alekc-canonical baseline computed from src.YAMLBody via
// kubernetes.GetLiveManifestFields. The cross-provider smoke job depends on
// this: if we passed gavinbunney's fingerprint through unchanged, alekc's
// drift check in ModifyPlan would fire post-Read against alekc's freshly
// computed live fingerprint, producing a non-empty plan on the first
// terraform plan after the move.
func TestMoveFromGavinbunney_RecomputesYAMLFingerprint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := sampleGavinbunneySource()
	// Sentinel value the recomputation must overwrite.
	src.YAMLInCluster = types.StringValue("gavinbunney-style-fp")
	src.LiveManifestInCluster = types.StringValue("gavinbunney-style-fp")

	req := resource.MoveStateRequest{
		SourceTypeName:        gavinbunneyManifestTypeName,
		SourceProviderAddress: "registry.terraform.io/gavinbunney/kubectl",
		SourceSchemaVersion:   1,
		SourceState:           newSourceState(ctx, t, src),
	}
	resp := &resource.MoveStateResponse{TargetState: newTargetState(ctx, t)}

	moveFromGavinbunneyManifest(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", resp.Diagnostics)
	}

	var got manifestResourceModel
	if diags := resp.TargetState.Get(ctx, &got); diags.HasError() {
		t.Fatalf("reading target state: %+v", diags)
	}

	if got.YAMLInCluster.ValueString() == "gavinbunney-style-fp" {
		t.Errorf("yaml_incluster should be recomputed, still equals sentinel")
	}
	if got.LiveManifestInCluster.ValueString() == "gavinbunney-style-fp" {
		t.Errorf("live_manifest_incluster should be recomputed, still equals sentinel")
	}
	if got.YAMLInCluster.ValueString() != got.LiveManifestInCluster.ValueString() {
		t.Errorf("post-move yaml_incluster and live_manifest_incluster must match: %q vs %q",
			got.YAMLInCluster.ValueString(), got.LiveManifestInCluster.ValueString())
	}
	if got.YAMLInCluster.ValueString() == "" {
		t.Errorf("expected a non-empty alekc-canonical fingerprint, got empty")
	}
}

// TestMoveFromGavinbunney_Skips asserts the mover stays silent (no state, no
// diagnostics) when the source is not gavinbunney/kubectl kubectl_manifest, so
// the framework can try other movers.
func TestMoveFromGavinbunney_Skips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name     string
		typeName string
		address  string
	}{
		{"wrong type name", "kubectl_server_version", "registry.terraform.io/gavinbunney/kubectl"},
		{"wrong provider", gavinbunneyManifestTypeName, "registry.terraform.io/hashicorp/kubernetes"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := resource.MoveStateRequest{
				SourceTypeName:        tc.typeName,
				SourceProviderAddress: tc.address,
				SourceSchemaVersion:   1,
				SourceState:           newSourceState(ctx, t, sampleGavinbunneySource()),
			}
			target := newTargetState(ctx, t)
			resp := &resource.MoveStateResponse{TargetState: target}

			moveFromGavinbunneyManifest(ctx, req, resp)

			if resp.Diagnostics.HasError() {
				t.Errorf("expected no diagnostics on skip, got %+v", resp.Diagnostics)
			}
			// A skipped mover must leave the target state untouched (still the
			// all-null object the framework pre-populated).
			if !resp.TargetState.Raw.Equal(target.Raw) {
				t.Errorf("expected target state untouched on skip")
			}
		})
	}
}

// TestMoveFromGavinbunney_UnsupportedSchemaVersion asserts that a matched
// request with a source schema version other than 1 is rejected with an
// error rather than decoded against the version-1 source schema.
func TestMoveFromGavinbunney_UnsupportedSchemaVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	req := resource.MoveStateRequest{
		SourceTypeName:        gavinbunneyManifestTypeName,
		SourceProviderAddress: "registry.terraform.io/gavinbunney/kubectl",
		SourceSchemaVersion:   2,
		SourceState:           newSourceState(ctx, t, sampleGavinbunneySource()),
	}
	resp := &resource.MoveStateResponse{TargetState: newTargetState(ctx, t)}

	moveFromGavinbunneyManifest(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected an error diagnostic for unsupported source schema version")
	}
}

// TestMoveFromGavinbunney_MatchedButNilSource asserts that once the type name
// and provider address match, a nil SourceState is a hard error (not a silent
// skip), so the practitioner sees a real message instead of "implementation
// not found".
func TestMoveFromGavinbunney_MatchedButNilSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	req := resource.MoveStateRequest{
		SourceTypeName:        gavinbunneyManifestTypeName,
		SourceProviderAddress: "registry.terraform.io/gavinbunney/kubectl",
		SourceSchemaVersion:   1,
		SourceState:           nil,
	}
	resp := &resource.MoveStateResponse{TargetState: newTargetState(ctx, t)}

	moveFromGavinbunneyManifest(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected an error diagnostic for matched request with nil source state")
	}
}
