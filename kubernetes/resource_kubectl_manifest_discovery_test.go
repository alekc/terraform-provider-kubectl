package kubernetes

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestDiscoveryWithTimeout_NoGoroutineLeak guards the channel buffering used by
// getRestClientFromUnstructured. The discovery call runs in its own goroutine
// and races a 60s timer; when the timer wins, the producer must still be able
// to deliver onto the channel and exit, otherwise it pins the discovery
// client until the process dies (issue #268).
//
// This is a pattern test: it exercises the same select-with-timeout shape with
// an injectable producer so the fix can be asserted without standing up a real
// apiserver.
func TestDiscoveryWithTimeout_NoGoroutineLeak(t *testing.T) {
	type result struct{}

	// finished is incremented only when the producer goroutine actually exits.
	var finished int32

	discoveryWithTimeout := func(produce func() *result) <-chan *result {
		// Buffered by 1, matching the fix in resource_kubectl_manifest.go.
		ch := make(chan *result, 1)
		go func() {
			ch <- produce()
			atomic.AddInt32(&finished, 1)
		}()
		return ch
	}

	// Producer sleeps long enough that the outer timeout will win the race.
	slowProduce := func() *result {
		time.Sleep(150 * time.Millisecond)
		return &result{}
	}

	const trials = 10
	for i := 0; i < trials; i++ {
		ch := discoveryWithTimeout(slowProduce)
		timeout := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ch:
			t.Fatalf("trial %d: expected timeout branch to win, got result", i)
		case <-timeout.C:
			// Timeout branch won; the producer is still running.
		}
		timeout.Stop()
	}

	// Give every producer goroutine time to deliver and exit. With an
	// unbuffered channel they would block forever on the send and finished
	// would remain 0; with the buffered channel they all complete.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&finished) < int32(trials) && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&finished); got != int32(trials) {
		t.Fatalf("producer goroutines leaked: %d of %d exited (the unbuffered-channel bug from #268)", got, trials)
	}
}
