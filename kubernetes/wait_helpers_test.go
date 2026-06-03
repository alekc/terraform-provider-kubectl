package kubernetes

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apimachineryschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/alekc/terraform-provider-kubectl/internal/types"
)

// mockResourceInterface is a hand-rolled minimal stub of
// dynamic.ResourceInterface that only honours Watch and Get. The wait
// helpers never call anything else, so the rest of the interface stays
// nil and would panic if reached — surfacing a test-bug, not silently
// regressing the production code.
type mockResourceInterface struct {
	dynamic.ResourceInterface

	watchFn func(ctx context.Context, opts meta_v1.ListOptions) (watch.Interface, error)
	getFn   func(ctx context.Context, name string, opts meta_v1.GetOptions, subresources ...string) (*meta_v1_unstruct.Unstructured, error)
}

func (m *mockResourceInterface) Watch(ctx context.Context, opts meta_v1.ListOptions) (watch.Interface, error) {
	return m.watchFn(ctx, opts)
}

func (m *mockResourceInterface) Get(ctx context.Context, name string, opts meta_v1.GetOptions, subresources ...string) (*meta_v1_unstruct.Unstructured, error) {
	return m.getFn(ctx, name, opts, subresources...)
}

// notFoundError returns the apierrors-typed not-found error the wait
// helpers test for via errors.IsNotFound / errors.IsGone.
func notFoundError(name string) error {
	return apierrors.NewNotFound(apimachineryschema.GroupResource{Resource: "configmaps"}, name)
}

// TestWaitForDelete_ClosedChannelReturnsSuccessWhenAlreadyGone pins the
// fix for issue #266 on the WaitForDelete path. When the apiserver
// closes the watch and a probe Get reports the object is gone, the
// helper must return nil rather than hot-spinning on the closed
// channel or returning a misleading error.
func TestWaitForDelete_ClosedChannelReturnsSuccessWhenAlreadyGone(t *testing.T) {
	t.Parallel()
	w := watch.NewFake()
	// Close the channel immediately so the helper's receive returns
	// (event, ok=false) on the first iteration.
	w.Stop()

	mock := &mockResourceInterface{
		watchFn: func(ctx context.Context, opts meta_v1.ListOptions) (watch.Interface, error) {
			return w, nil
		},
		getFn: func(ctx context.Context, name string, opts meta_v1.GetOptions, subresources ...string) (*meta_v1_unstruct.Unstructured, error) {
			// First call (inside WaitForDelete before opening the
			// watch) returns the object so the helper enters the watch
			// loop; subsequent calls (the post-close probe) return
			// NotFound so the helper concludes the deletion succeeded.
			return nil, notFoundError("x")
		},
	}
	// Seed the first-call branch separately: the helper does a Get
	// before opening the watch to bail out early on already-gone
	// resources. Returning the object on that first call forces the
	// watch path to run.
	calls := 0
	mock.getFn = func(ctx context.Context, name string, opts meta_v1.GetOptions, subresources ...string) (*meta_v1_unstruct.Unstructured, error) {
		calls++
		if calls == 1 {
			obj := &meta_v1_unstruct.Unstructured{}
			obj.SetName(name)
			obj.SetResourceVersion("123")
			return obj, nil
		}
		return nil, notFoundError(name)
	}

	rc := RestClientResultSuccess(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := WaitForDelete(ctx, rc, "x", 60*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error (post-close probe found object gone), got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("WaitForDelete should return promptly on closed channel; took %s", elapsed)
	}
}

// TestWaitForDelete_ClosedChannelReturnsErrorWhenStillPresent pins the
// flip side: closed channel + Get still finds the object → return error
// rather than hot-spin or false-success.
func TestWaitForDelete_ClosedChannelReturnsErrorWhenStillPresent(t *testing.T) {
	t.Parallel()
	w := watch.NewFake()
	w.Stop()

	calls := 0
	mock := &mockResourceInterface{
		watchFn: func(ctx context.Context, opts meta_v1.ListOptions) (watch.Interface, error) {
			return w, nil
		},
		getFn: func(ctx context.Context, name string, opts meta_v1.GetOptions, subresources ...string) (*meta_v1_unstruct.Unstructured, error) {
			calls++
			obj := &meta_v1_unstruct.Unstructured{}
			obj.SetName(name)
			obj.SetResourceVersion("123")
			return obj, nil
		},
	}

	rc := RestClientResultSuccess(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := WaitForDelete(ctx, rc, "x", 60*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error when post-close probe finds object still present, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("WaitForDelete should fail promptly on closed channel; took %s", elapsed)
	}
}

// TestWaitForConditions_ClosedChannelReturnsErrorWhenNoMatch covers
// the WaitForConditions path through matchesWaitConditions. Closed
// channel + Get returns a manifest that does not satisfy the desired
// conditions → return error promptly.
func TestWaitForConditions_ClosedChannelReturnsErrorWhenNoMatch(t *testing.T) {
	t.Parallel()
	w := watch.NewFake()
	w.Stop()

	mock := &mockResourceInterface{
		watchFn: func(ctx context.Context, opts meta_v1.ListOptions) (watch.Interface, error) {
			return w, nil
		},
		getFn: func(ctx context.Context, name string, opts meta_v1.GetOptions, subresources ...string) (*meta_v1_unstruct.Unstructured, error) {
			// Returns a manifest with no status.conditions; the
			// expected Ready=True condition is missing, so the post-
			// close probe returns false and WaitForConditions reports
			// a watch-closed error.
			obj := &meta_v1_unstruct.Unstructured{}
			obj.SetName(name)
			obj.SetUnstructuredContent(map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]interface{}{"name": name},
			})
			return obj, nil
		},
	}

	rc := RestClientResultSuccess(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := WaitForConditions(
		ctx,
		rc,
		nil,
		[]types.WaitForStatusCondition{{Type: "Ready", Status: "True"}},
		"x",
		60*time.Second,
	)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error when post-close probe finds conditions unmet, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("WaitForConditions should fail promptly on closed channel; took %s", elapsed)
	}
}

// TestWaitForConditions_ClosedChannelReturnsSuccessWhenMatched covers
// the happy path. Closed channel + Get returns a manifest that matches
// the conditions (the apiserver delivered the matching state just as
// the watch closed) → return nil.
func TestWaitForConditions_ClosedChannelReturnsSuccessWhenMatched(t *testing.T) {
	t.Parallel()
	w := watch.NewFake()
	w.Stop()

	mock := &mockResourceInterface{
		watchFn: func(ctx context.Context, opts meta_v1.ListOptions) (watch.Interface, error) {
			return w, nil
		},
		getFn: func(ctx context.Context, name string, opts meta_v1.GetOptions, subresources ...string) (*meta_v1_unstruct.Unstructured, error) {
			obj := &meta_v1_unstruct.Unstructured{}
			obj.SetName(name)
			obj.SetUnstructuredContent(map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]interface{}{"name": name},
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True"},
					},
				},
			})
			return obj, nil
		},
	}

	rc := RestClientResultSuccess(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := WaitForConditions(
		ctx,
		rc,
		nil,
		[]types.WaitForStatusCondition{{Type: "Ready", Status: "True"}},
		"x",
		60*time.Second,
	)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error (probe satisfies conditions), got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("WaitForConditions should return promptly on closed channel; took %s", elapsed)
	}
}

// TestMatchesWaitConditions_TableDriven exercises the extracted helper
// directly. Each row pins one shape: missing conditions, present
// conditions, field eq match, field regex match, field missing,
// regex failure, all-or-nothing aggregation.
func TestMatchesWaitConditions_TableDriven(t *testing.T) {
	t.Parallel()

	makeManifest := func(content map[string]interface{}) *meta_v1_unstruct.Unstructured {
		obj := &meta_v1_unstruct.Unstructured{}
		obj.SetUnstructuredContent(content)
		return obj
	}

	cases := []struct {
		name     string
		manifest *meta_v1_unstruct.Unstructured
		conds    []types.WaitForStatusCondition
		fields   []types.WaitForField
		want     bool
	}{
		{
			"condition matches",
			makeManifest(map[string]interface{}{
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True"},
					},
				},
			}),
			[]types.WaitForStatusCondition{{Type: "Ready", Status: "True"}},
			nil,
			true,
		},
		{
			"condition status mismatched",
			makeManifest(map[string]interface{}{
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "False"},
					},
				},
			}),
			[]types.WaitForStatusCondition{{Type: "Ready", Status: "True"}},
			nil,
			false,
		},
		{
			"field eq match",
			makeManifest(map[string]interface{}{
				"spec": map[string]interface{}{"replicas": int64(3)},
			}),
			nil,
			[]types.WaitForField{{Key: "spec.replicas", Value: "3", ValueType: "eq"}},
			true,
		},
		{
			"field regex match",
			makeManifest(map[string]interface{}{
				"status": map[string]interface{}{"phase": "Active"},
			}),
			nil,
			[]types.WaitForField{{Key: "status.phase", Value: "Act.*", ValueType: "regex"}},
			true,
		},
		{
			"all-or-nothing: one condition missing fails the whole match",
			makeManifest(map[string]interface{}{
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True"},
					},
				},
			}),
			[]types.WaitForStatusCondition{
				{Type: "Ready", Status: "True"},
				{Type: "Synced", Status: "True"},
			},
			nil,
			false,
		},
		{
			"nil manifest is never matched",
			nil,
			[]types.WaitForStatusCondition{{Type: "Ready", Status: "True"}},
			nil,
			false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := matchesWaitConditions(tc.manifest, tc.fields, tc.conds, "test")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("matchesWaitConditions = %v, want %v", got, tc.want)
			}
		})
	}
}
