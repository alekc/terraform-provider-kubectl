package kubernetes

import (
	"runtime"
	"testing"
	"time"
)

// TestDiscoveryWithTimeout_NoGoroutineLeak guards the channel buffering used
// by the package-level discoveryWithTimeout helper that
// getRestClientFromUnstructured relies on. The discovery call runs in its own
// goroutine and races a 60s timer; when the timer wins, the producer must
// still be able to deliver onto the channel and exit, otherwise it pins the
// discovery client until the process dies (issue #268). If a future change
// drops the buffer from discoveryWithTimeout, this test fails because the
// producer goroutines block forever on the send and the goroutine count never
// returns to its pre-test baseline.
func TestDiscoveryWithTimeout_NoGoroutineLeak(t *testing.T) {
	// Force any background goroutines spawned by package init to settle so
	// the baseline reading is stable.
	time.Sleep(20 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()

	// Producer sleeps long enough that the outer timeout always wins the
	// race, then returns. With the buffered channel the send succeeds and
	// the goroutine exits; with an unbuffered channel it blocks on the send
	// forever and leaks.
	slowProduce := func() *RestClientResult {
		time.Sleep(50 * time.Millisecond)
		return RestClientResultFromErr(nil)
	}

	const trials = 20
	for i := 0; i < trials; i++ {
		ch := discoveryWithTimeout(slowProduce)
		timeout := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ch:
			t.Fatalf("trial %d: expected timeout branch to win, got result", i)
		case <-timeout.C:
			// Timeout branch won; the producer is still running.
		}
		timeout.Stop()
	}

	// Wait for the goroutine count to settle back to baseline. With a
	// buffered channel every producer can finish its send and exit within a
	// few hundred milliseconds. With an unbuffered channel they never exit.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}

	if delta := runtime.NumGoroutine() - baseline; delta > 0 {
		t.Fatalf("producer goroutines leaked: %d still running above baseline (the unbuffered-channel bug from #268)", delta)
	}
}
