package framework_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccKubectlServerVersion_dataSource reads the apiserver version via the
// framework-side `kubectl_server_version` data source and asserts that the
// computed `git_version` field is populated. Smoke-level: proves mux + framework
// data source + FetchServerVersion helper all wire up.
func TestAccKubectlServerVersion_dataSource(t *testing.T) {
	t.Parallel()

	cfg := `
data "kubectl_server_version" "current" {}

check "version_populated" {
  assert {
    condition     = length(data.kubectl_server_version.current.git_version) > 0
    error_message = "expected non-empty git_version, got '${data.kubectl_server_version.current.git_version}'"
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				// All nine attributes should be populated against any real cluster.
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "id"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "version"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "major"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "minor"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "patch"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "git_version"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "git_commit"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "build_date"),
				resource.TestCheckResourceAttrSet("data.kubectl_server_version.current", "platform"),
			),
		}},
	})
}

// TestAccKubectlServerVersion_resource exercises the resource form: Create
// fetches the version into state, Read refreshes it, and changing the
// `triggers` map forces destroy+recreate. The trigger-driven replacement
// contract is the only behaviour worth testing on this resource - everything
// else is computed - so the middle step uses a PlanOnly assertion against
// `plancheck.ExpectResourceAction(..., ResourceActionReplace)` to pin the
// fact that the RequiresReplace plan modifier is doing its job. Without that
// assertion, a silent regression to in-place updates would still pass the
// post-apply value checks.
func TestAccKubectlServerVersion_resource(t *testing.T) {
	t.Parallel()

	cfgInitial := `
resource "kubectl_server_version" "current" {
  triggers = {
    cluster = "v1"
  }
}
`
	cfgRotated := `
resource "kubectl_server_version" "current" {
  triggers = {
    cluster = "v2"
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Step 1: Create v1 baseline.
				Config: cfgInitial,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kubectl_server_version.current", "git_version"),
					resource.TestCheckResourceAttrSet("kubectl_server_version.current", "id"),
					resource.TestCheckResourceAttr("kubectl_server_version.current", "triggers.cluster", "v1"),
				),
			},
			{
				// Step 2: Apply v2. The PreApply plan check pins the
				// trigger-driven replacement contract: flipping
				// triggers.cluster MUST produce a Replace action, not an
				// in-place Update. A regression that drops the
				// RequiresReplace plan modifier would let the post-apply
				// `triggers.cluster=v2` check pass while silently breaking
				// the contract; this plan check fails loudly instead.
				//
				// `PlanOnly: true + PreApply` is rejected by the testing
				// framework (mutually exclusive: PlanOnly stops before
				// apply, PreApply runs between plan and apply). Folding the
				// assertion into the apply step covers both: the plan
				// check fires once, then the apply lands and the value
				// check confirms it.
				Config: cfgRotated,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"kubectl_server_version.current",
							plancheck.ResourceActionReplace,
						),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kubectl_server_version.current", "git_version"),
					resource.TestCheckResourceAttr("kubectl_server_version.current", "triggers.cluster", "v2"),
				),
			},
		},
	})
}
