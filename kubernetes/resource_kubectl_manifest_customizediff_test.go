package kubernetes

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// TestCustomizeDiff_ForceConflictsRequiresSSA mirrors the framework-half
// TestModifyPlan_ForceConflictsRequiresSSA: the SDK v2 path must reject
// force_conflicts = true when server_side_apply = false (#309), and
// pass every other combination.
func TestCustomizeDiff_ForceConflictsRequiresSSA(t *testing.T) {
	t.Parallel()
	res := resourceKubectlManifest()
	const baseYAML = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"

	cases := []struct {
		name          string
		raw           map[string]interface{}
		wantErrSubstr string
	}{
		{
			name: "both false: ok",
			raw: map[string]interface{}{
				"yaml_body":         baseYAML,
				"force_conflicts":   false,
				"server_side_apply": false,
			},
		},
		{
			name: "ssa true, force false: ok",
			raw: map[string]interface{}{
				"yaml_body":         baseYAML,
				"force_conflicts":   false,
				"server_side_apply": true,
			},
		},
		{
			name: "ssa true, force true: ok",
			raw: map[string]interface{}{
				"yaml_body":         baseYAML,
				"force_conflicts":   true,
				"server_side_apply": true,
			},
		},
		{
			name: "force true, ssa false: rejected",
			raw: map[string]interface{}{
				"yaml_body":         baseYAML,
				"force_conflicts":   true,
				"server_side_apply": false,
			},
			wantErrSubstr: "force_conflicts = true has no effect with the default client-side apply",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := terraform.NewResourceConfigRaw(tc.raw)
			_, err := res.Diff(context.Background(), nil, cfg, nil)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("expected error containing %q, got: %s", tc.wantErrSubstr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
		})
	}
}
