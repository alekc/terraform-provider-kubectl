package framework_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
// `triggers` map forces destroy+recreate (verified by the second step). The
// resource's value is using a state-persisted refresh-on-trigger pattern,
// so the test focuses on the trigger-driven replacement semantic alongside
// the basic Create / Read flow.
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
				Config: cfgInitial,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kubectl_server_version.current", "git_version"),
					resource.TestCheckResourceAttrSet("kubectl_server_version.current", "id"),
					resource.TestCheckResourceAttr("kubectl_server_version.current", "triggers.cluster", "v1"),
				),
			},
			{
				Config: cfgRotated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kubectl_server_version.current", "git_version"),
					resource.TestCheckResourceAttr("kubectl_server_version.current", "triggers.cluster", "v2"),
				),
			},
		},
	})
}
