package kubernetes

import (
	"encoding/json"
	"reflect"
	"testing"
)

// mustUnmarshal turns a JSON string into the same nested
// interface{} shape the production code receives from
// json.Unmarshal of a Kubernetes object. Tests use it so the
// document under test matches real call sites byte-for-byte rather
// than relying on hand-built map literals which can hide type
// mismatches (e.g. map[string]interface{} vs map[string]string).
func mustUnmarshal(t *testing.T, raw string) interface{} {
	t.Helper()
	var doc interface{}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid JSON fixture: %v", err)
	}
	return doc
}

func TestExtractByPath_BackwardCompat(t *testing.T) {
	t.Parallel()
	doc := mustUnmarshal(t, `{
		"metadata": {
			"name": "demo",
			"namespace": "default"
		},
		"spec": {
			"replicas": 3,
			"containers": [
				{"name": "app", "image": "foo:v1"},
				{"name": "sidecar", "image": "bar:v1"}
			]
		}
	}`)

	cases := []struct {
		name  string
		path  string
		want  interface{}
		found bool
	}{
		{"map-scalar", "metadata.name", "demo", true},
		{"nested-map", "spec.replicas", float64(3), true},
		{"bare-int-slice-index", "spec.containers.0.image", "foo:v1", true},
		{"bracketed-int-with-dot", "spec.containers.[0].image", "foo:v1", true},
		{"bracketed-int-without-dot", "spec.containers[1].image", "bar:v1", true},
		{"missing-key", "metadata.gone", nil, false},
		{"missing-nested", "spec.template.spec", nil, false},
		{"oob-index", "spec.containers.99.image", nil, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, found, err := ExtractByPath(doc, c.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found != c.found {
				t.Errorf("found: got %v want %v", found, c.found)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("value: got %v want %v", got, c.want)
			}
		})
	}
}

func TestExtractByPath_DottedKeysViaBrackets(t *testing.T) {
	t.Parallel()
	// Realistic Kubernetes labels and annotations.
	doc := mustUnmarshal(t, `{
		"metadata": {
			"labels": {
				"app.kubernetes.io/name": "frontend",
				"app.kubernetes.io/version": "1.2.3",
				"team": "platform"
			},
			"annotations": {
				"argocd.argoproj.io/sync-wave": "5",
				"nginx.ingress.kubernetes.io/rewrite-target": "/"
			}
		}
	}`)

	cases := []struct {
		name string
		path string
		want interface{}
	}{
		{
			name: "double-quoted-domain-label",
			path: `metadata.labels["app.kubernetes.io/name"]`,
			want: "frontend",
		},
		{
			name: "single-quoted-domain-label",
			path: `metadata.labels['app.kubernetes.io/version']`,
			want: "1.2.3",
		},
		{
			name: "annotation-with-domain-prefix",
			path: `metadata.annotations["argocd.argoproj.io/sync-wave"]`,
			want: "5",
		},
		{
			name: "annotation-deep-domain",
			path: `metadata.annotations["nginx.ingress.kubernetes.io/rewrite-target"]`,
			want: "/",
		},
		{
			name: "non-dotted-key-via-bracket-also-works",
			path: `metadata.labels["team"]`,
			want: "platform",
		},
		{
			name: "non-dotted-key-via-dot-still-works",
			path: `metadata.labels.team`,
			want: "platform",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, found, err := ExtractByPath(doc, c.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !found {
				t.Fatalf("expected found=true for path %q, got false", c.path)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("path %q: got %v want %v", c.path, got, c.want)
			}
		})
	}
}

func TestExtractByPath_ExplicitNullVsMissing(t *testing.T) {
	t.Parallel()
	doc := mustUnmarshal(t, `{
		"status": {
			"lastScheduleTime": null,
			"phase": "Ready"
		}
	}`)

	t.Run("explicit-null-is-found", func(t *testing.T) {
		t.Parallel()
		v, found, err := ExtractByPath(doc, "status.lastScheduleTime")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !found {
			t.Errorf("explicit null should be found=true, was false")
		}
		if v != nil {
			t.Errorf("explicit null value: got %v want nil", v)
		}
	})

	t.Run("missing-is-not-found", func(t *testing.T) {
		t.Parallel()
		_, found, err := ExtractByPath(doc, "status.startTime")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found {
			t.Errorf("missing key should be found=false, was true")
		}
	})

	t.Run("descending-through-null-is-not-found", func(t *testing.T) {
		t.Parallel()
		_, found, err := ExtractByPath(doc, "status.lastScheduleTime.year")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found {
			t.Errorf("descending past null should be found=false, was true")
		}
	})
}

func TestExtractByPath_MalformedPaths(t *testing.T) {
	t.Parallel()
	doc := mustUnmarshal(t, `{"x": 1, "spec": {"replicas": 3}}`)
	cases := []string{
		"",
		".x",
		"x.",
		"x..y",
		"x[",
		"x[]",
		`x[""]`,
		`x['']`,
		"x['unterminated",
		`x["unterminated`,
		"x]",
		"x[abc",
		// Bracket segments must be followed by `.`, `[`, or
		// end-of-path. Anything else (bare identifier touching
		// the close-bracket) would otherwise parse as if a `.`
		// were silently present, masking a typo.
		"x[0]y",
		`x["k"]y`,
		`x['k']more`,
	}
	for _, p := range cases {
		p := p
		t.Run("path="+p, func(t *testing.T) {
			t.Parallel()
			_, _, err := ExtractByPath(doc, p)
			if err == nil {
				t.Errorf("expected error for malformed path %q, got nil", p)
			}
		})
	}
}

// TestExtractByPath_BracketedIndexAgainstMap regresses the
// semantic that `[N]` (bracketed integer) is for slices only.
// Applying it to a map node returns a type-mismatch error rather
// than silently looking up the literal string key "N", which is
// almost always a user typo (e.g. assuming `metadata.annotations`
// is a list when it is in fact a map).
func TestExtractByPath_BracketedIndexAgainstMap(t *testing.T) {
	t.Parallel()
	doc := mustUnmarshal(t, `{
		"spec": {
			"replicas": 3
		},
		"metadata": {
			"annotations": {"0": "legacy-value", "app": "nginx"}
		}
	}`)
	cases := []string{
		"spec[0]",
		"metadata.annotations[0]",
	}
	for _, p := range cases {
		p := p
		t.Run("path="+p, func(t *testing.T) {
			t.Parallel()
			_, _, err := ExtractByPath(doc, p)
			if err == nil {
				t.Errorf("expected type-mismatch error for %q (bracketed index against map), got nil", p)
			}
		})
	}
}

// TestExtractByPath_BareNumericAgainstMap confirms the symmetric
// case: a dot-separated numeric segment (e.g. `containers.0`) on a
// map node IS allowed to be treated as a literal string key, since
// it could equally have been written without the dot. Only the
// explicit-bracket form `[N]` carries the "must be an index"
// commitment.
func TestExtractByPath_BareNumericAgainstMap(t *testing.T) {
	t.Parallel()
	doc := mustUnmarshal(t, `{
		"metadata": {
			"annotations": {"0": "legacy-value"}
		}
	}`)
	v, found, err := ExtractByPath(doc, "metadata.annotations.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("expected to find annotations[\"0\"], got not-found")
	}
	if v != "legacy-value" {
		t.Errorf("got %v, want legacy-value", v)
	}
}

func TestExtractByPath_TypeMismatch(t *testing.T) {
	t.Parallel()
	doc := mustUnmarshal(t, `{
		"spec": {
			"replicas": 3,
			"containers": [{"name": "app"}]
		}
	}`)

	t.Run("quoted-segment-cannot-index-slice", func(t *testing.T) {
		t.Parallel()
		_, _, err := ExtractByPath(doc, `spec.containers["first"]`)
		if err == nil {
			t.Fatal("expected error: quoted segment against slice")
		}
	})

	t.Run("non-numeric-bare-segment-against-slice", func(t *testing.T) {
		t.Parallel()
		// Bare "first" against a slice has no integer reading
		// so it must fail rather than silently coerce to 0.
		_, _, err := ExtractByPath(doc, "spec.containers.first.image")
		if err == nil {
			t.Fatal("expected error: non-integer segment against slice")
		}
	})

	t.Run("descending-into-scalar-is-not-found", func(t *testing.T) {
		t.Parallel()
		// `replicas` is a scalar; walking deeper is not-found.
		_, found, err := ExtractByPath(doc, "spec.replicas.value")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found {
			t.Errorf("descending into scalar should be not-found")
		}
	})
}

