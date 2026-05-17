package kubernetes

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
)

// TestAccKubectlDataSourceManifest_namespacedConfigMap creates a ConfigMap via
// the kubectl_manifest resource, then reads it back through the new data
// source and extracts a scalar field.
func TestAccKubectlDataSourceManifest_namespacedConfigMap(t *testing.T) {
	name := fmt.Sprintf("acc-test-cm-%s", acctest.RandString(8))
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
  greeting: hello
YAML
}

data "kubectl_manifest" "read" {
  depends_on  = [kubectl_manifest.seed]
  api_version = "v1"
  kind        = "ConfigMap"
  name        = "%s"
  namespace   = "default"
  fields = {
    region = "data.region"
  }
}
`, name, name)

	resource.Test(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccCheckkubectlDestroy,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.kubectl_manifest.read", "kind", "ConfigMap"),
					resource.TestCheckResourceAttr("data.kubectl_manifest.read", "namespace", "default"),
					resource.TestCheckResourceAttr("data.kubectl_manifest.read", "results.region", "eu-west-1"),
					resource.TestCheckResourceAttrSet("data.kubectl_manifest.read", "uid"),
					resource.TestCheckResourceAttrSet("data.kubectl_manifest.read", "yaml"),
					resource.TestCheckResourceAttrSet("data.kubectl_manifest.read", "json"),
				),
			},
		},
	})
}

// TestAccKubectlDataSourceManifest_clusterScoped reads a cluster-scoped
// built-in (kube-system Namespace) without supplying namespace. Verifies
// the existing GetRestClientFromUnstructured cluster-vs-namespaced
// detection works end-to-end through the data source.
func TestAccKubectlDataSourceManifest_clusterScoped(t *testing.T) {
	cfg := `
data "kubectl_manifest" "ns" {
  api_version = "v1"
  kind        = "Namespace"
  name        = "kube-system"
  fields = {
    phase = "status.phase"
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:  func() { testAccPreCheck(t) },
		Providers: testAccProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.kubectl_manifest.ns", "kind", "Namespace"),
					resource.TestCheckResourceAttr("data.kubectl_manifest.ns", "results.phase", "Active"),
					resource.TestCheckResourceAttrSet("data.kubectl_manifest.ns", "uid"),
				),
			},
		},
	})
}

// TestAccKubectlDataSourceManifest_arrayIndex verifies gojsonq array-index
// paths work end-to-end (`spec.template.spec.containers.0.image`).
func TestAccKubectlDataSourceManifest_arrayIndex(t *testing.T) {
	name := fmt.Sprintf("acc-test-dep-%s", acctest.RandString(8))
	cfg := fmt.Sprintf(`
resource "kubectl_manifest" "seed" {
  wait = true
  yaml_body = <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: 2
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
        - name: nginx
          image: nginx:1.25
YAML
}

data "kubectl_manifest" "read" {
  depends_on  = [kubectl_manifest.seed]
  api_version = "apps/v1"
  kind        = "Deployment"
  name        = "%s"
  namespace   = "default"
  fields = {
    replicas    = "spec.replicas"
    first_image = "spec.template.spec.containers.0.image"
  }
}
`, name, name, name, name)

	resource.Test(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccCheckkubectlDestroy,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.kubectl_manifest.read", "results.replicas", "2"),
					resource.TestCheckResourceAttr("data.kubectl_manifest.read", "results.first_image", "nginx:1.25"),
				),
			},
		},
	})
}

// TestAccKubectlDataSourceManifest_notFound asserts the data source errors
// loudly when the requested object does not exist.
func TestAccKubectlDataSourceManifest_notFound(t *testing.T) {
	cfg := `
data "kubectl_manifest" "missing" {
  api_version = "v1"
  kind        = "ConfigMap"
  name        = "definitely-not-here-acceptance-test"
  namespace   = "default"
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:  func() { testAccPreCheck(t) },
		Providers: testAccProviders,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile("(?i)not.?found"),
			},
		},
	})
}

// TestAccKubectlDataSourceManifest_missingFieldPath asserts that an
// unresolved gojsonq path under `fields` produces an error naming the
// offending key.
func TestAccKubectlDataSourceManifest_missingFieldPath(t *testing.T) {
	cfg := `
data "kubectl_manifest" "ns" {
  api_version = "v1"
  kind        = "Namespace"
  name        = "kube-system"
  fields = {
    bogus = "spec.does.not.exist"
  }
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:  func() { testAccPreCheck(t) },
		Providers: testAccProviders,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`fields\["bogus"\]`),
			},
		},
	})
}
