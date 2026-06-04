package kubernetes

import (
	"strings"
	"testing"
)

func TestRenderDrift_InSync(t *testing.T) {
	desired := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      "x",
			"namespace": "default",
		},
		"data": map[string]interface{}{
			"key": "value",
		},
	}
	live := deepClone(desired)
	got := RenderDrift(desired, live, DriftOptions{})
	if got != "" {
		t.Fatalf("expected empty drift for identical manifests, got %q", got)
	}
}

func TestRenderDrift_LeafChange_None(t *testing.T) {
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"foo": "bar",
				"baz": "qux",
			},
		},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"foo": "BAR",
				"baz": "qux",
			},
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	want := "metadata:\n  annotations:\n    foo: <drift>\n"
	if got != want {
		t.Fatalf("ShowNone: got %q want %q", got, want)
	}
}

func TestRenderDrift_LeafChange_Hash(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 2,
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 3,
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowHash})
	if !strings.Contains(got, "replicas:") {
		t.Fatalf("expected path in output, got %q", got)
	}
	if !strings.Contains(got, "<was:") || !strings.Contains(got, "now:") {
		t.Fatalf("expected hash markers, got %q", got)
	}
	if strings.Contains(got, "2") && strings.Contains(got, "3") && !strings.Contains(got, "was:") {
		t.Fatalf("hash mode should NOT leak literal values, got %q", got)
	}
}

func TestRenderDrift_LeafChange_Full(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 2,
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 3,
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowFull})
	if !strings.Contains(got, "<was: 2, now: 3>") {
		t.Fatalf("Full mode: expected was/now values, got %q", got)
	}
}

func TestRenderDrift_MissingInLive_NotReported(t *testing.T) {
	// User wrote a field that live doesn't have. Common cases: the
	// apiserver strips fields the kind doesn't accept (e.g.
	// metadata.namespace on cluster-scoped resources, injected by
	// override_namespace). Reporting these as drift triggers
	// infinite update loops because apply doesn't change live.
	// Match v2's silent-skip behavior; surface via TRACE log only.
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				"app": "foo",
			},
		},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	if got != "" {
		t.Fatalf("missing-in-live should not surface as drift, got %q", got)
	}
}

// TestRenderDrift_OverrideNamespaceOnClusterScoped is a regression for
// the v3 false-drift caused by override_namespace injecting
// metadata.namespace into a ClusterRole / CRD / other cluster-scoped
// kind. The apiserver strips it, so live lacks the field; reporting
// drift triggered post-apply non-empty-plan failures in the
// TestAccKubectlSetNamespace_nonnamespaced_resource acc test.
func TestRenderDrift_OverrideNamespaceOnClusterScoped(t *testing.T) {
	desired := map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata": map[string]interface{}{
			"name":      "x",
			"namespace": "dev", // injected by override_namespace
		},
	}
	live := map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata": map[string]interface{}{
			"name": "x",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	if got != "" {
		t.Fatalf("override_namespace on cluster-scoped resource should not drift, got %q", got)
	}
}

func TestRenderDrift_ExtraInLive_NotReported(t *testing.T) {
	// Server-side controllers add fields. Those are not drift.
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 2,
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 2,
		},
		"status": map[string]interface{}{
			"phase": "Running",
		},
		"metadata": map[string]interface{}{
			"resourceVersion": "1234",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{})
	if got != "" {
		t.Fatalf("server-added fields are not drift, got %q", got)
	}
}

func TestRenderDrift_IgnoreFields_Exact(t *testing.T) {
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"foo": "bar",
			},
		},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"foo": "BAR",
			},
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		IgnoreFields: []string{"metadata.annotations.foo"},
	})
	if got != "" {
		t.Fatalf("ignored field should not appear in drift, got %q", got)
	}
}

func TestRenderDrift_IgnoreFields_Prefix(t *testing.T) {
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"foo": "bar",
				"baz": "qux",
			},
		},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"foo": "BAR",
				"baz": "QUX",
			},
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		IgnoreFields: []string{"metadata.annotations"},
	})
	if got != "" {
		t.Fatalf("prefix ignore should suppress all children, got %q", got)
	}
}

func TestRenderDrift_KubernetesControlFields_AutoIgnored(t *testing.T) {
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{
			"finalizers": []interface{}{"a"},
			"name":       "x",
		},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{
			"finalizers": []interface{}{"b"},
			"name":       "x",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{})
	if got != "" {
		t.Fatalf("metadata.finalizers is a control field, got %q", got)
	}
}

func TestRenderDrift_Secret_AutoMasksData(t *testing.T) {
	desired := map[string]interface{}{
		"data": map[string]interface{}{
			"password": "c2VjcmV0",
		},
	}
	live := map[string]interface{}{
		"data": map[string]interface{}{
			"password": "QU5PVEhFUg==",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		Kind:       "Secret",
		APIVersion: "v1",
		ShowMode:   ShowFull, // Secret masking overrides Full mode
	})
	if !strings.Contains(got, "<drift sensitive>") {
		t.Fatalf("Secret data must be auto-masked, got %q", got)
	}
	if strings.Contains(got, "c2VjcmV0") || strings.Contains(got, "QU5PVEhFUg") {
		t.Fatalf("Secret values leaked: %q", got)
	}
}

func TestRenderDrift_NonSecretKind_DoesNotMaskData(t *testing.T) {
	// `data` is not magic on a ConfigMap. Should render normally.
	desired := map[string]interface{}{
		"data": map[string]interface{}{
			"key": "v1",
		},
	}
	live := map[string]interface{}{
		"data": map[string]interface{}{
			"key": "v2",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		Kind:       "ConfigMap",
		APIVersion: "v1",
		ShowMode:   ShowFull,
	})
	if !strings.Contains(got, "<was: \"v1\", now: \"v2\">") {
		t.Fatalf("expected full value rendering on non-Secret kind, got %q", got)
	}
}

func TestRenderDrift_MaskPaths_LiteralEntryMasksDescendants(t *testing.T) {
	// A literal mask_paths entry must hide every leaf under that
	// subtree, not just an exact-segment match. Users writing
	// mask_paths = ["data"] reasonably expect data.password to be
	// masked too.
	desired := map[string]interface{}{
		"data": map[string]interface{}{
			"password": "old-secret",
			"username": "admin",
		},
	}
	live := map[string]interface{}{
		"data": map[string]interface{}{
			"password": "new-secret",
			"username": "root",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		MaskPaths: []string{"data"},
		ShowMode:  ShowFull,
	})
	if strings.Contains(got, "old-secret") || strings.Contains(got, "new-secret") {
		t.Fatalf("password value leaked under data.* mask: %q", got)
	}
	if strings.Contains(got, "admin") || strings.Contains(got, "root") {
		t.Fatalf("username value leaked under data.* mask: %q", got)
	}
}

func TestRenderDrift_MaskPaths_ExplicitDoubleStarNotDoubled(t *testing.T) {
	// User already wrote `**`; don't auto-append another. Result
	// should still mask descendants but not duplicate the pattern.
	desired := map[string]interface{}{
		"data": map[string]interface{}{
			"k": "old",
		},
	}
	live := map[string]interface{}{
		"data": map[string]interface{}{
			"k": "new",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		MaskPaths: []string{"data.**"},
		ShowMode:  ShowFull,
	})
	if strings.Contains(got, "old") || strings.Contains(got, "new") {
		t.Fatalf("value leaked despite data.** mask: %q", got)
	}
}

func TestRenderDrift_MaskPaths_Exact(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"password": "a",
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"password": "b",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		MaskPaths: []string{"spec.password"},
		ShowMode:  ShowFull,
	})
	if !strings.Contains(got, "<drift sensitive>") {
		t.Fatalf("mask_paths exact match should mask, got %q", got)
	}
}

func TestRenderDrift_MaskPaths_DoubleGlob(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"env": []interface{}{
								map[string]interface{}{
									"name":  "PASSWORD",
									"value": "old",
								},
							},
						},
					},
				},
			},
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"env": []interface{}{
								map[string]interface{}{
									"name":  "PASSWORD",
									"value": "new",
								},
							},
						},
					},
				},
			},
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		MaskPaths: []string{"**.value"},
		ShowMode:  ShowFull,
	})
	if strings.Contains(got, "old") || strings.Contains(got, "new") {
		t.Fatalf("** glob should mask the leaf, got %q", got)
	}
	if !strings.Contains(got, "<drift sensitive>") {
		t.Fatalf("expected sensitive marker, got %q", got)
	}
}

func TestRenderDrift_ArrayLeafChange(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{
					"name":  "app",
					"image": "foo:v1",
				},
				map[string]interface{}{
					"name":  "sidecar",
					"image": "bar:v1",
				},
			},
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{
					"name":  "app",
					"image": "foo:v1",
				},
				map[string]interface{}{
					"name":  "sidecar",
					"image": "bar:v2",
				},
			},
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	if !strings.Contains(got, "_index_: 1") {
		t.Fatalf("expected index marker for drifted item, got %q", got)
	}
	if !strings.Contains(got, "image:") {
		t.Fatalf("expected image path in drift output, got %q", got)
	}
}

func TestRenderDrift_ArrayLengthMismatch_DesiredLonger(t *testing.T) {
	// User wrote 3 items, live has 2. Treated as non-drift for the
	// same reason as the missing-map-key case: the apiserver may
	// have trimmed the list via strategic merge or list-type
	// semantics, and reporting drift would trigger infinite
	// updates. Surface via TRACE log only.
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"items": []interface{}{
				"a",
				"b",
				"c",
			},
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"items": []interface{}{
				"a",
				"b",
			},
		},
	}
	got := RenderDrift(desired, live, DriftOptions{})
	if got != "" {
		t.Fatalf("desired-longer-than-live should not drift, got %q", got)
	}
}

func TestRenderDrift_StringTrimming(t *testing.T) {
	desired := map[string]interface{}{
		"data": map[string]interface{}{
			"key": "value",
		},
	}
	live := map[string]interface{}{
		"data": map[string]interface{}{
			"key": "value\n",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{
		Kind:       "ConfigMap",
		APIVersion: "v1",
	})
	if got != "" {
		t.Fatalf("trailing whitespace should not register as drift, got %q", got)
	}
}

func TestRenderDrift_TypeMismatch(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"x": "string",
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"x": 42,
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	if !strings.Contains(got, "x:") {
		t.Fatalf("expected the drifted key in output, got %q", got)
	}
}

func TestRenderDrift_NestedMapWithMultipleDrifts(t *testing.T) {
	desired := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"a": "1",
				"b": "2",
				"c": "3",
			},
		},
		"spec": map[string]interface{}{
			"replicas": 2,
		},
	}
	live := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"a": "X",
				"b": "2",
				"c": "Y",
			},
		},
		"spec": map[string]interface{}{
			"replicas": 5,
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	// Each drifted leaf should appear; unchanged "b" should not.
	if !strings.Contains(got, "a: <drift>") {
		t.Fatalf("missing drift on a: %q", got)
	}
	if !strings.Contains(got, "c: <drift>") {
		t.Fatalf("missing drift on c: %q", got)
	}
	if !strings.Contains(got, "replicas: <drift>") {
		t.Fatalf("missing drift on replicas: %q", got)
	}
	if strings.Contains(got, "b: <drift>") {
		t.Fatalf("unchanged 'b' rendered as drift: %q", got)
	}
}

func TestRenderDrift_DeterministicOrdering(t *testing.T) {
	desired := map[string]interface{}{
		"z": "1",
		"a": "1",
		"m": "1",
	}
	live := map[string]interface{}{
		"z": "X",
		"a": "X",
		"m": "X",
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	// Map keys should be sorted (sigs.k8s.io/yaml respects map ordering via
	// json marshalling and sorts keys, so a/m/z is the natural order).
	idxA := strings.Index(got, "a:")
	idxM := strings.Index(got, "m:")
	idxZ := strings.Index(got, "z:")
	if !(idxA < idxM && idxM < idxZ) {
		t.Fatalf("expected sorted key order a, m, z; got %q", got)
	}
}

// TestRenderDrift_LeafTypeMismatch covers the dispatch branch in
// collectAnyDrift where desired is a map and live is a scalar (or
// vice versa). The renderer should emit the path with the
// appropriate leaf marker rather than recurse into nothing.
func TestRenderDrift_LeafTypeMismatch_DesiredMapLiveScalar(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"x": map[string]interface{}{"inner": "value"},
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"x": 42, // type mismatch
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowFull})
	if !strings.Contains(got, "x:") {
		t.Fatalf("expected drifted key in output, got %q", got)
	}
	if !strings.Contains(got, "<was:") {
		t.Fatalf("expected was/now markers for type-mismatched leaf, got %q", got)
	}
}

func TestRenderDrift_LeafTypeMismatch_DesiredSliceLiveScalar(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"items": []interface{}{"a", "b"},
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"items": "not-a-list",
		},
	}
	got := RenderDrift(desired, live, DriftOptions{ShowMode: ShowNone})
	if !strings.Contains(got, "items:") {
		t.Fatalf("expected drifted key in output, got %q", got)
	}
}

// TestRenderDrift_LeafEqual_NilCases exercises the explicit nil
// branches in leafEqual that the higher-level RenderDrift tests
// don't reach naturally.
func TestRenderDrift_LeafEqual_NilCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		desired, live  interface{}
		wantEqual      bool
	}{
		{"both-nil", nil, nil, true},
		{"desired-nil-live-string", nil, "x", false},
		{"desired-string-live-nil", "x", nil, false},
		{"trim-whitespace-equal", "foo\n", " foo ", true},
		{"different-strings", "foo", "bar", false},
		{"int-vs-float-not-coerced", 1, 1.0, false},
		{"matching-ints", 42, 42, true},
		{"int-vs-string", 1, "1", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := leafEqual(c.desired, c.live)
			if got != c.wantEqual {
				t.Errorf("leafEqual(%v, %v) = %v, want %v", c.desired, c.live, got, c.wantEqual)
			}
		})
	}
}

// TestRenderDrift_ShortHash_Nil exercises the explicit nil branch in
// shortHash that the higher-level Hash-mode test doesn't reach.
func TestRenderDrift_ShortHash_Nil(t *testing.T) {
	if got := shortHash(nil); got != "missing" {
		t.Errorf("shortHash(nil): got %q want %q", got, "missing")
	}
	if got := shortHash("foo"); len(got) != 8 {
		t.Errorf("shortHash(string): expected 8-char prefix, got %q", got)
	}
	if a, b := shortHash(1), shortHash(2); a == b {
		t.Errorf("shortHash should differ across values: %q == %q", a, b)
	}
}

// TestRenderDrift_FormatValue_Variants covers the various Go types
// formatValue handles for ShowFull rendering.
func TestRenderDrift_FormatValue_Variants(t *testing.T) {
	cases := []struct {
		in       interface{}
		contains string
	}{
		{"hello", `"hello"`},
		{42, "42"},
		{true, "true"},
		{3.14, "3.14"},
		{nil, missingMarker},
	}
	for _, c := range cases {
		got := formatValue(c.in)
		if !strings.Contains(got, c.contains) {
			t.Errorf("formatValue(%v): expected to contain %q, got %q", c.in, c.contains, got)
		}
	}
}

// TestRenderDrift_MaskUnderArrayIndex confirms the mask globs apply
// to leaves inside array elements. The common shape is
// `spec.template.spec.containers.*.env.*.value`: env entries in
// each container with potentially sensitive values.
func TestRenderDrift_MaskUnderArrayIndex(t *testing.T) {
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{
					"name": "app",
					"env": []interface{}{
						map[string]interface{}{
							"name":  "DB_PASSWORD",
							"value": "before",
						},
					},
				},
			},
		},
	}
	live := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{
					"name": "app",
					"env": []interface{}{
						map[string]interface{}{
							"name":  "DB_PASSWORD",
							"value": "after",
						},
					},
				},
			},
		},
	}
	// Array indices in the path render as "[N]"; the glob must
	// match the literal segment "[0]" or use `*` to skip it.
	got := RenderDrift(desired, live, DriftOptions{
		MaskPaths: []string{"spec.containers.*.env.*.value"},
		ShowMode:  ShowFull,
	})
	if strings.Contains(got, "before") || strings.Contains(got, "after") {
		t.Fatalf("env value leaked under array glob mask: %q", got)
	}
	if !strings.Contains(got, "<drift sensitive>") {
		t.Fatalf("expected sensitive marker on env.*.value, got %q", got)
	}
}

func TestRenderDrift_IgnoreSetMatches_EmptyPath(t *testing.T) {
	// Defensive: empty path should never match an ignore prefix.
	// Otherwise a top-level RenderDrift call with `IgnoreFields =
	// ["something"]` could short-circuit before walking children.
	set := buildIgnoreSet([]string{"spec", "metadata"})
	if set.matches(nil) {
		t.Errorf("ignoreSet.matches(nil) should be false")
	}
	if set.matches([]string{}) {
		t.Errorf("ignoreSet.matches(empty slice) should be false")
	}
}

func TestRenderDrift_EmptyInputs(t *testing.T) {
	got := RenderDrift(nil, nil, DriftOptions{})
	if got != "" {
		t.Fatalf("nil-nil should be in-sync, got %q", got)
	}
	got = RenderDrift(map[string]interface{}{}, map[string]interface{}{}, DriftOptions{})
	if got != "" {
		t.Fatalf("empty-empty should be in-sync, got %q", got)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern []string
		path    []string
		want    bool
	}{
		{[]string{"data"}, []string{"data"}, true},
		{[]string{"data"}, []string{"data", "x"}, false},
		{[]string{"data", "*"}, []string{"data", "x"}, true},
		{[]string{"data", "*"}, []string{"data", "x", "y"}, false},
		{[]string{"data", "**"}, []string{"data", "x", "y"}, true},
		{[]string{"data", "**"}, []string{"data"}, true},
		{[]string{"**", "password"}, []string{"spec", "x", "password"}, true},
		{[]string{"**", "password"}, []string{"password"}, true},
		{[]string{"**", "password"}, []string{"spec", "password", "x"}, false},
		{[]string{"a", "*", "c"}, []string{"a", "b", "c"}, true},
		{[]string{"a", "*", "c"}, []string{"a", "c"}, false},
	}
	for _, c := range cases {
		got := globMatch(c.pattern, c.path)
		if got != c.want {
			t.Errorf("globMatch(%v, %v) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// deepClone returns a deep copy of an unstructured-style map, used by tests
// that need to mutate the live side without disturbing desired.
func deepClone(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = deepCloneAny(v)
	}
	return out
}

func deepCloneAny(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		return deepClone(x)
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, e := range x {
			out[i] = deepCloneAny(e)
		}
		return out
	default:
		return v
	}
}
