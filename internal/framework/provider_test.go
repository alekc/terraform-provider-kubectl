package framework

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestInt64AtLeastValidator pins the schema-level guard for issue #274:
// apply_retry_count = -1 historically wrapped through the signed-to-unsigned
// cast in BuildKubeProvider; we now reject it at plan time so the misuse
// surfaces as an attribute error against the user's config instead of
// (silently) becoming infinite retries at apply time.
//
// Covers null + unknown pass-through (composes with Optional), the minimum
// boundary, and a value below the minimum.
func TestInt64AtLeastValidator(t *testing.T) {
	v := int64AtLeastValidator{min: 0}

	cases := []struct {
		name   string
		value  types.Int64
		wantOK bool
		needle string
	}{
		{"null passes", types.Int64Null(), true, ""},
		{"unknown passes", types.Int64Unknown(), true, ""},
		{"minimum passes", types.Int64Value(0), true, ""},
		{"above minimum passes", types.Int64Value(5), true, ""},
		{"below minimum fails", types.Int64Value(-1), false, "at least 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validator.Int64Request{
				Path:           path.Root("apply_retry_count"),
				PathExpression: path.MatchRoot("apply_retry_count"),
				ConfigValue:    tc.value,
			}
			resp := &validator.Int64Response{}
			v.ValidateInt64(context.Background(), req, resp)

			hasError := resp.Diagnostics.HasError()
			if tc.wantOK && hasError {
				t.Fatalf("unexpected diagnostics: %s", resp.Diagnostics)
			}
			if !tc.wantOK && !hasError {
				t.Fatalf("expected an attribute error, got none")
			}
			if tc.needle != "" {
				var matched bool
				for _, d := range resp.Diagnostics.Errors() {
					if strings.Contains(d.Detail(), tc.needle) || strings.Contains(d.Summary(), tc.needle) {
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("diagnostic did not mention %q: %s", tc.needle, resp.Diagnostics)
				}
			}
		})
	}

	// Sanity-check Description / MarkdownDescription return something useful
	// so the user sees a meaningful message in `terraform validate` output.
	if got := v.Description(context.Background()); !strings.Contains(got, "0") {
		t.Errorf("Description does not mention the minimum: %s", got)
	}
	if got := v.MarkdownDescription(context.Background()); !strings.Contains(got, "0") {
		t.Errorf("MarkdownDescription does not mention the minimum: %s", got)
	}

	// Compile-time assertion that the validator implements the interface
	// (catches accidental signature drift from upstream).
	var _ validator.Int64 = v

	// attr is only imported so the test stays close to a real plan-time
	// invocation that the framework would do. Tag-use anchor to keep the
	// import live without a separate _ = attr.Type assignment.
	_ = (attr.Value)(types.Int64Null())
}
