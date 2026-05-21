package framework_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccKubectlDataSourceFileDocuments_single(t *testing.T) {
	t.Parallel()

	cfg := `
data "kubectl_file_documents" "test" {
  content = <<YAML
kind: Service1
YAML
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "documents.#", "1"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "documents.0", "kind: Service1"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "manifests.%", "1"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "manifests./apis/service1s", "kind: Service1\n"),
				resource.TestCheckResourceAttrSet("data.kubectl_file_documents.test", "id"),
			),
		}},
	})
}

func TestAccKubectlDataSourceFileDocuments_multiple(t *testing.T) {
	t.Parallel()

	cfg := `
data "kubectl_file_documents" "test" {
  content = <<YAML
kind: Service1
---
kind: Service2
YAML
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "documents.#", "2"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "documents.0", "kind: Service1"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "documents.1", "kind: Service2"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "manifests.%", "2"),
			),
		}},
	})
}

// TestAccKubectlDataSourceFileDocuments_emptyAndComments confirms the SDK v2
// behaviour is preserved: empty separators / comment-only stanzas drop out
// of `documents`, and only the two real Services survive.
func TestAccKubectlDataSourceFileDocuments_emptyAndComments(t *testing.T) {
	t.Parallel()

	cfg := `
data "kubectl_file_documents" "test" {
  content = <<YAML
kind: Service1
---
# just a comment
---
kind: Service2
---
YAML
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "documents.#", "2"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "manifests.%", "2"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "manifests./apis/service1s", "kind: Service1\n"),
				resource.TestCheckResourceAttr("data.kubectl_file_documents.test", "manifests./apis/service2s", "kind: Service2\n"),
			),
		}},
	})
}
