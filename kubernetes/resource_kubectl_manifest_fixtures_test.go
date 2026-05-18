package kubernetes

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/acctest"
)

// Fixture helpers for resource_kubectl_manifest_test.go. Each helper returns a
// `kubectl_manifest` HCL config along with the generated random resource name
// so that:
//
//   - tests can opt into `t.Parallel()` without colliding on Kubernetes object
//     names (default-namespace tests previously all hardcoded `nginx` /
//     `nginx-deployment` and raced under parallelism);
//   - the noisy YAML boilerplate is in one place instead of duplicated across
//     ~20 tests.
//
// `extraArgs` is splatted into the `kubectl_manifest` resource block above the
// `yaml_body` heredoc — use it to set `wait`, `wait_for_rollout`,
// `delete_cascade`, `wait_for { ... }`, etc.

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
// httpGet readiness probe. Caller can override replicas via extraArgs or by
// using nginxDeploymentConfigReplicas.
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
