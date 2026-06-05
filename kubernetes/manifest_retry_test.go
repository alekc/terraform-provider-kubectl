package kubernetes

import (
	"context"
	"errors"
	"testing"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zeroBackoff returns a backoff that never sleeps, so the retry tests
// below exercise the attempt-count and ctx-cancel control flow without
// waiting for the production 3s..30s intervals.
func zeroBackoff() backoff.BackOff { return backoff.NewConstantBackOff(0) }

// TestNewApplyBackoff_Tuning locks in the two deliberate deviations from
// the library defaults that the apply retry contract depends on: a hard
// 30s ceiling (RandomizationFactor 0, so no jitter pushes a sleep past
// MaxInterval) and no wall-clock stop (MaxElapsedTime 0, so WithMaxRetries
// is the sole loop bound). A regression here silently changes documented
// retry behaviour, so assert the fields directly.
func TestNewApplyBackoff_Tuning(t *testing.T) {
	b := newApplyBackoff()
	assert.Equal(t, applyRetryInitialInterval, b.InitialInterval, "initial interval")
	assert.Equal(t, applyRetryMaxInterval, b.MaxInterval, "max interval")
	assert.Equal(t, 3*time.Second, b.InitialInterval, "initial interval literal")
	assert.Equal(t, 30*time.Second, b.MaxInterval, "max interval literal")
	assert.Zero(t, b.RandomizationFactor, "randomization factor must be 0 for a hard ceiling")
	assert.Zero(t, b.MaxElapsedTime, "max elapsed time must be 0 so WithMaxRetries bounds the loop")
}

// TestNewApplyBackoff_HardCeiling drives NextBackOff far past the point
// where the exponential base would exceed MaxInterval and asserts the
// returned sleep never exceeds the 30s ceiling. With RandomizationFactor
// 0 the cap is exact; the default 0.5 jitter would allow up to 1.5x.
func TestNewApplyBackoff_HardCeiling(t *testing.T) {
	b := newApplyBackoff()
	b.Reset()
	for i := 0; i < 50; i++ {
		next := b.NextBackOff()
		require.NotEqual(t, backoff.Stop, next, "MaxElapsedTime=0 must not stop the backoff")
		assert.LessOrEqual(t, next, applyRetryMaxInterval, "sleep must never exceed the 30s ceiling")
	}
}

// TestRunWithApplyRetry_ZeroCountSingleShotOnError is the issue #228
// contract: retryCount 0 runs the operation exactly once and does NOT
// retry, even when it fails. The single-shot path must not spend the
// rate-limit budget on a retry the user disabled.
func TestRunWithApplyRetry_ZeroCountSingleShotOnError(t *testing.T) {
	wantErr := errors.New("apply boom")
	calls := 0
	op := func() error {
		calls++
		return wantErr
	}

	err := runWithApplyRetry(context.Background(), 0, op)

	assert.Equal(t, 1, calls, "retryCount 0 must run op exactly once")
	assert.ErrorIs(t, err, wantErr, "the single op error must be returned verbatim")
}

// TestRunWithApplyRetry_ZeroCountSuccess confirms the happy single-shot
// path: one call, nil error.
func TestRunWithApplyRetry_ZeroCountSuccess(t *testing.T) {
	calls := 0
	err := runWithApplyRetry(context.Background(), 0, func() error {
		calls++
		return nil
	})
	assert.Equal(t, 1, calls)
	assert.NoError(t, err)
}

// TestRunWithApplyRetryPolicy_RetriesThenSucceeds verifies the attempt
// bound: with retryCount 3 and an op that fails twice then succeeds, the
// op runs exactly 3 times and the overall result is success.
func TestRunWithApplyRetryPolicy_RetriesThenSucceeds(t *testing.T) {
	calls := 0
	op := func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	}

	err := runWithApplyRetryPolicy(context.Background(), 3, zeroBackoff(), op)

	assert.NoError(t, err)
	assert.Equal(t, 3, calls, "should stop retrying once op succeeds")
}

// TestRunWithApplyRetryPolicy_ExhaustsRetries verifies the worst-case
// bound: retryCount N runs the op N+1 times (initial + N retries) when it
// always fails, then returns the last error.
func TestRunWithApplyRetryPolicy_ExhaustsRetries(t *testing.T) {
	wantErr := errors.New("always fails")
	calls := 0
	op := func() error {
		calls++
		return wantErr
	}

	err := runWithApplyRetryPolicy(context.Background(), 2, zeroBackoff(), op)

	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, 3, calls, "retryCount 2 must produce 1 + 2 = 3 total attempts")
}

// TestRunWithApplyRetryPolicy_CancelledCtxStopsBeforeRetry proves a ctx
// already cancelled when the loop starts lets the first attempt run, then
// stops rather than burning the full retry budget. op runs once.
func TestRunWithApplyRetryPolicy_CancelledCtxStopsBeforeRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	wantErr := errors.New("fail on cancelled ctx")
	calls := 0
	op := func() error {
		calls++
		return wantErr
	}

	err := runWithApplyRetryPolicy(ctx, 5, zeroBackoff(), op)

	assert.Error(t, err)
	assert.Equal(t, 1, calls, "a cancelled ctx must stop retries after the first attempt")
}

// TestRunWithApplyRetryPolicy_CancelDuringInterruptsLoop proves that a
// cancellation happening mid-run (here, the op cancels on its first
// failure) interrupts the retry loop instead of continuing to the
// configured retry count. Contrast with ExhaustsRetries (same N=2, no
// cancel) which runs 3 times; here it runs once.
func TestRunWithApplyRetryPolicy_CancelDuringInterruptsLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	op := func() error {
		calls++
		cancel() // user hits Ctrl-C / resource timeout fires mid-apply
		return errors.New("failed, and now cancelled")
	}

	err := runWithApplyRetryPolicy(ctx, 2, zeroBackoff(), op)

	assert.Error(t, err)
	assert.Equal(t, 1, calls, "cancellation during the run must cut the loop short")
}
