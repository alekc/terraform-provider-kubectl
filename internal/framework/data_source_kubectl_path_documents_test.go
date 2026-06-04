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

// TestAccKubectlDataSourcePathDocuments_emptyMapsByDefault pins the v2
// behaviour of `vars` and `sensitive_vars` defaulting to an empty map
// when the user omits them. Regression test for #328: Phase F's
// framework rewrite shipped `types.MapNull` here so downstream
// `keys(data.x.vars)` started failing with "argument must not be null".
// The state count attribute `.%` is `"0"` for an empty map and absent
// for a null map, so checking for `"0"` doubles as a null-rejection
// assertion.
func TestAccKubectlDataSourcePathDocuments_emptyMapsByDefault(t *testing.T) {
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
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "vars.%", "0"),
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "sensitive_vars.%", "0"),
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
// data source. The fixture single-templated.yaml has one placeholder
// `${the_kind}` which the test supplies.
func TestAccKubectlDataSourcePathDocuments_vars(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern = "%s/single-templated.yaml"
  vars = {
    the_kind = "CronTab"
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
				// The rendered document must contain the substituted kind.
				resource.TestMatchResourceAttr(
					"data.kubectl_path_documents.test",
					"documents.0",
					regexp.MustCompile(`kind:\s*CronTab`),
				),
			),
		}},
	})
}

// TestAccKubectlDataSourcePathDocuments_sensitiveVarsMergeWins exercises
// the merge rule: when the same key appears in both `vars` and
// `sensitive_vars`, the sensitive value takes precedence. Uses the
// single-templated.yaml fixture whose placeholder is `${the_kind}`.
func TestAccKubectlDataSourcePathDocuments_sensitiveVarsMergeWins(t *testing.T) {
	t.Parallel()
	cfg := fmt.Sprintf(`
data "kubectl_path_documents" "test" {
  pattern = "%s/single-templated.yaml"
  vars = {
    the_kind = "PlainKind"
  }
  sensitive_vars = {
    the_kind = "SensitiveKind"
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
				// The rendered manifest's kind line must come from the
				// sensitive value, not the non-sensitive one.
				resource.TestMatchResourceAttr(
					"data.kubectl_path_documents.test",
					"documents.0",
					regexp.MustCompile(`kind:\s*SensitiveKind`),
				),
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
				// `${the_kind}` as literal text. Assert the placeholder
				// survives end-to-end so a regression that silently re-enables
				// template rendering can't slide past on document count alone.
				resource.TestCheckResourceAttr("data.kubectl_path_documents.test", "documents.#", "1"),
				resource.TestMatchResourceAttr(
					"data.kubectl_path_documents.test",
					"documents.0",
					regexp.MustCompile(`\$\{the_kind\}`),
				),
			),
		}},
	})
}

// TestAccKubectlDataSourcePathDocuments_varsListRejected confirms a list
// value under `vars` is rejected at config-validate time. The schema
// declares `ElementType: types.StringType`, so the framework emits an
// `Incorrect attribute value type` diagnostic on the way in.
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
			ExpectError: regexp.MustCompile(`(?is)Incorrect attribute value type.*string required`),
		}},
	})
}
