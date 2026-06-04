package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alekc/terraform-provider-kubectl/yaml"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apimachinery_types "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// computeDrift integration with the server-side engine is exercised via
// a stub dynamic.ResourceInterface so tests stay deterministic / Go-only.
// The acceptance suite covers the real apiserver path under TF_ACC=1.

type stubResource struct {
	dynamic.ResourceInterface
	patchCalled  bool
	gotPatchType apimachinery_types.PatchType
	gotOptions   meta_v1.PatchOptions
	resp         *meta_v1_unstruct.Unstructured
	respErr      error
}

func (s *stubResource) Patch(_ context.Context, _ string, pt apimachinery_types.PatchType, _ []byte, opts meta_v1.PatchOptions, _ ...string) (*meta_v1_unstruct.Unstructured, error) {
	s.patchCalled = true
	s.gotPatchType = pt
	s.gotOptions = opts
	return s.resp, s.respErr
}

func newManifest(obj map[string]interface{}) *yaml.Manifest {
	return yaml.NewFromUnstructured(&meta_v1_unstruct.Unstructured{Object: obj})
}

// TestComputeDrift_ServerEngine_UsesPatchResponse asserts that the
// server engine calls Patch with ApplyPatchType + DryRunAll and feeds
// the response into the renderer.
func TestComputeDrift_ServerEngine_UsesPatchResponse(t *testing.T) {
	t.Parallel()
	desired := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
		"data":       map[string]interface{}{"key": "DESIRED"},
	})
	live := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
		"data":       map[string]interface{}{"key": "LIVE"},
	})
	// Apiserver's view of post-apply: would change `data.key` to DESIRED.
	stub := &stubResource{
		resp: &meta_v1_unstruct.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]interface{}{"name": "x"},
			"data":       map[string]interface{}{"key": "DESIRED"},
		}},
	}
	got := computeDrift(context.Background(), stub, desired, live, nil, nil, ShowFull, ServerDriftEngine, "kubectl", false)

	if !stub.patchCalled {
		t.Fatalf("server engine did not call Patch")
	}
	if stub.gotPatchType != apimachinery_types.ApplyPatchType {
		t.Errorf("expected ApplyPatchType, got %v", stub.gotPatchType)
	}
	if len(stub.gotOptions.DryRun) != 1 || stub.gotOptions.DryRun[0] != meta_v1.DryRunAll {
		t.Errorf("expected DryRun=[All], got %v", stub.gotOptions.DryRun)
	}
	if stub.gotOptions.FieldManager != "kubectl" {
		t.Errorf("expected FieldManager=kubectl, got %q", stub.gotOptions.FieldManager)
	}
	if !strings.Contains(got, "data:") || !strings.Contains(got, "key:") {
		t.Errorf("expected drift to include the changing path, got %q", got)
	}
}

// TestComputeDrift_ServerEngine_FallsBackOnPatchError asserts that a
// Patch error (CRD without PATCH, webhook rejection, RBAC denied, etc.)
// falls back to the client engine and produces the same drift signal
// without surfacing the error to the caller.
func TestComputeDrift_ServerEngine_FallsBackOnPatchError(t *testing.T) {
	t.Parallel()
	desired := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
		"data":       map[string]interface{}{"key": "DESIRED"},
	})
	live := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
		"data":       map[string]interface{}{"key": "LIVE"},
	})
	stub := &stubResource{
		respErr: errors.New("RBAC: PATCH not permitted"),
	}
	got := computeDrift(context.Background(), stub, desired, live, nil, nil, ShowNone, ServerDriftEngine, "kubectl", false)

	if !stub.patchCalled {
		t.Fatalf("server engine should have attempted Patch before falling back")
	}
	if got == "" {
		t.Errorf("fallback to client engine should still produce drift, got empty")
	}
	if !strings.Contains(got, "key:") {
		t.Errorf("client-engine drift missing changing path: %q", got)
	}
}

// TestComputeDrift_ClientEngine_DoesNotCallPatch asserts that the
// default client engine never reaches the apiserver — no API call cost
// on Read for existing users who don't opt in.
func TestComputeDrift_ClientEngine_DoesNotCallPatch(t *testing.T) {
	t.Parallel()
	desired := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
		"data":       map[string]interface{}{"key": "DESIRED"},
	})
	live := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
		"data":       map[string]interface{}{"key": "LIVE"},
	})
	stub := &stubResource{}
	_ = computeDrift(context.Background(), stub, desired, live, nil, nil, ShowNone, ClientDriftEngine, "kubectl", false)
	if stub.patchCalled {
		t.Errorf("client engine must not call Patch")
	}
	// Also: empty engine (zero value) defaults to client semantics.
	stub2 := &stubResource{}
	_ = computeDrift(context.Background(), stub2, desired, live, nil, nil, ShowNone, "", "kubectl", false)
	if stub2.patchCalled {
		t.Errorf("empty DriftEngine must default to client (no Patch call)")
	}
}

// TestComputeDrift_ServerEngine_ForceConflictsPropagates asserts that
// the ForceConflicts flag passes through to PatchOptions.Force. The
// real apply path uses the same flag, so the dry-run must agree to
// produce an accurate "what would apply do" diff.
func TestComputeDrift_ServerEngine_ForceConflictsPropagates(t *testing.T) {
	t.Parallel()
	desired := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
	})
	live := newManifest(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "x"},
	})
	stub := &stubResource{
		resp: &meta_v1_unstruct.Unstructured{Object: desired.Raw.Object},
	}
	_ = computeDrift(context.Background(), stub, desired, live, nil, nil, ShowNone, ServerDriftEngine, "alekc-tool", true)
	if stub.gotOptions.Force == nil || !*stub.gotOptions.Force {
		t.Errorf("ForceConflicts=true should propagate to PatchOptions.Force")
	}
	if stub.gotOptions.FieldManager != "alekc-tool" {
		t.Errorf("FieldManager passthrough: got %q want %q", stub.gotOptions.FieldManager, "alekc-tool")
	}
}
