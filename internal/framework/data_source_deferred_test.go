package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestDataSourceReadDeferredActions pins the resource-level deferred-actions
// behaviour for issue #356 on the data sources that take config inputs. When
// the client allows deferral and the data source configuration is not fully
// known, Read must return a deferral instead of reading from unknown values.
// The capability gate must keep the classic read path unchanged. The
// input-less server_version data source is intentionally excluded: its config
// can never be unknown, so it carries no deferral guard.
func TestDataSourceReadDeferredActions(t *testing.T) {
	ctx := context.Background()

	factories := map[string]func() datasource.DataSource{
		"filename_list":       NewFilenameListDataSource,
		"file_documents":      NewFileDocumentsDataSource,
		"path_documents":      NewPathDocumentsDataSource,
		"kustomize_documents": NewKustomizeDocumentsDataSource,
		"manifest":            NewManifestDataSource,
	}

	for name, newDS := range factories {
		t.Run(name, func(t *testing.T) {
			ds := newDS()

			var schemaResp datasource.SchemaResponse
			ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
			cfgType := schemaResp.Schema.Type().TerraformType(ctx)

			// A wholly-unknown config object is "not fully known". The
			// deferral guard runs before req.Config.Get, so the value is
			// never decoded; it stands in for any config attribute sourced
			// from a not-yet-applied resource.
			unknownCfg := tftypes.NewValue(cfgType, tftypes.UnknownValue)

			// Deferral allowed + unknown config -> defer.
			reqDefer := datasource.ReadRequest{
				Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: unknownCfg},
				ClientCapabilities: datasource.ReadClientCapabilities{DeferralAllowed: true},
			}
			respDefer := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			ds.Read(ctx, reqDefer, respDefer)

			if respDefer.Deferred == nil {
				t.Fatalf("expected a deferred read, got none")
			}
			if respDefer.Deferred.Reason != datasource.DeferredReasonDataSourceConfigUnknown {
				t.Errorf("deferred reason = %s, want Data Source Config Unknown", respDefer.Deferred.Reason)
			}

			// Capability off + unknown config -> no defer (gate preserved).
			// Read falls through; we assert only that no deferral was set,
			// not on any diagnostics the unknown config may produce.
			reqNoCap := datasource.ReadRequest{
				Config:             tfsdk.Config{Schema: schemaResp.Schema, Raw: unknownCfg},
				ClientCapabilities: datasource.ReadClientCapabilities{DeferralAllowed: false},
			}
			respNoCap := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			ds.Read(ctx, reqNoCap, respNoCap)

			if respNoCap.Deferred != nil {
				t.Errorf("did not expect a deferred read without the capability, got %s", respNoCap.Deferred.Reason)
			}
		})
	}
}
