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

// clientOpts mirrors what computeDriftClient passes to RenderDrift.
var clientOpts = DriftOptions{NullAsAbsent: true, CanonicalQuantities: true}

// clientOptsFor is clientOpts for a kind: apiVersion gates quantities, and
// apiVersion plus kind pick the patch type that decides null handling.
func clientOptsFor(apiVersion, kind string) DriftOptions {
	o := clientOpts
	o.APIVersion = apiVersion
	o.Kind = kind
	return o
}

func TestCoreQuantityGroup(t *testing.T) {
	t.Parallel()
	for apiVersion, want := range map[string]bool{
		"v1": true, "apps/v1": true, "batch/v1": true, "node.k8s.io/v1": true,
		"": false, "example.com/v1": false, "autoscaling.k8s.io/v1": false,
		"karpenter.sh/v1": false, "cilium.io/v2": false,
	} {
		if got := coreQuantityGroup(apiVersion); got != want {
			t.Errorf("coreQuantityGroup(%q) = %v, want %v", apiVersion, got, want)
		}
	}
}

// Server engine: the dry-run response keeps whatever type was applied, so a
// number vs string at a quantity path is a real difference there.
func TestRenderDrift_ServerEngine_NoQuantityMatching(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{"spec": map[string]interface{}{
		"resources": map[string]interface{}{"limits": map[string]interface{}{"cpu": int64(1)}},
	}}
	live := map[string]interface{}{"spec": map[string]interface{}{
		"resources": map[string]interface{}{"limits": map[string]interface{}{"cpu": "1"}},
	}}
	got := RenderDrift(desired, live, DriftOptions{APIVersion: "apps/v1"})
	if got == "" {
		t.Fatal("server engine must not canonicalise quantities")
	}
	if got := RenderDrift(desired, live, clientOptsFor("apps/v1", "Deployment")); got != "" {
		t.Fatalf("client engine on a core kind should match, got %q", got)
	}
}

// Custom resources keep strict comparison: int-or-string fields store the
// type that was written, and CRD maps named limits/max/hard are not quantities.
func TestRenderDrift_CRD_StrictComparison(t *testing.T) {
	t.Parallel()
	desired := mustParse(t, `
apiVersion: drifttest.kubectl.alekc.dev/v1
kind: DriftWidget
metadata:
  name: w
  annotations:
spec:
  surge: 1
  limits:
    cpu: 1
  max:
    cpu: "0.5"
  hard:
    tier: "0.5"
  sizeLimit: "1024Mi"
  tags:
`)
	live := mustParse(t, `
apiVersion: drifttest.kubectl.alekc.dev/v1
kind: DriftWidget
metadata:
  name: w
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: "{}"
spec:
  surge: "1"
  limits:
    cpu: "1"
  max:
    cpu: 500m
  hard:
    tier: 500m
  sizeLimit: 1Gi
  tags:
  - a
`)
	got := RenderDrift(desired, live, clientOptsFor("drifttest.kubectl.alekc.dev/v1", "DriftWidget"))
	want := "spec:\n  hard:\n    tier: <drift>\n  limits:\n    cpu: <drift>\n  max:\n    cpu: <drift>\n  sizeLimit: <drift>\n  surge: <drift>\n"
	if got != want {
		t.Fatalf("CRD must compare strictly, null map/list still absent:\ngot  %q\nwant %q", got, want)
	}
}

// CRD with values that already match exactly: no drift, null-as-absent still
// applies to custom resources.
func TestComputeDriftClient_CRDInSync(t *testing.T) {
	t.Parallel()
	body := "apiVersion: drifttest.kubectl.alekc.dev/v1\nkind: DriftWidget\nmetadata:\n  name: w\n  annotations:\nspec:\n  surge: 1\n  limits:\n    cpu: 1\n"
	desired, err := yaml.ParseYAML(body)
	if err != nil {
		t.Fatal(err)
	}
	live, err := yaml.ParseYAML(body)
	if err != nil {
		t.Fatal(err)
	}
	live.Raw.Object["metadata"].(map[string]interface{})["annotations"] = map[string]interface{}{
		"kubectl.kubernetes.io/last-applied-configuration": "{}",
	}
	if got := computeDriftClient(desired, live, nil, nil, ShowNone); got != "" {
		t.Fatalf("in-sync CRD should not drift, got %q", got)
	}
}

// Copilot on #402: a v1 Service selector label named sizeLimit is a plain
// string; "0.5" and "500m" select different Pods.
func TestRenderDrift_SelectorKeySizeLimitStillDrifts(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{"spec": map[string]interface{}{
		"selector": map[string]interface{}{"sizeLimit": "0.5"},
	}}
	live := map[string]interface{}{"spec": map[string]interface{}{
		"selector": map[string]interface{}{"sizeLimit": "500m"},
	}}
	want := "spec:\n  selector:\n    sizeLimit: <drift>\n"
	if got := RenderDrift(desired, live, clientOptsFor("v1", "Service")); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// End to end through the client engine: the issue's DaemonSet cpu: 1.
func TestComputeDriftClient_CoreQuantity(t *testing.T) {
	t.Parallel()
	desired, err := yaml.ParseYAML("apiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: d\nspec:\n  template:\n    spec:\n      initContainers:\n      - name: i\n        resources:\n          limits:\n            cpu: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	live, err := yaml.ParseYAML("apiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: d\nspec:\n  template:\n    spec:\n      initContainers:\n      - name: i\n        resources:\n          limits:\n            cpu: \"1\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := computeDriftClient(desired, live, nil, nil, ShowNone); got != "" {
		t.Fatalf("client engine must match cpu 1 against \"1\", got %q", got)
	}
}

// The server engine compares a dry-run response, where an explicit null on
// a nullable CRD field is a real change, so it must still drift there.
func TestRenderDrift_NullAsAbsentOff_ServerEngine(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{
		"spec": map[string]interface{}{"tags": nil, "selector": nil},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"tags":     []interface{}{"a"},
			"selector": map[string]interface{}{"app": "x"},
		},
	}
	got := RenderDrift(desired, live, DriftOptions{})
	want := "spec:\n  selector: <drift>\n  tags: <drift>\n"
	if got != want {
		t.Fatalf("server engine null vs collection: got %q want %q", got, want)
	}
}

func TestComputeDriftClient_NullAnnotations(t *testing.T) {
	t.Parallel()
	desired, err := yaml.ParseYAML("apiVersion: v1\nkind: Service\nmetadata:\n  name: s\n  annotations:\n")
	if err != nil {
		t.Fatal(err)
	}
	live, err := yaml.ParseYAML("apiVersion: v1\nkind: Service\nmetadata:\n  name: s\n  annotations:\n    kubectl.kubernetes.io/last-applied-configuration: '{}'\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := computeDriftClient(desired, live, nil, nil, ShowNone); got != "" {
		t.Fatalf("client engine must treat null annotations as absent, got %q", got)
	}
}

// Bot review on #402: canonical-looking strings outside core quantity maps
// are real edits and must still drift end to end.
func TestRenderDrift_QuantityLookalikeStringsStillDrift(t *testing.T) {
	t.Parallel()
	desired := mustParse(t, `
apiVersion: example.com/v1
kind: Widget
metadata:
  name: w
spec:
  resources:
    version: "1.0"
  max: "0.5"
  template:
    metadata:
      annotations:
        limits: "0.5"
`)
	live := mustParse(t, `
apiVersion: example.com/v1
kind: Widget
metadata:
  name: w
spec:
  resources:
    version: "1"
  max: "500m"
  template:
    metadata:
      annotations:
        limits: "500m"
`)
	// Core group, so only the path rule can keep these as drift.
	got := RenderDrift(desired, live, clientOptsFor("v1", "Pod"))
	want := "spec:\n  max: <drift>\n  resources:\n    version: <drift>\n  template:\n    metadata:\n      annotations:\n        limits: <drift>\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
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
			if got := RenderDrift(desired, live, clientOpts); got != "" {
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
	// Built-in kind: the strategic patch keeps null and deletes the live list.
	got := RenderDrift(desired, live, clientOptsFor("v1", "Service"))
	want := "spec:\n  ports: <drift>\n  replicas: <drift>\n"
	if got != want {
		t.Fatalf("built-in: got %q want %q", got, want)
	}
	// CRD: JSON merge patch makes null a no-op; a null scalar still drifts.
	got = RenderDrift(desired, live, clientOptsFor("example.com/v1", "Widget"))
	want = "spec:\n  replicas: <drift>\n"
	if got != want {
		t.Fatalf("CRD: got %q want %q", got, want)
	}
}

// A null on labels or pod template annotations of a built-in kind: apply
// would delete the live content, so it is drift, as kubectl diff shows it.
func TestRenderDrift_NullBuiltinMapsStillDrift(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{"labels": nil},
		"spec": map[string]interface{}{"template": map[string]interface{}{
			"metadata": map[string]interface{}{"annotations": nil},
		}},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{"labels": map[string]interface{}{"team": "a"}},
		"spec": map[string]interface{}{"template": map[string]interface{}{
			"metadata": map[string]interface{}{"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/restartedAt": "2026-11-02T00:00:00Z",
			}},
		}},
	}
	got := RenderDrift(desired, live, clientOptsFor("apps/v1", "Deployment"))
	want := "metadata:\n  labels: <drift>\nspec:\n  template:\n    metadata:\n      annotations: <drift>\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// The apiserver re-adds kubernetes.io/metadata.name to every Namespace, so a
// null labels map against only that label is not drift; any other label is.
func TestRenderDrift_NamespaceNullLabels(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{"metadata": map[string]interface{}{"name": "ns", "labels": nil}}
	live := map[string]interface{}{"metadata": map[string]interface{}{"name": "ns",
		"labels": map[string]interface{}{namespaceNameLabel: "ns"}}}
	if got := RenderDrift(desired, live, clientOptsFor("v1", "Namespace")); got != "" {
		t.Fatalf("auto label only should not drift, got %q", got)
	}
	live["metadata"].(map[string]interface{})["labels"].(map[string]interface{})["team"] = "a"
	if got := RenderDrift(desired, live, clientOptsFor("v1", "Namespace")); got != "metadata:\n  labels: <drift>\n" {
		t.Fatalf("extra label must drift, got %q", got)
	}
	// Only Namespace gets the exception.
	live["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{namespaceNameLabel: "ns"}
	if got := RenderDrift(desired, live, clientOptsFor("v1", "ConfigMap")); got == "" {
		t.Fatal("the name label exception is Namespace only")
	}
}

// CRDs get a JSON merge patch, where a null labels map is a no-op too.
func TestRenderDrift_CRDNullLabels(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{"metadata": map[string]interface{}{"labels": nil}}
	live := map[string]interface{}{"metadata": map[string]interface{}{
		"labels": map[string]interface{}{"team": "a"}}}
	if got := RenderDrift(desired, live, clientOptsFor("example.com/v1", "Widget")); got != "" {
		t.Fatalf("CRD null labels should not drift, got %q", got)
	}
}

// Top-level annotations null on a built-in kind with controller and manual
// annotations: apply re-adds last-applied and leaves the rest, so no drift.
func TestRenderDrift_BuiltinNullAnnotationsWithExtras(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{"metadata": map[string]interface{}{"annotations": nil}}
	live := map[string]interface{}{"metadata": map[string]interface{}{
		"annotations": map[string]interface{}{
			"kubectl.kubernetes.io/last-applied-configuration": "{}",
			"deployment.kubernetes.io/revision":                "3",
			"owner":                                            "someone",
		}}}
	if got := RenderDrift(desired, live, clientOptsFor("apps/v1", "Deployment")); got != "" {
		t.Fatalf("top-level null annotations should not drift, got %q", got)
	}
}

func TestUsesMergePatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		apiVersion, kind string
		want             bool
	}{
		{"v1", "Pod", false},
		{"v1", "Namespace", false},
		{"apps/v1", "Deployment", false},
		{"batch/v1", "CronJob", false},
		{"example.com/v1", "Widget", true},
		{"gateway.networking.k8s.io/v1", "HTTPRoute", true},
		{"cilium.io/v2", "CiliumNetworkPolicy", true},
		{"", "Pod", false},
		{"v1", "", false},
	}
	for _, c := range cases {
		if got := usesMergePatch(c.apiVersion, c.kind); got != c.want {
			t.Errorf("usesMergePatch(%q, %q) = %v, want %v", c.apiVersion, c.kind, got, c.want)
		}
	}
}

func TestRenderDrift_NullInDesired_LiveKeyAbsent(t *testing.T) {
	t.Parallel()
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "x", "annotations": nil},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "x"},
	}
	if got := RenderDrift(desired, live, clientOpts); got != "" {
		t.Fatalf("null against an absent live key should not drift, got %q", got)
	}
}

func TestComputeDriftClient_SecretNullData(t *testing.T) {
	t.Parallel()
	desired, err := yaml.ParseYAML("apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\ndata:\n")
	if err != nil {
		t.Fatal(err)
	}
	live, err := yaml.ParseYAML("apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\ndata:\n  k: dg==\n")
	if err != nil {
		t.Fatal(err)
	}
	// Strategic patch keeps data: null, so apply would wipe the Secret's data.
	if got := computeDriftClient(desired, live, nil, nil, ShowNone); got != "data: <drift sensitive>\n" {
		t.Fatalf("Secret with null data against live data must drift, got %q", got)
	}
}

// opencode cross-check on #402: null data plus stringData must keep the
// stringData comparison instead of dropping it with the null map.
func TestComputeDriftClient_SecretNullDataWithStringData(t *testing.T) {
	t.Parallel()
	desired, err := yaml.ParseYAML("apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\ndata:\nstringData:\n  foo: x\n")
	if err != nil {
		t.Fatal(err)
	}
	live, err := yaml.ParseYAML("apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\ndata:\n  foo: b2xk\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "data:\n  foo: <drift sensitive>\n"
	if got := computeDriftClient(desired, live, nil, nil, ShowNone); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	live.Raw.Object["data"] = map[string]interface{}{"foo": "eA=="}
	if got := computeDriftClient(desired, live, nil, nil, ShowNone); got != "" {
		t.Fatalf("matching stringData should not drift, got %q", got)
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
	if got := RenderDrift(desired, live, clientOptsFor("example.com/v1", "Widget")); got != "" {
		t.Fatalf("CRD: null list inside a slice element should not drift, got %q", got)
	}
	want := "spec:\n  containers:\n  - _index_: 0\n    env: <drift>\n"
	if got := RenderDrift(desired, live, clientOptsFor("v1", "Pod")); got != want {
		t.Fatalf("built-in: got %q want %q", got, want)
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
	if got := RenderDrift(desired, live, clientOptsFor("apps/v1", "DaemonSet")); got != "" {
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
		{"number-outside-quantity-keys", int64(1), "1", []string{"spec", "minAllowed", "cpu"}, false},
		{"number-int-or-string-field", int64(1), "1", []string{"spec", "surge"}, false},
		{"string-pvc-storage", "1024Mi", "1Gi", []string{"spec", "resources", "requests", "storage"}, true},
		{"string-quota-hard", "0.5", "500m", []string{"spec", "hard", "cpu"}, true},
		{"string-emptydir-sizelimit", "1024Mi", "1Gi", []string{"spec", "volumes", "[0]", "emptyDir", "sizeLimit"}, true},
		// Copilot on #402: a label key named sizeLimit is not a quantity.
		{"selector-key-sizelimit", "0.5", "500m", []string{"spec", "selector", "sizeLimit"}, false},
		{"bare-sizelimit-leaf", "1024Mi", "1Gi", []string{"spec", "sizeLimit"}, false},
		// Cases raised by CodeRabbit and Copilot on #402.
		{"string-crd-resources-version", "1.0", "1", []string{"spec", "resources", "version"}, false},
		{"string-template-annotation-limits", "0.5", "500m", []string{"spec", "template", "metadata", "annotations", "limits"}, false},
		{"number-template-annotation", int64(1), "1", []string{"spec", "template", "metadata", "annotations", "limits"}, false},
		{"string-crd-spec-max", "0.5", "500m", []string{"spec", "max"}, false},
		{"string-limits-nested-deeper", "1.0", "1", []string{"spec", "limits", "x", "version"}, false},
		{"string-limitrange-max", "0.5", "500m", []string{"spec", "limits", "[0]", "max", "cpu"}, true},
		{"string-quota-dotted-key", "0.5", "500m", []string{"spec", "hard", "requests.cpu"}, true},
		{"string-runtimeclass-podfixed", "0.25", "250m", []string{"overhead", "podFixed", "cpu"}, true},
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
