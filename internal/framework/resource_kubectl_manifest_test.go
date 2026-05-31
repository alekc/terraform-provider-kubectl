package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// TestManifestResource_SchemaShape verifies the Schema() call returns a
// well-formed schema with the expected attribute count, attribute names,
// the wait_for block, and Version: 1 to match the SDK v2 schema's
// SchemaVersion. Fails fast if a future commit accidentally drops or
// renames any attribute.
func TestManifestResource_SchemaShape(t *testing.T) {
	t.Parallel()

	r := NewManifestResource()

	mdResp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "kubectl"}, mdResp)
	if mdResp.TypeName != "kubectl_manifest" {
		t.Fatalf("Metadata TypeName: got %q, want %q", mdResp.TypeName, "kubectl_manifest")
	}

	schemaResp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %+v", schemaResp.Diagnostics)
	}

	s := schemaResp.Schema
	if s.Version != 1 {
		t.Fatalf("Schema Version: got %d, want 1", s.Version)
	}

	// 24 attributes total (id + the 23 mirrored from SDK v2). wait_for is
	// modelled as a Block, not an Attribute (matches SDK v2's TypeList +
	// nested Resource shape), so it appears in s.Blocks instead.
	wantAttrs := []string{
		"id", "uid", "live_uid", "yaml_incluster", "live_manifest_incluster",
		"api_version", "kind", "name", "namespace", "override_namespace",
		"yaml_body", "yaml_body_parsed", "sensitive_fields", "force_new",
		"upgrade_api_version", "server_side_apply", "field_manager",
		"force_conflicts", "apply_only", "ignore_fields", "wait",
		"wait_for_rollout", "validate_schema", "delete_cascade",
	}
	for _, name := range wantAttrs {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("Schema.Attributes missing %q", name)
		}
	}
	if len(s.Attributes) != len(wantAttrs) {
		t.Errorf("Schema.Attributes count: got %d, want %d (extras: %v)",
			len(s.Attributes), len(wantAttrs), extraAttrs(s.Attributes, wantAttrs))
	}

	if _, ok := s.Blocks["wait_for"]; !ok {
		t.Errorf("Schema.Blocks missing %q", "wait_for")
	}
	if len(s.Blocks) != 1 {
		t.Errorf("Schema.Blocks count: got %d, want 1", len(s.Blocks))
	}
}

// extraAttrs returns the attribute names present in got but not in want, so
// the failure message points at the actual drift rather than just a count
// mismatch.
func extraAttrs[T any](got map[string]T, want []string) []string {
	expected := make(map[string]struct{}, len(want))
	for _, n := range want {
		expected[n] = struct{}{}
	}
	var extras []string
	for n := range got {
		if _, ok := expected[n]; !ok {
			extras = append(extras, n)
		}
	}
	return extras
}

