package kubernetes

import (
	"testing"

	"github.com/alekc/terraform-provider-kubectl/yaml"
)

// mustParse turns a YAML manifest into the same map the client engine
// compares, so number types match what users get from yaml_body.
func mustParse(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	m, err := yaml.ParseYAML(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m.Raw.Object
}

func TestRenderDrift_NullMapInDesired(t *testing.T) {
	t.Parallel()
	desired := mustParse(t, `
apiVersion: v1
kind: Service
metadata:
  name: hubble-relay
  namespace: kube-system
  annotations:
  labels:
    app: relay
`)
	cases := []struct {
		name string
		live map[string]interface{}
	}{
		{"only-last-applied", map[string]interface{}{
			"kubectl.kubernetes.io/last-applied-configuration": "{}",
		}},
		{"controller-injected", map[string]interface{}{
			"kubectl.kubernetes.io/last-applied-configuration": "{}",
			"deployment.kubernetes.io/revision":                "3",
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			live := deepClone(desired)
			live["metadata"].(map[string]interface{})["annotations"] = c.live
			if got := RenderDrift(desired, live, DriftOptions{}); got != "" {
				t.Fatalf("null annotations should not drift, got %q", got)
			}
		})
	}
}

func TestRenderDrift_NullInDesired_ListAndScalar(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{
		"spec": map[string]interface{}{"ports": nil, "replicas": nil},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"ports":    []interface{}{map[string]interface{}{"port": int64(80)}},
			"replicas": int64(2),
		},
	}
	got := RenderDrift(desired, live, DriftOptions{})
	want := "spec:\n  replicas: <drift>\n"
	if got != want {
		t.Fatalf("null list is absent, null scalar is drift: got %q want %q", got, want)
	}
}

func TestRenderDrift_NullInsideSliceElement(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []interface{}{map[string]interface{}{"name": "a", "env": nil}},
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []interface{}{map[string]interface{}{
				"name": "a",
				"env":  []interface{}{map[string]interface{}{"name": "X", "value": "1"}},
			}},
		},
	}
	if got := RenderDrift(desired, live, DriftOptions{}); got != "" {
		t.Fatalf("null list inside a slice element should not drift, got %q", got)
	}
}

func TestRenderDrift_QuantityFromYAML(t *testing.T) {
	t.Parallel()
	desired := mustParse(t, `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: cilium
spec:
  template:
    spec:
      initContainers:
      - name: install-cni
        resources:
          limits:
            cpu: 1
            memory: 1Gi
          requests:
            cpu: 0.5
            memory: "1024Mi"
`)
	live := mustParse(t, `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: cilium
spec:
  template:
    spec:
      initContainers:
      - name: install-cni
        resources:
          limits:
            cpu: "1"
            memory: 1Gi
          requests:
            cpu: 500m
            memory: 1Gi
`)
	if got := RenderDrift(desired, live, DriftOptions{}); got != "" {
		t.Fatalf("canonicalised quantities should not drift, got %q", got)
	}
}

func TestQuantityEqual(t *testing.T) {
	t.Parallel()
	spec := []string{"spec", "resources", "limits", "cpu"}
	cases := []struct {
		name          string
		desired, live interface{}
		path          []string
		want          bool
	}{
		{"int64-vs-string", int64(1), "1", spec, true},
		{"int-vs-string", 2, "2", spec, true},
		{"float-vs-milli", 0.5, "500m", spec, true},
		{"float32-vs-milli", float32(0.25), "250m", spec, true},
		{"string-vs-milli", "0.5", "500m", spec, true},
		{"binary-suffix", "1024Mi", "1Gi", spec, true},
		{"trimmed", " 0.5 ", "500m", spec, true},
		{"different-value", int64(2), "1", spec, false},
		{"not-canonical-live", "500m", "0.5", spec, false},
		{"desired-not-quantity", "abc", "def", spec, false},
		{"live-not-string", int64(1), int64(2), spec, false},
		{"desired-bool", true, "1", spec, false},
		{"metadata-excluded", "1.0", "1", []string{"metadata", "labels", "v"}, false},
		{"configmap-data-excluded", "1.0", "1", []string{"data", "v"}, false},
		{"stringdata-excluded", "1.0", "1", []string{"stringData", "v"}, false},
		{"binarydata-excluded", "1.0", "1", []string{"binaryData", "v"}, false},
		{"empty-path", int64(1), "1", nil, false},
		{"string-outside-quantity-keys", "0.5", "500m", []string{"spec", "containers", "[0]", "env", "[0]", "value"}, false},
		{"string-crd-version", "1.0", "1", []string{"spec", "version"}, false},
		{"number-outside-quantity-keys", int64(1), "1", []string{"spec", "minAllowed", "cpu"}, true},
		{"string-pvc-storage", "1024Mi", "1Gi", []string{"spec", "resources", "requests", "storage"}, true},
		{"string-quota-hard", "0.5", "500m", []string{"spec", "hard", "cpu"}, true},
		{"string-emptydir-sizelimit", "1024Mi", "1Gi", []string{"spec", "volumes", "[0]", "emptyDir", "sizeLimit"}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := quantityEqual(c.desired, c.live, c.path); got != c.want {
				t.Errorf("quantityEqual(%v, %v, %v) = %v, want %v", c.desired, c.live, c.path, got, c.want)
			}
		})
	}
}
