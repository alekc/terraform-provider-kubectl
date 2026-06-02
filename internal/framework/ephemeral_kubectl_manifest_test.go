package framework_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/alekc/terraform-provider-kubectl/internal/framework"
)

// skipIfOpenTofu marks the test as skipped when the CI runner pointed the
// test framework at the OpenTofu binary via TF_ACC_TERRAFORM_PATH (i.e.
// the basename is tofu rather than terraform). Ephemeral resources are a
// Terraform 1.10+ protocol feature; OpenTofu 1.10+ understands the
// protocol but its plan-walker reports a non-empty plan on the ephemeral
// refresh step in cases Terraform considers empty. Until that divergence
// resolves upstream the ephemeral acc suite is gated to Terraform only.
func skipIfOpenTofu(t *testing.T) {
	t.Helper()
	if path := os.Getenv("TF_ACC_TERRAFORM_PATH"); strings.Contains(filepath.Base(path), "tofu") {
		t.Skip("ephemeral acc tests gated to Terraform; OpenTofu reports a non-empty refresh plan for ephemeral reads")
	}
}

// testAccProtoV6ProviderFactories returns the framework-only provider
// under the `kubectl` type name. Post-#297 the SDK v2 half is gone, so
// the factory wraps framework.New directly via providerserver.NewProtocol6.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"kubectl": func() (tfprotov6.ProviderServer, error) {
		return providerserver.NewProtocol6(framework.New("test"))(), nil
	},
}

func testAccPreCheck(t *testing.T) {
	if v := os.Getenv("KUBECONFIG"); v == "" {
		t.Fatal("KUBECONFIG must be set for acceptance tests")
	}
}

// TestAccKubectlEphemeralManifest_clusterScoped reads the kube-system
// Namespace via the ephemeral resource. Verifies the framework resource
// and shared FetchManifest helper wire up correctly.
//
// Ephemeral values cannot flow into outputs, so we consume the result via a
// `check` block that asserts on `phase`. A failing check produces a test
// failure with a stable error message we can match.
func TestAccKubectlEphemeralManifest_clusterScoped(t *testing.T) {
	skipIfOpenTofu(t)
	t.Parallel()

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

// TestAccKubectlEphemeralManifest_namespacedConfigMap seeds a ConfigMap
// via the kubectl_manifest resource and reads it back via the ephemeral
// resource, asserting on an extracted scalar field. Confirms the
// namespaced path through GetRestClientFromUnstructured works through
// the ephemeral handler, not just the cluster-scoped path.
//
// Split across two TestSteps because Terraform evaluates ephemeral resources
// at pre-apply plan time even when `depends_on` points at a managed resource
// that is also being created in the same step. A single-step config that
// bundles `kubectl_manifest.seed` and the ephemeral lookup against the same
// cluster object fails the first plan with NotFound. Step 1 applies the seed
// alone; step 2 keeps the seed in config (so it stays in state) and adds the
// ephemeral block plus the assertion. By the time step 2's plan runs, the
// ConfigMap is on the cluster and the ephemeral read succeeds.
func TestAccKubectlEphemeralManifest_namespacedConfigMap(t *testing.T) {
	skipIfOpenTofu(t)
	t.Parallel()

	name := fmt.Sprintf("acc-ephemeral-cm-%s", acctest.RandString(8))
	seedCfg := fmt.Sprintf(`
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
`, name)
	readCfg := seedCfg + fmt.Sprintf(`
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
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_10_0),
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Step 1: apply the seed only. ConfigMap lands on the cluster.
				Config: seedCfg,
			},
			{
				// Step 2: ephemeral reads what step 1 created. Plan-time
				// evaluation is now safe because the ConfigMap exists.
				Config: readCfg,
			},
		},
	})
}

// TestAccKubectlEphemeralManifest_missingFieldPath asserts the framework
// half surfaces the same error shape as the SDK v2 data source when a
// gojsonq path under `fields` does not resolve.
func TestAccKubectlEphemeralManifest_missingFieldPath(t *testing.T) {
	skipIfOpenTofu(t)
	t.Parallel()

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
	skipIfOpenTofu(t)
	t.Parallel()

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
//
// Split across two TestSteps for the same reason as
// TestAccKubectlEphemeralManifest_namespacedConfigMap above: a single-step
// seed-then-read config fails the pre-apply plan because Terraform evaluates
// the ephemeral before the seed has been applied. The state-not-in
// assertion lives on step 2, after both seed and ephemeral are in play.
func TestAccKubectlEphemeralManifest_notInState(t *testing.T) {
	skipIfOpenTofu(t)
	t.Parallel()

	name := fmt.Sprintf("acc-ephemeral-state-%s", acctest.RandString(8))
	seedCfg := fmt.Sprintf(`
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
`, name)
	readCfg := seedCfg + fmt.Sprintf(`
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
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_10_0),
		},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Step 1: apply the seed only.
				Config: seedCfg,
			},
			{
				// Step 2: add the ephemeral block + the state-not-in
				// assertion. The ephemeral never lands in state regardless
				// of whether it's evaluated; the assertion proves that.
				Config: readCfg,
				ConfigStateChecks: []statecheck.StateCheck{
					ephemeralNotInState{addressPrefix: "ephemeral.kubectl_manifest.cm"},
				},
			},
		},
	})
}
