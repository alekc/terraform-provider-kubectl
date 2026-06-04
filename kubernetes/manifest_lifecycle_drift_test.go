package kubernetes

import (
	"strings"
	"testing"

	"github.com/alekc/terraform-provider-kubectl/yaml"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestComputeDriftYAML_InSync exercises the lifecycle wrapper that
// RenderDrift sits behind. Confirms an identical desired/live pair
// (post Secret-stringData normalisation) returns the empty string.
func TestComputeDriftYAML_InSync(t *testing.T) {
	desired := mustManifest(t, map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "x",
		},
		"data": map[string]interface{}{
			"key": "value",
		},
	})
	live := mustManifest(t, map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":            "x",
			"resourceVersion": "1234", // server-added; not user input
		},
		"data": map[string]interface{}{
			"key": "value",
		},
	})
	got := computeDriftClient(desired, live, nil, nil, ShowNone)
	if got != "" {
		t.Fatalf("expected in-sync, got %q", got)
	}
}

// TestComputeDriftYAML_SecretStringDataNormalisation ensures that a
// Secret with stringData in desired and base64 data in live (the normal
// shape post-apply) is recognised as in-sync.
func TestComputeDriftYAML_SecretStringDataNormalisation(t *testing.T) {
	desired := mustManifest(t, map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "creds"},
		"stringData": map[string]interface{}{
			"password": "hunter2",
		},
	})
	// "hunter2" base64 == "aHVudGVyMg=="
	live := mustManifest(t, map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "creds"},
		"data": map[string]interface{}{
			"password": "aHVudGVyMg==",
		},
	})
	got := computeDriftClient(desired, live, nil, nil, ShowNone)
	if got != "" {
		t.Fatalf("Secret stringData -> data must normalise; got drift %q", got)
	}
}

// TestComputeDriftYAML_SecretAutoMasks confirms that even when the
// caller asks for ShowFull, a Secret's data leaves never leak.
func TestComputeDriftYAML_SecretAutoMasks(t *testing.T) {
	desired := mustManifest(t, map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "creds"},
		"data": map[string]interface{}{
			"password": "aGVsbG8=",
		},
	})
	live := mustManifest(t, map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "creds"},
		"data": map[string]interface{}{
			"password": "Z29vZGJ5ZQ==",
		},
	})
	got := computeDriftClient(desired, live, nil, nil, ShowFull)
	if !strings.Contains(got, "<drift sensitive>") {
		t.Fatalf("expected sensitive marker for Secret data, got %q", got)
	}
	if strings.Contains(got, "aGVsbG8=") || strings.Contains(got, "Z29vZGJ5ZQ==") {
		t.Fatalf("Secret data leaked into drift: %q", got)
	}
}

// TestComputeDriftYAML_DoesNotMutateDesired guards against a regression
// of the #269 class of bugs: GetLiveManifestFields used to mutate the
// caller's manifest while applying the stringData normalisation.
// computeDriftClient must always work on a deep copy.
func TestComputeDriftYAML_DoesNotMutateDesired(t *testing.T) {
	original := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "creds"},
		"stringData": map[string]interface{}{
			"password": "hunter2",
		},
	}
	desired := mustManifest(t, original)
	live := mustManifest(t, map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "creds"},
		"data": map[string]interface{}{
			"password": "aHVudGVyMg==",
		},
	})
	_ = computeDriftClient(desired, live, nil, nil, ShowNone)
	if _, hasStringData := desired.Raw.Object["stringData"]; !hasStringData {
		t.Fatalf("computeDriftClient stripped stringData from caller's manifest")
	}
	if _, hasData := desired.Raw.Object["data"]; hasData {
		t.Fatalf("computeDriftClient added data to caller's manifest")
	}
}

// mustManifest converts a literal map into a *yaml.Manifest for testing.
// Skips through the yaml round-trip the production code goes through.
func mustManifest(t *testing.T, obj map[string]interface{}) *yaml.Manifest {
	t.Helper()
	u := &meta_v1_unstruct.Unstructured{Object: obj}
	return yaml.NewFromUnstructured(u)
}
