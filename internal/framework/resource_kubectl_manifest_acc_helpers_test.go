package framework_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"k8s.io/apimachinery/pkg/api/errors"
	kclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Acceptance-test helpers for the framework half of kubectl_manifest, ported
// from kubernetes/provider_test.go and kubernetes/resource_kubectl_manifest_fixtures_test.go
// as part of #61. The SDK v2 originals stay in place (data source acc tests
// still use them) until the data source itself migrates; the duplication is
// intentional for the transition window.
//
// One semantic change: the destroy / exists / status checks build a Kubernetes
// clientset directly from $KUBECONFIG rather than reaching into the configured
// SDK v2 provider's *KubeProvider. The muxed provider's configured value is
// not exposed through the terraform-plugin-testing harness, and $KUBECONFIG
// is already a hard precondition (enforced by testAccPreCheck), so the direct
// clientset is the right boundary.

// newKubeClientset constructs a *kubernetes.Clientset from $KUBECONFIG, used
// by the imperative cluster-state check helpers below. Test fails fast if the
// kubeconfig can't be loaded.
func newKubeClientset(t *testing.T) *kclient.Clientset {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatalf("BuildConfigFromFlags: %v", err)
	}
	cs, err := kclient.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("NewForConfig: %v", err)
	}
	return cs
}

// testAccCheckkubectlDestroy is the CheckDestroy hook for kubectl_manifest
// acc tests: every kubectl_manifest resource in state must be absent from the
// cluster after destroy. Mirrors the SDK v2 helper of the same name.
func testAccCheckkubectlDestroy(s *terraform.State) error {
	return testAccCheckkubectlStatus(s, false)
}

// testAccCheckkubectlExists is the post-apply check counterpart: every
// kubectl_manifest resource in state must be present in the cluster.
func testAccCheckkubectlExists(s *terraform.State) error {
	return testAccCheckkubectlStatus(s, true)
}

// testAccCheckkubectlStatus probes the cluster for each kubectl_manifest
// resource in state and reports either a missing resource that should exist
// or, when shouldExist is false, silently tolerates a still-present resource
// (matching the SDK v2 helper's lenient destroy semantics). The probe uses
// the self-link stored as the Terraform resource ID.
func testAccCheckkubectlStatus(s *terraform.State, shouldExist bool) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		return fmt.Errorf("BuildConfigFromFlags: %v", err)
	}
	cs, err := kclient.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("NewForConfig: %v", err)
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "kubectl_manifest" {
			continue
		}
		content, err := cs.RESTClient().Get().AbsPath(rs.Primary.ID).DoRaw(context.TODO())
		if (errors.IsNotFound(err) || errors.IsGone(err)) && shouldExist {
			return fmt.Errorf("failed to find resource at %s: %+v %v", rs.Primary.ID, err, string(content))
		}
	}
	return nil
}

// Fixture helpers for kubectl_manifest acc tests. Each helper returns a
// kubectl_manifest HCL config plus the generated random resource name, so:
//
//   - tests can opt into t.Parallel() without colliding on Kubernetes object
//     names (default-namespace tests previously hardcoded names and raced);
//   - the noisy YAML boilerplate lives in one place.
//
// `extraArgs` is splatted into the kubectl_manifest resource block above the
// yaml_body heredoc, use it to set wait, wait_for_rollout, delete_cascade,
// wait_for { ... }, etc.

const nginxImage = "registry.k8s.io/e2e-test-images/nginx:1.28.0-1"

// nginxPodConfig builds a single-container nginx Pod with a 10-second-delayed
// httpGet readiness probe on port 80.
func nginxPodConfig(extraArgs string) (config, name string) {
	name = acctest.RandomWithPrefix("nginx-pod")
	config = fmt.Sprintf(`
resource "kubectl_manifest" "test" {
%s
	yaml_body = <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  containers:
  - name: nginx
    image: %s
    readinessProbe:
      httpGet:
        path: "/"
        port: 80
      initialDelaySeconds: 10
YAML
}
`, extraArgs, name, nginxImage)
	return
}

// nginxDeploymentConfig builds a 1-replica nginx Deployment with a 10-second
// httpGet readiness probe. Callers wanting a different replica count should
// use nginxDeploymentConfigReplicas directly.
func nginxDeploymentConfig(extraArgs string) (config, name string) {
	return nginxDeploymentConfigReplicas(extraArgs, 1)
}

// nginxDeploymentConfigReplicas is like nginxDeploymentConfig but with a
// caller-chosen replica count.
func nginxDeploymentConfigReplicas(extraArgs string, replicas int) (config, name string) {
	name = acctest.RandomWithPrefix("nginx-deployment")
	config = fmt.Sprintf(`
resource "kubectl_manifest" "test" {
%s
	yaml_body = <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  labels:
    app: %s
spec:
  replicas: %d
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
          image: %s
          ports:
            - containerPort: 80
          readinessProbe:
            httpGet:
              path: "/"
              port: 80
            initialDelaySeconds: 10
YAML
}
`, extraArgs, name, name, replicas, name, name, nginxImage)
	return
}

// nginxDaemonSetConfig builds a single-container nginx DaemonSet with a
// RollingUpdate update strategy.
func nginxDaemonSetConfig(extraArgs string) (config, name string) {
	name = acctest.RandomWithPrefix("nginx-daemonset")
	config = fmt.Sprintf(`
resource "kubectl_manifest" "test" {
%s
	yaml_body = <<YAML
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: %s
  labels:
    app: %s
spec:
  updateStrategy:
    type: RollingUpdate
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
          image: %s
          ports:
            - containerPort: 80
          readinessProbe:
            httpGet:
              path: "/"
              port: 80
            initialDelaySeconds: 10
YAML
}
`, extraArgs, name, name, name, name, nginxImage)
	return
}

// nginxStatefulSetConfig builds a 3-replica nginx StatefulSet with a
// RollingUpdate update strategy.
func nginxStatefulSetConfig(extraArgs string) (config, name string) {
	name = acctest.RandomWithPrefix("nginx-statefulset")
	config = fmt.Sprintf(`
resource "kubectl_manifest" "test" {
%s
	yaml_body = <<YAML
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: %s
  labels:
    app: %s
spec:
  updateStrategy:
    type: RollingUpdate
  replicas: 3
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
          image: %s
          ports:
            - containerPort: 80
          readinessProbe:
            httpGet:
              path: "/"
              port: 80
            initialDelaySeconds: 10
YAML
}
`, extraArgs, name, name, name, name, nginxImage)
	return
}
