package kubernetes

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/alekc/terraform-provider-kubectl/internal/types"
	"github.com/alekc/terraform-provider-kubectl/yaml"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// wait_for_manifest.go: shared "wait for an object to exist and
// satisfy user predicates" helper for the read-side paths
// (kubectl_manifest data source and ephemeral resource). Issue #179.
//
// The resource's apply path (manifest_lifecycle.go) does its own
// wait via WaitForConditions because it has already issued the
// apply and knows the object will appear shortly. The data-source
// and ephemeral read paths cannot assume the object exists yet:
// they may be waiting on a controller-created Secret, an Argo
// rollout's Pod, a cert-manager Certificate's secretName, etc. So
// the read-side wait is structured in two phases:
//
//   - Phase A (WaitForManifestExists): poll Get with exponential
//     backoff until the apiserver returns the object, propagate
//     non-404 errors immediately, honour ctx cancellation.
//   - Phase B: hand off to the existing WaitForConditions, which
//     Get-probes once for the resourceVersion seed and then opens
//     a watch with a metadata.name field selector.
//
// Closing the race window in WaitForConditions's RV-seeding probe
// is the structural reason for phase A rather than handing off
// directly: the existing helper's Get returning 404 falls through
// to a watch with empty resourceVersion, and "watch from latest"
// loses any create event that fires between the Get and the watch
// handshake. With phase A we know the object exists before we
// start watching, so the RV is non-empty and the race is closed.

// defaultExistsPollInitial is the initial poll period for Phase A.
// Short enough to feel responsive when the controller is fast;
// the exponential backoff (capped by defaultExistsPollMax) means
// long waits don't hammer the apiserver.
const (
	defaultExistsPollInitial = 1 * time.Second
	defaultExistsPollMax     = 10 * time.Second
)

// DefaultManifestWaitTimeout is the wait_for default applied when
// the caller's `timeouts` block does not supply an override. Data
// source reads and ephemeral resource opens are typically faster
// than the kubectl_manifest resource's apply path (no rollout to
// bound), so 5m is shorter than the resource default. Used by
// both data source and ephemeral resource so the two share one
// canonical value.
const DefaultManifestWaitTimeout = 5 * time.Minute

// WaitForManifestOptions captures the inputs WaitForManifest reads
// from the caller. Same shape as ApplyManifestOptions / ReadManifestOptions
// for the fields they share, so the framework adapters can build
// the options struct without depending on internal/types directly.
type WaitForManifestOptions struct {
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	// WaitFor is the user-supplied predicate set. A nil value means
	// "wait for the object to exist, no condition / field checks".
	WaitFor *types.WaitFor
	// Timeout bounds the total time across both phases. Internally
	// the helper wraps ctx in context.WithTimeout, so callers do
	// not need to set a deadline themselves.
	Timeout time.Duration
}

// WaitForManifest blocks until the target object exists and any
// user-supplied wait_for predicates match, or until the timeout
// elapses. Read-side callers (data source, ephemeral resource)
// invoke this before FetchManifest so the subsequent Get returns
// the satisfied state.
//
// Phase A (exists) is bounded by the same timeout as phase B
// (conditions). A typical fast path (object exists, no predicates)
// completes in a single Get plus the WaitForConditions pre-probe;
// for the common deferred-create case (waiting for a controller)
// phase A consumes the bulk of the timeout and phase B's watch
// fires the moment the apiserver delivers the Added event.
func WaitForManifest(ctx context.Context, provider *KubeProvider, opts WaitForManifestOptions) error {
	if opts.Timeout <= 0 {
		return fmt.Errorf("WaitForManifest: timeout must be positive, got %v", opts.Timeout)
	}

	// Build a lookup manifest so GetRestClientFromUnstructured can
	// resolve the GVK to a dynamic.ResourceInterface. Same shape
	// the data source uses in FetchManifest.
	lookup := &meta_v1_unstruct.Unstructured{}
	lookup.SetAPIVersion(opts.APIVersion)
	lookup.SetKind(opts.Kind)
	lookup.SetName(opts.Name)
	if opts.Namespace != "" {
		lookup.SetNamespace(opts.Namespace)
	}
	manifest := yaml.NewFromUnstructured(lookup)

	restClient := GetRestClientFromUnstructuredWithContext(ctx, manifest, provider)
	if restClient.Error != nil {
		return fmt.Errorf("WaitForManifest: failed to build REST client for %s/%s: %w", opts.APIVersion, opts.Kind, restClient.Error)
	}

	return waitForManifestWithClient(ctx, restClient, opts)
}

// waitForManifestWithClient is the inner orchestration used by
// WaitForManifest after the REST client is built. Split out so
// unit tests can drive it with a stub *RestClientResult instead
// of needing a real *KubeProvider + discovery. Both Phase A (the
// poll-for-existence loop) and Phase B (the watch-and-evaluate
// predicate loop) honour ctx, which the wrapper bounds with the
// caller's opts.Timeout.
//
// Precondition: opts.Timeout > 0. The outer WaitForManifest
// validates this and rejects non-positive values; the inner
// function does NOT re-validate, so any future direct caller
// must validate or accept that context.WithTimeout(ctx, 0)
// returns an already-cancelled context (Phase A's first Get then
// surfaces as DeadlineExceeded wrapped by the helper).
func waitForManifestWithClient(ctx context.Context, restClient *RestClientResult, opts WaitForManifestOptions) error {
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	if err := WaitForManifestExists(ctx, restClient.ResourceInterface, opts.Name, defaultExistsPollInitial, defaultExistsPollMax); err != nil {
		return fmt.Errorf("WaitForManifest: %w", err)
	}

	// No predicates means Phase A is the whole job.
	if opts.WaitFor == nil || (len(opts.WaitFor.Field) == 0 && len(opts.WaitFor.Condition) == 0) {
		return nil
	}

	// Phase B: hand off to the existing watch-based predicate
	// runner. It Get-probes once more (cheap; the object exists
	// post-Phase-A) and uses the observed ResourceVersion to seed
	// the watch, eliminating the race-on-Added-event window.
	return WaitForConditions(ctx, restClient, opts.WaitFor.Field, opts.WaitFor.Condition, opts.Name, opts.Timeout)
}

// WaitForManifestExists polls Get on the given client until the
// named object is returned without an IsNotFound error, ctx
// cancels, or a non-404 error surfaces. Polling interval starts at
// initial and grows exponentially up to maxInterval.
//
// 404 is the only "keep waiting" signal; every other error (RBAC
// forbidden, apiserver unreachable, etc.) returns immediately so a
// misconfigured caller surfaces the problem at the right time
// rather than after the full timeout.
func WaitForManifestExists(ctx context.Context, client dynamic.ResourceInterface, name string, initial, maxInterval time.Duration) error {
	if initial <= 0 {
		initial = defaultExistsPollInitial
	}
	if maxInterval < initial {
		maxInterval = initial
	}
	period := initial

	for {
		if _, err := client.Get(ctx, name, meta_v1.GetOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("get %s: %w", name, err)
			}
			log.Printf("[TRACE] WaitForManifestExists: %s not yet present, retrying in %s", name, period)
		} else {
			return nil
		}

		select {
		case <-time.After(period):
		case <-ctx.Done():
			// Distinguish deadline-exceeded (timeout) from
			// explicit cancellation so the caller's diagnostic
			// names the right cause.
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("%s did not exist within the wait timeout", name)
			}
			return ctx.Err()
		}

		// Exponential backoff, capped.
		period *= 2
		if period > maxInterval {
			period = maxInterval
		}
	}
}
