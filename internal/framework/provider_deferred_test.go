package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestConfigureDeferredActions pins the deferred-actions behaviour added for
// issue #354. When Terraform allows deferral (e.g. a Stacks workflow) and the
// provider configuration is not fully known, Configure must return a
// provider-level deferral so the framework defers every resource and data
// source instead of letting core call ValidateResourceConfig against an
// unconfigured provider. The capability is gated: an unknown config without
// DeferralAllowed must NOT defer, and a fully-known config must never defer.
func TestConfigureDeferredActions(t *testing.T) {
	ctx := context.Background()
	p := New("test")

	var schemaResp provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &schemaResp)
	cfgType, ok := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("provider schema type is %T, want tftypes.Object", schemaResp.Schema.Type().TerraformType(ctx))
	}

	// allNull builds a known provider-config object whose attributes are all
	// null (IsFullyKnown == true). partialUnknown overrides one attribute
	// (host) with an unknown value, which is what Terraform sends when an
	// argument is sourced from a not-yet-applied component's output.
	allNull := func() map[string]tftypes.Value {
		attrs := make(map[string]tftypes.Value, len(cfgType.AttributeTypes))
		for name, at := range cfgType.AttributeTypes {
			attrs[name] = tftypes.NewValue(at, nil)
		}
		return attrs
	}

	knownCfg := tftypes.NewValue(cfgType, allNull())

	partial := allNull()
	partial["host"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	unknownCfg := tftypes.NewValue(cfgType, partial)

	cases := []struct {
		name            string
		raw             tftypes.Value
		deferralAllowed bool
		wantDeferred    bool
	}{
		{"unknown config with deferral allowed defers", unknownCfg, true, true},
		{"unknown config without deferral allowed does not defer", unknownCfg, false, false},
		{"known config with deferral allowed does not defer", knownCfg, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := provider.ConfigureRequest{
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: tc.raw},
				ClientCapabilities: provider.ConfigureProviderClientCapabilities{
					DeferralAllowed: tc.deferralAllowed,
				},
			}
			resp := &provider.ConfigureResponse{}
			p.Configure(ctx, req, resp)

			if tc.wantDeferred {
				if resp.Deferred == nil {
					t.Fatalf("expected a deferred response, got none")
				}
				if resp.Deferred.Reason != provider.DeferredReasonProviderConfigUnknown {
					t.Errorf("deferred reason = %s, want Provider Config Unknown", resp.Deferred.Reason)
				}
				// A deferral returns before building a client, so the
				// configure step itself must not surface errors.
				if resp.Diagnostics.HasError() {
					t.Errorf("unexpected diagnostics on deferral: %s", resp.Diagnostics)
				}
				return
			}

			// Non-deferral cases fall through to normal configure. We only
			// assert that no deferral was requested; whether building a
			// client from null/empty config succeeds depends on the host
			// environment and is not what this test pins.
			if resp.Deferred != nil {
				t.Fatalf("did not expect a deferred response, got reason %s", resp.Deferred.Reason)
			}
		})
	}
}
