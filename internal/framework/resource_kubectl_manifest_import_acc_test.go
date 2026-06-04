package framework_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccKubectl_ImportClusterScoped verifies the kubectl_manifest
// importer round-trips state for a cluster-scoped object (Namespace,
// 3-part ID). Regression test for #326: PR #318 dropped the SDK v2
// Importer.StateContext; this exercises the framework-side ImportState
// that restored the feature.
func TestAccKubectl_ImportClusterScoped(t *testing.T) {
	t.Parallel()

	name := acctest.RandomWithPrefix("imp-ns")
	config := fmt.Sprintf(`
resource "kubectl_manifest" "imported" {
  yaml_body = <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: %s
EOF
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckkubectlDestroy,
		Steps: []resource.TestStep{
			{Config: config},
			{
				ResourceName:      "kubectl_manifest.imported",
				ImportState:       true,
				ImportStateId:     "v1//Namespace//" + name,
				ImportStateVerify: true,
				// Fields that legitimately diverge between apply-state
				// and import-state, mirroring SDK v2 behaviour:
				//
				// yaml_body / yaml_body_parsed: importer rebuilds
				// from the stripped live object, which differs in key
				// order and quoting from the user's apply-time input.
				//
				// yaml_incluster / live_manifest_incluster: fingerprint
				// is computed over flattenedUser intersected with
				// flattenedLive. At apply time userProvided is the
				// user's small yaml_body so the fingerprint covers
				// only those keys. At import time userProvided is the
				// full stripped live object, so the fingerprint covers
				// every key the cluster surfaced. The next plan after
				// import re-fingerprints with the user's yaml_body and
				// converges; the apply-vs-import gap exists only at
				// the moment of the verify step.
				ImportStateVerifyIgnore: []string{
					// yaml_body / yaml_body_parsed: importer rebuilds
					// from the stripped live object, which differs in
					// key order and quoting from the user's apply-time
					// input. drift: computed during Read using the
					// stub manifest the importer rebuilt from the
					// live object, so it diverges at the verify step.
					// The next plan after import converges using the
					// user's yaml_body.
					"yaml_body",
					"yaml_body_parsed",
					"drift",
				},
			},
		},
	})
}

// TestAccKubectl_ImportNamespaced verifies the importer for a
// namespaced object (ConfigMap, 4-part ID). Exercises the namespace
// arm of parseManifestImportID and the namespaced branch of
// buildManifestImportYAMLStub.
func TestAccKubectl_ImportNamespaced(t *testing.T) {
	t.Parallel()

	ns := acctest.RandomWithPrefix("imp-cm-ns")
	cm := acctest.RandomWithPrefix("imp-cm")
	config := fmt.Sprintf(`
resource "kubectl_manifest" "imp_ns" {
  yaml_body = <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: %s
EOF
}

resource "kubectl_manifest" "imported" {
  depends_on = [kubectl_manifest.imp_ns]
  yaml_body = <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  key: value
EOF
}
`, ns, cm, ns)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckkubectlDestroy,
		Steps: []resource.TestStep{
			{Config: config},
			{
				ResourceName:      "kubectl_manifest.imported",
				ImportState:       true,
				ImportStateId:     fmt.Sprintf("v1//ConfigMap//%s//%s", cm, ns),
				ImportStateVerify: true,
				// Match the cluster-scoped test's verify-ignore: the
				// fingerprint helper computes a different value for
				// imports (userProvided is the full live object) than
				// for applies (userProvided is the user's yaml_body),
				// so yaml_incluster / live_manifest_incluster diverge
				// at the verify step alongside yaml_body / yaml_body_parsed.
				ImportStateVerifyIgnore: []string{
					// yaml_body / yaml_body_parsed: importer rebuilds
					// from the stripped live object, which differs in
					// key order and quoting from the user's apply-time
					// input. drift: computed during Read using the
					// stub manifest the importer rebuilt from the
					// live object, so it diverges at the verify step.
					// The next plan after import converges using the
					// user's yaml_body.
					"yaml_body",
					"yaml_body_parsed",
					"drift",
				},
			},
		},
	})
}

// TestAccKubectl_ImportMalformedID verifies the importer surfaces a
// clear diagnostic when the ID has the wrong shape, rather than
// stack-trace-style failure deeper in the dynamic client.
func TestAccKubectl_ImportMalformedID(t *testing.T) {
	t.Parallel()

	name := acctest.RandomWithPrefix("imp-bad")
	config := fmt.Sprintf(`
resource "kubectl_manifest" "bad" {
  yaml_body = <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: %s
EOF
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckkubectlDestroy,
		Steps: []resource.TestStep{
			{Config: config},
			{
				ResourceName:  "kubectl_manifest.bad",
				ImportState:   true,
				ImportStateId: "this-is-not-a-valid-id",
				ExpectError:   regexp.MustCompile(`malformed ID`),
			},
		},
	})
}
