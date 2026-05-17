package framework_test

import (
	"context"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/alekc/terraform-provider-kubectl/internal/mux"
)

// testAccProtoV6ProviderFactories returns the muxed provider (SDK v2 + framework)
// under the `kubectl` type name. Mirrors the pattern used by
// terraform-provider-kubernetes for ephemeral resource acceptance tests.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"kubectl": func() (tfprotov6.ProviderServer, error) {
		return mux.MuxServer(context.Background(), "test")
	},
}

func testAccPreCheck(t *testing.T) {
	if v := os.Getenv("KUBECONFIG"); v == "" {
		t.Fatal("KUBECONFIG must be set for acceptance tests")
	}
}

// TestAccKubectlEphemeralManifest_clusterScoped reads the kube-system
// Namespace via the ephemeral resource. Verifies the mux + framework
// resource + shared FetchManifest helper all wire up correctly.
//
// Ephemeral values cannot flow into outputs, so we consume the result via a
// `check` block that asserts on `phase`. A failing check produces a test
// failure with a stable error message we can match.
func TestAccKubectlEphemeralManifest_clusterScoped(t *testing.T) {
	cfg := `
ephemeral "kubectl_manifest" "ns" {
  api_version = "v1"
  kind        = "Namespace"
  name        = "kube-system"
  fields = {
    phase = "status.phase"
  }
}

check "ns_active" {
  assert {
    condition     = ephemeral.kubectl_manifest.ns.results["phase"] == "Active"
    error_message = "expected kube-system phase Active, got: ${ephemeral.kubectl_manifest.ns.results["phase"]}"
  }
}
`
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_10_0),
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
			},
		},
	})
}

// TestAccKubectlEphemeralManifest_notFound asserts the ephemeral resource
// errors when the target object does not exist.
func TestAccKubectlEphemeralManifest_notFound(t *testing.T) {
	cfg := `
ephemeral "kubectl_manifest" "missing" {
  api_version = "v1"
  kind        = "ConfigMap"
  name        = "definitely-not-here-ephemeral-acceptance"
  namespace   = "default"
}

check "stub" {
  assert {
    condition     = length(ephemeral.kubectl_manifest.missing.yaml) > 0
    error_message = "unreachable"
  }
}
`
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_10_0),
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile("(?i)not.?found"),
			},
		},
	})
}
