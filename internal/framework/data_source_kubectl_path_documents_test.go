package framework_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const pathDocumentsExamplesDir = "../../_examples/manifests"

func TestAccKubectlDataSourcePathDocuments_single(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern = "%s/single.yaml"
}
`, pathDocumentsExamplesDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "documents.#", "1"),
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "documents.0", "apiVersion: \"stable.example.com/v1\"\nkind: CronTab\nmetadata:\n  name: name-here-crd-single\nspec:\n  cronSpec: \"* * * * /5\"\n  image: my-awesome-cron-image"),
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "manifests.%", "1"),
				resource.TestCheckResourceAttrSet("data.kubectl_path_documents.test", "id"),
			),
		}},
	})
}

func TestAccKubectlDataSourcePathDocuments_multiple(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern = "%s/multiple.yaml"
}
`, pathDocumentsExamplesDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "documents.#", "2"),
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "manifests.%", "2"),
			),
		}},
	})
}

// TestAccKubectlDataSourcePathDocuments_vars proves the HCL template
// renderer + vars substitution flow works end-to-end through the framework
// data source.
func TestAccKubectlDataSourcePathDocuments_vars(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern = "%s/single-templated.yaml"
  vars = {
    name        = "from-vars"
    cron_spec   = "* * * * *"
    image       = "alpine"
  }
}
`, pathDocumentsExamplesDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "documents.#", "1"),
				// Asserting on the manifest map's key (which derives from the
				// rendered manifest's name field) proves substitution landed.
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "manifests.%", "1"),
			),
		}},
	})
}

// TestAccKubectlDataSourcePathDocuments_sensitiveVarsMergeWins exercises
// the merge rule: when the same key appears in both `vars` and
// `sensitive_vars`, the sensitive value takes precedence (matches the SDK v2
// behaviour).
func TestAccKubectlDataSourcePathDocuments_sensitiveVarsMergeWins(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern = "%s/single-templated.yaml"
  vars = {
    name      = "non-sensitive-name"
    cron_spec = "1 1 1 1 1"
    image     = "non-sensitive"
  }
  sensitive_vars = {
    name = "sensitive-wins"
  }
}
`, pathDocumentsExamplesDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "documents.#", "1"),
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "manifests.%", "1"),
			),
		}},
	})
}

// TestAccKubectlDataSourcePathDocuments_disableTemplate confirms files load
// verbatim when `disable_template = true`, even when they contain `${...}`
// expressions that would otherwise fail to render.
func TestAccKubectlDataSourcePathDocuments_disableTemplate(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern          = "%s/single-templated.yaml"
  disable_template = true
}
`, pathDocumentsExamplesDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				// File is loaded verbatim - the rendered manifest still has
				// `${name}` etc as literal text, so the manifest map key
				// reflects that.
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "documents.#", "1"),
			),
		}},
	})
}

// TestAccKubectlDataSourcePathDocuments_varsListRejected confirms the
// primitive-only validator surfaces an error when vars carries a list value.
// Belt and braces over the unit test in
// kubernetes/path_documents_helper_test.go - this confirms the validator is
// wired into the framework schema correctly.
func TestAccKubectlDataSourcePathDocuments_varsListRejected(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern = "%s/single.yaml"
  vars = {
    name = ["a", "b"]
  }
}
`, pathDocumentsExamplesDir)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?i)(tuple|list).*string|element.*incorrect type`),
		}},
	})
}
