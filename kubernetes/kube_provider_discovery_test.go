package kubernetes

import (
	"testing"
	"time"

	restclient "k8s.io/client-go/rest"
)

// TestDiscoveryRestConfig_BoundsOnlyWhenAppropriate pins the timeout-scoping
// rule behind the issue #344 fix: discovery HTTP requests are bounded so a
// slow or unhealthy aggregated APIService surfaces as a tolerated
// GroupDiscoveryFailedError instead of pinning the 60s outer timer, but the
// bound must never loosen a user's tighter kubeconfig timeout, and a zero
// timeout must preserve the historic unbounded behaviour.
func TestDiscoveryRestConfig_BoundsOnlyWhenAppropriate(t *testing.T) {
	cases := []struct {
		name        string
		baseTimeout time.Duration
		timeout     time.Duration
		want        time.Duration
	}{
		{"zero timeout leaves unbounded base untouched", 0, 0, 0},
		{"zero timeout leaves user timeout untouched", 15 * time.Second, 0, 15 * time.Second},
		{"applies bound to unbounded base", 0, 30 * time.Second, 30 * time.Second},
		{"lowers a looser user timeout", 60 * time.Second, 30 * time.Second, 30 * time.Second},
		{"keeps a tighter user timeout", 10 * time.Second, 30 * time.Second, 10 * time.Second},
		{"negative timeout treated as no bound", 0, -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := restclient.Config{Host: "https://example.invalid", Timeout: tc.baseTimeout}
			got := discoveryRestConfig(base, tc.timeout)
			if got.Timeout != tc.want {
				t.Errorf("discoveryRestConfig timeout = %v, want %v", got.Timeout, tc.want)
			}
			if base.Timeout != tc.baseTimeout {
				t.Errorf("base config was mutated: Timeout = %v, want %v (the dynamic apply/read client must stay unbounded)", base.Timeout, tc.baseTimeout)
			}
			if got.Host != base.Host {
				t.Errorf("unrelated field changed: Host = %q, want %q", got.Host, base.Host)
			}
		})
	}
}

// TestToDiscoveryClient_MemoisesSingleInstance pins the issue #344 thundering-
// herd fix: every resource in the process shares one cached discovery client,
// so discovery is enumerated once instead of once per resource on a cold
// cache. ToDiscoveryClient must therefore return the same instance on repeated
// calls. Constructed directly (no BuildKubeProvider) to prove the zero-value
// sync.Once works, which is what the lazy_load empty-config path relies on.
func TestToDiscoveryClient_MemoisesSingleInstance(t *testing.T) {
	kp := &KubeProvider{RestConfig: restclient.Config{}}

	first, err := kp.ToDiscoveryClient()
	if err != nil {
		t.Fatalf("first ToDiscoveryClient: %v", err)
	}
	if first == nil {
		t.Fatalf("first ToDiscoveryClient returned a nil client")
	}

	second, err := kp.ToDiscoveryClient()
	if err != nil {
		t.Fatalf("second ToDiscoveryClient: %v", err)
	}
	if first != second {
		t.Errorf("ToDiscoveryClient returned a fresh client on the second call; expected the memoised instance")
	}
}
