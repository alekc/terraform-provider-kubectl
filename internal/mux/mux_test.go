package mux

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// TestMuxServer_GetProviderSchemaIsClean pins the fix for issue #275.
//
// Background: tf6muxserver.NewMuxServer compares the provider configuration
// schema returned by every muxed server and emits an error diagnostic when
// they differ. Versions before 2.3.1 shipped with the framework half
// declaring an empty schema while the SDK v2 half declared the full set,
// which broke `terraform plan` at provider startup for every user (the
// terraform-plugin-testing harness used by acc tests does not surface that
// diagnostic, so CI did not catch it).
//
// This test invokes GetProviderSchema directly and fails if any error-
// severity diagnostic comes back. The two halves of the schema live in
// `kubernetes/provider.go` (SDK v2) and `internal/framework/provider.go`
// (framework) — when adding, renaming, or re-describing a provider
// attribute, both must be touched, and this test will fail otherwise.
func TestMuxServer_GetProviderSchemaIsClean(t *testing.T) {
	ctx := context.Background()
	server, err := MuxServer(ctx, "test")
	if err != nil {
		t.Fatalf("MuxServer: %v", err)
	}
	resp, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	for i, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("diagnostic[%d]: %s — %s", i, d.Summary, d.Detail)
		}
	}
}
