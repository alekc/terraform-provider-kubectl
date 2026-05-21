package framework_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const kustomizeFixtureDir = "../../_examples/kustomize/base"

func TestAccKubectlDataSourceKustomizeDocuments_basic(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_kustomize_documents" "test" {
  target = "%s"
}
`, kustomizeFixtureDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				// Two resources: Namespace + ConfigMap.
				resource.TestCheckResourceAttr("data.kubectl_kustomize_documents.test", "documents.#", "2"),
				resource.TestCheckResourceAttrSet("data.kubectl_kustomize_documents.test", "id"),
				// Default load_restrictor lands as the implicit "rootOnly".
				resource.TestCheckResourceAttr("data.kubectl_kustomize_documents.test", "load_restrictor", "rootOnly"),
				resource.TestCheckResourceAttr("data.kubectl_kustomize_documents.test", "add_managed_by_label", "false"),
			),
		}},
	})
}

// TestAccKubectlDataSourceKustomizeDocuments_loadRestrictorExplicit pins the
// behaviour when load_restrictor is explicitly set; covers the rootOnly
// branch of the validator + the switch in Read.
func TestAccKubectlDataSourceKustomizeDocuments_loadRestrictorExplicit(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_kustomize_documents" "test" {
  target          = "%s"
  load_restrictor = "rootOnly"
}
`, kustomizeFixtureDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_kustomize_documents.test", "documents.#", "2"),
				resource.TestCheckResourceAttr("data.kubectl_kustomize_documents.test", "load_restrictor", "rootOnly"),
			),
		}},
	})
}

// TestAccKubectlDataSourceKustomizeDocuments_addManagedByLabel proves the
// managed-by label flag round-trips through the rendered output.
func TestAccKubectlDataSourceKustomizeDocuments_addManagedByLabel(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_kustomize_documents" "test" {
  target               = "%s"
  add_managed_by_label = true
}
`, kustomizeFixtureDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_kustomize_documents.test", "documents.#", "2"),
				resource.TestCheckResourceAttr("data.kubectl_kustomize_documents.test", "add_managed_by_label", "true"),
				// The label string itself should appear in the rendered documents.
				resource.TestMatchResourceAttr(
					"data.kubectl_kustomize_documents.test",
					"documents.0",
					regexp.MustCompile("app.kubernetes.io/managed-by"),
				),
			),
		}},
	})
}

// TestAccKubectlDataSourceKustomizeDocuments_invalidRestrictor exercises the
// validator path. An unrecognised value must produce a config-validation
// error rather than reaching the Read function.
func TestAccKubectlDataSourceKustomizeDocuments_invalidRestrictor(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_kustomize_documents" "test" {
  target          = "%s"
  load_restrictor = "bogus"
}
`, kustomizeFixtureDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?i)load_restrictor|invalid`),
		}},
	})
}

// TestAccKubectlDataSourceKustomizeDocuments_missingTarget covers the
// error path when kustomize can't find a kustomization at the target dir.
func TestAccKubectlDataSourceKustomizeDocuments_missingTarget(t *testing.T) {
	t.Parallel()
	cfg := `
data "kubectl_kustomize_documents" "test" {
  target = "../../_examples/kustomize/this-path-definitely-does-not-exist"
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?i)kustomize render failed|no such file|not found`),
		}},
	})
}
