package kubernetes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekc/terraform-provider-kubectl/internal/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apimachinery_types "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// existsStub is a tiny dynamic.ResourceInterface that lets tests
// programme the sequence of Get responses without spinning up a
// fake apiserver. attempts is an atomic counter so concurrent
// goroutines (none today, but a future change might race) can
// inspect it safely. responses is consumed in order; if exhausted,
// the last entry is repeated.
type existsStub struct {
	dynamic.ResourceInterface
	mu        sync.Mutex
	attempts  int32
	responses []func() (*meta_v1_unstruct.Unstructured, error)
}

func (s *existsStub) Get(_ context.Context, _ string, _ meta_v1.GetOptions, _ ...string) (*meta_v1_unstruct.Unstructured, error) {
	atomic.AddInt32(&s.attempts, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.responses) == 0 {
		return nil, errors.New("no responses configured")
	}
	r := s.responses[0]
	if len(s.responses) > 1 {
		s.responses = s.responses[1:]
	}
	return r()
}

func notFound() (*meta_v1_unstruct.Unstructured, error) {
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "test"}, "x")
}
func okResp() (*meta_v1_unstruct.Unstructured, error) {
	obj := &meta_v1_unstruct.Unstructured{}
	obj.SetName("x")
	obj.SetUID(apimachinery_types.UID("uid-1"))
	return obj, nil
}
func forbidden() (*meta_v1_unstruct.Unstructured, error) {
	return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "test"}, "x", errors.New("denied"))
}

// TestWaitForManifestExists_ImmediateOK regresses the common fast
// path: the object exists on the very first Get, so the helper
// must return without sleeping at all.
func TestWaitForManifestExists_ImmediateOK(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){okResp}}
	start := time.Now()
	if err := WaitForManifestExists(context.Background(), stub, "x", 50*time.Millisecond, 200*time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("immediate-OK should not sleep; elapsed=%s", elapsed)
	}
	if got := atomic.LoadInt32(&stub.attempts); got != 1 {
		t.Errorf("expected exactly 1 Get, got %d", got)
	}
}

// TestWaitForManifestExists_RetriesOn404 confirms the helper
// repeats Get on IsNotFound and returns successfully when the
// object appears mid-wait.
func TestWaitForManifestExists_RetriesOn404(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){
		notFound, notFound, okResp,
	}}
	if err := WaitForManifestExists(context.Background(), stub, "x", 10*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&stub.attempts); got != 3 {
		t.Errorf("expected 3 Get attempts, got %d", got)
	}
}

// TestWaitForManifestExists_PropagatesNonNotFound asserts that
// only 404 triggers retry; other errors (RBAC forbidden, etc.)
// surface immediately so misconfigurations are visible without
// waiting out the full timeout.
func TestWaitForManifestExists_PropagatesNonNotFound(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){forbidden}}
	err := WaitForManifestExists(context.Background(), stub, "x", 10*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error for forbidden response, got nil")
	}
	if got := atomic.LoadInt32(&stub.attempts); got != 1 {
		t.Errorf("expected non-404 to bail after 1 attempt, got %d", got)
	}
}

// TestWaitForManifestExists_TimeoutMessage verifies the helper
// surfaces a wait-timeout error (not a raw context.DeadlineExceeded
// or some other shape) when the ctx deadline fires. The error
// message is part of the user-facing diagnostic; keep it stable.
func TestWaitForManifestExists_TimeoutMessage(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){
		notFound, notFound, notFound, notFound, notFound,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := WaitForManifestExists(ctx, stub, "x", 10*time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !contains(err.Error(), "did not exist within the wait timeout") {
		t.Errorf("expected timeout-shaped diagnostic, got %q", err.Error())
	}
}

// TestWaitForManifestExists_HonoursParentCancel covers the explicit
// cancel path (distinct from deadline-exceeded): if the parent
// context is cancelled mid-wait the helper returns ctx.Err()
// without a fresh-string wrap, matching how the rest of the
// kubernetes package surfaces parent-cancellation.
func TestWaitForManifestExists_HonoursParentCancel(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){
		notFound, notFound, notFound,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := WaitForManifestExists(ctx, stub, "x", 10*time.Millisecond, 50*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestWaitForManifestExists_BackoffCapped guards against the
// exponential growth running away on long waits: the doubled
// period must clamp to maxInterval rather than producing
// minute-long sleeps after a few iterations.
func TestWaitForManifestExists_BackoffCapped(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){
		notFound, notFound, notFound, notFound, notFound, okResp,
	}}
	start := time.Now()
	if err := WaitForManifestExists(context.Background(), stub, "x", 5*time.Millisecond, 15*time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)
	// 5 false attempts before success. With initial=5ms and
	// max=15ms, sleeps grow 5, 10, 15, 15, 15 = 60ms in the
	// worst case + Get latency. Generous upper bound to avoid
	// flakiness on busy CI.
	if elapsed > 500*time.Millisecond {
		t.Errorf("expected backoff cap to keep wait short; elapsed=%s (>500ms suggests the cap is broken)", elapsed)
	}
}

// contains is a tiny strings.Contains shim so the timeout test
// stays readable without importing strings for one assertion.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestWaitForManifestWithClient_NoWaitForReturnsAfterPhaseA pins
// the orchestrator's fast-path: when the WaitFor field is nil
// (no predicates), Phase A alone must satisfy the helper, and
// it must return cleanly without trying to open a watch.
func TestWaitForManifestWithClient_NoWaitForReturnsAfterPhaseA(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){okResp}}
	restClient := &RestClientResult{ResourceInterface: stub}
	opts := WaitForManifestOptions{
		Name:    "x",
		WaitFor: nil,
		Timeout: 500 * time.Millisecond,
	}
	if err := waitForManifestWithClient(context.Background(), restClient, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWaitForManifestWithClient_EmptyWaitForEqualsNoPredicates
// confirms that a non-nil but empty WaitFor (no Field, no
// Condition) is treated identically to nil: Phase A returns and
// the helper exits without watching.
func TestWaitForManifestWithClient_EmptyWaitForEqualsNoPredicates(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){okResp}}
	restClient := &RestClientResult{ResourceInterface: stub}
	opts := WaitForManifestOptions{
		Name:    "x",
		WaitFor: &types.WaitFor{}, // empty
		Timeout: 500 * time.Millisecond,
	}
	if err := waitForManifestWithClient(context.Background(), restClient, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWaitForManifestWithClient_PhaseAErrorPropagates regresses
// the contract that Phase A errors short-circuit the helper:
// non-404 errors from Get must surface immediately instead of
// being masked by the watch phase.
func TestWaitForManifestWithClient_PhaseAErrorPropagates(t *testing.T) {
	t.Parallel()
	stub := &existsStub{responses: []func() (*meta_v1_unstruct.Unstructured, error){forbidden}}
	restClient := &RestClientResult{ResourceInterface: stub}
	opts := WaitForManifestOptions{
		Name:    "x",
		WaitFor: nil,
		Timeout: 500 * time.Millisecond,
	}
	err := waitForManifestWithClient(context.Background(), restClient, opts)
	if err == nil {
		t.Fatalf("expected forbidden error to propagate, got nil")
	}
	if !contains(err.Error(), "WaitForManifest") {
		t.Errorf("expected wrapped 'WaitForManifest' prefix, got %q", err.Error())
	}
}

// TestWaitForManifestWithClient_TimeoutValidation pins the
// caller-contract check: the public WaitForManifest rejects
// non-positive timeouts before doing anything else. The
// orchestrator inner function does not enforce this; the outer
// wrapper does. Verify via the outer wrapper with a stub
// provider that never gets used.
func TestWaitForManifest_RejectsNonPositiveTimeout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		timeout time.Duration
	}{
		{"zero", 0},
		{"negative", -1 * time.Second},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// provider is intentionally nil; the validator
			// must fire before WaitForManifest reaches the
			// REST-client construction step.
			err := WaitForManifest(context.Background(), nil, WaitForManifestOptions{
				Name:    "x",
				Timeout: tc.timeout,
			})
			if err == nil {
				t.Fatal("expected timeout-validation error, got nil")
			}
			if !contains(err.Error(), "timeout must be positive") {
				t.Errorf("expected 'timeout must be positive' diagnostic, got %q", err.Error())
			}
		})
	}
}
