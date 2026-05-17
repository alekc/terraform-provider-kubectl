package framework_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
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

// TestAccKubectlEphemeralManifest_namespacedConfigMap seeds a ConfigMap via
// the SDK v2 kubectl_manifest resource (same muxed provider) and reads it
// back via the ephemeral resource, asserting on an extracted scalar field.
// Confirms the namespaced path through getRestClientFromUnstructured works
// from the framework half too, not just cluster-scoped.
func TestAccKubectlEphemeralManifest_namespacedConfigMap(t *testing.T) {
	name := fmt.Sprintf("acc-ephemeral-cm-%s", acctest.RandString(8))
	cfg := fmt.Sprintf(`
resource "kubectl_manifest" "seed" {
  yaml_body = <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: default
data:
  region: eu-west-1
YAML
}

ephemeral "kubectl_manifest" "cm" {
  api_version = "v1"
  kind        = "ConfigMap"
  name        = %q
  namespace   = "default"
  fields = {
    region = "data.region"
  }
  depends_on = [kubectl_manifest.seed]
}

check "region_ok" {
  assert {
    condition     = ephemeral.kubectl_manifest.cm.results["region"] == "eu-west-1"
    error_message = "expected region eu-west-1, got: ${ephemeral.kubectl_manifest.cm.results["region"]}"
  }
}
`, name, name)

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

// TestAccKubectlEphemeralManifest_missingFieldPath asserts the framework
// half surfaces the same error shape as the SDK v2 data source when a
// gojsonq path under `fields` does not resolve.
func TestAccKubectlEphemeralManifest_missingFieldPath(t *testing.T) {
	cfg := `
ephemeral "kubectl_manifest" "ns" {
  api_version = "v1"
  kind        = "Namespace"
  name        = "kube-system"
  fields = {
    bogus = "spec.does.not.exist"
  }
}

check "stub" {
  assert {
    condition     = length(ephemeral.kubectl_manifest.ns.results["bogus"]) > 0
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
				ExpectError: regexp.MustCompile(`fields\["bogus"\]`),
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

// ephemeralNotInState is a custom StateCheck that fails if any resource in
// the rendered state JSON has an address starting with the given prefix —
// used to confirm `ephemeral.kubectl_manifest.X` never lands in state.
type ephemeralNotInState struct {
	addressPrefix string
}

func (c ephemeralNotInState) CheckState(_ context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
	if req.State == nil || req.State.Values == nil || req.State.Values.RootModule == nil {
		return
	}
	for _, r := range req.State.Values.RootModule.Resources {
		if strings.HasPrefix(r.Address, c.addressPrefix) {
			resp.Error = fmt.Errorf(
				"ephemeral resource address %q found in state at %q; ephemeral resources must never persist",
				c.addressPrefix, r.Address,
			)
			return
		}
	}
}

var _ statecheck.StateCheck = ephemeralNotInState{}

// TestAccKubectlEphemeralManifest_notInState exercises the ephemeral
// resource alongside a managed seed resource and asserts that, after apply,
// the rendered state contains no entry for the ephemeral resource. This is
// the headline guarantee the ephemeral half exists to deliver.
//
// (The seed kubectl_manifest resource IS in state — that's expected and
// distinct from the ephemeral block.)
func TestAccKubectlEphemeralManifest_notInState(t *testing.T) {
	name := fmt.Sprintf("acc-ephemeral-state-%s", acctest.RandString(8))
	cfg := fmt.Sprintf(`
resource "kubectl_manifest" "seed" {
  yaml_body = <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: default
data:
  marker: present
YAML
}

ephemeral "kubectl_manifest" "cm" {
  api_version = "v1"
  kind        = "ConfigMap"
  name        = %q
  namespace   = "default"
  fields = {
    marker = "data.marker"
  }
  depends_on = [kubectl_manifest.seed]
}

check "consumed" {
  assert {
    condition     = ephemeral.kubectl_manifest.cm.results["marker"] == "present"
    error_message = "ephemeral resource did not fetch the expected marker"
  }
}
`, name, name)

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_10_0),
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					ephemeralNotInState{addressPrefix: "ephemeral.kubectl_manifest.cm"},
				},
			},
		},
	})
}
