package framework_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccKubectlDataSourceFilenameList_basic(t *testing.T) {
	t.Parallel()

	path := "../../_examples/crds"
	cfg := fmt.Sprintf(`
data "kubectl_filename_list" "test" {
  pattern = "%s/*"
}
`, path)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_filename_list.test", "matches.#", "1"),
				resource.TestCheckResourceAttr("data.kubectl_filename_list.test", "matches.0", path+"/couchbase.tf"),
				resource.TestCheckResourceAttr("data.kubectl_filename_list.test", "basenames.#", "1"),
				resource.TestCheckResourceAttr("data.kubectl_filename_list.test", "basenames.0", "couchbase.tf"),
				resource.TestCheckResourceAttrSet("data.kubectl_filename_list.test", "id"),
			),
		}},
	})
}

func TestAccKubectlDataSourceFilenameList_noMatches(t *testing.T) {
	t.Parallel()

	cfg := `
data "kubectl_filename_list" "test" {
  pattern = "../../_examples/this-path-definitely-does-not-exist/*"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_filename_list.test", "matches.#", "0"),
				resource.TestCheckResourceAttr("data.kubectl_filename_list.test", "basenames.#", "0"),
				resource.TestCheckResourceAttrSet("data.kubectl_filename_list.test", "id"),
			),
		}},
	})
}
