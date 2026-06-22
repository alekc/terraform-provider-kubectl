package kubernetes

import (
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mitchellh/go-homedir"
	"k8s.io/apimachinery/pkg/api/meta"
	k8sresource "k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/discovery"
	diskcached "k8s.io/client-go/discovery/cached/disk"
	memcached "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	aggregator "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset"
)

// KubeProvider is the carrier for a configured Kubernetes client set + REST
// config. It is built once per provider block by
// kubernetes.BuildKubeProvider, stored on resp.ResourceData /
// DataSourceData / EphemeralResourceData by the framework provider's
// Configure pass, and read directly by every resource and data source via
// a type assertion. The same struct is passed to the lifecycle helpers
// (ApplyManifest, ReadManifest, etc.) so they can re-use the configured
// REST config without re-reading kubeconfig.
type KubeProvider struct {
	MainClientset *kubernetes.Clientset
	RestConfig    restclient.Config
	// AggregatorClientset is the interface (not a concrete clientset) so
	// tests can substitute the upstream fake from
	// kube-aggregator/.../clientset/fake without faking out the REST
	// transport. Production code constructs a real *aggregator.Clientset
	// from provider_config.go; the interface widening is purely additive.
	AggregatorClientset aggregator.Interface
	// ApplyRetryCount is how many times an apply is retried with exponential
	// backoff before surfacing the error. Sourced from the
	// `apply_retry_count` provider arg (or KUBECTL_PROVIDER_APPLY_RETRY_COUNT).
	// Held per-provider so aliased blocks with different values do not
	// clobber each other (issue #265).
	ApplyRetryCount uint64

	// DiscoveryTimeout bounds each discovery HTTP request so a slow or
	// unhealthy aggregated APIService cannot pin the full discovery
	// enumeration (issue #344). Sourced from KUBECTL_PROVIDER_DISCOVERY_TIMEOUT
	// (default defaultDiscoveryTimeout). Zero means "no bound", which is the
	// historic behaviour and what a directly-constructed KubeProvider (tests)
	// gets. The bound is applied to a copy of RestConfig used only for
	// discovery, never the dynamic apply/read client.
	DiscoveryTimeout time.Duration

	// discoveryOnce memoises the cached discovery client so every resource in
	// this process shares one in-memory discovery result instead of
	// re-enumerating the cluster per call. KubeProvider is always used by
	// pointer, so the embedded sync.Once is never copied.
	discoveryOnce   sync.Once
	discoveryClient discovery.CachedDiscoveryInterface
	discoveryErr    error
}

// defaultDiscoveryTimeout bounds a single discovery HTTP request. It sits well
// below the 60s outer timer in getRestClientFromUnstructured so a slow group
// times out (and is tolerated as a GroupDiscoveryFailedError) before the outer
// timer fails the whole read with "timed out fetching resources from discovery
// client" (issue #344).
const defaultDiscoveryTimeout = 30 * time.Second

var _ k8sresource.RESTClientGetter = &KubeProvider{}

// ToRawKubeConfigLoader satisfies the RESTClientGetter interface. The
// returned loader is intentionally nil: this provider builds its REST
// config from the typed ProviderConfig once at configure time and does
// not expose a runtime kubeconfig loader.
func (p *KubeProvider) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return nil
}

// ToRESTConfig returns the configured REST config. Pointer is to the
// struct on KubeProvider, so callers must not mutate it.
func (p *KubeProvider) ToRESTConfig() (*restclient.Config, error) {
	return &p.RestConfig, nil
}

// ToDiscoveryClient returns the provider's shared cached discovery client,
// building it once on first use. Sharing one client across every resource in
// the process means discovery is enumerated once (the in-memory cache
// serialises concurrent first-fetches) instead of N parallel resources each
// re-enumerating the cluster on a cold cache (issue #344). The memoised error
// is only the construction error, which is deterministic; transient discovery
// failures are not cached because the HTTP fetch happens later, on the
// returned client.
func (p *KubeProvider) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	p.discoveryOnce.Do(func() {
		p.discoveryClient, p.discoveryErr = p.newCachedDiscoveryClient()
	})
	return p.discoveryClient, p.discoveryErr
}

// newCachedDiscoveryClient builds the two-layer discovery client: a disk-cached
// client (cross-process, ~/.kube/cache/discovery/<hostname>, mirroring the
// kubectl convention so concurrent kubectl usage shares the cache) wrapped in
// an in-memory cache (in-process sharing + concurrent-fetch dedup).
//
// The REST config is copied and given a per-request Timeout so a slow or
// unhealthy aggregated APIService (metrics.k8s.io, custom.metrics,
// webhook-backed groups) surfaces as a tolerated GroupDiscoveryFailedError
// instead of pinning the enumeration until the 60s outer timer fails the read
// (issue #344). The bound is scoped to this copy and never reaches the dynamic
// apply/read client, which must stay unbounded for long applies and waits. A
// zero DiscoveryTimeout (directly-constructed KubeProvider) leaves the config
// timeout untouched, preserving the historic behaviour.
func (p *KubeProvider) newCachedDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	home, err := homedir.Dir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory for discovery cache: %w", err)
	}
	httpCacheDir := filepath.Join(home, ".kube", "http-cache")
	discoveryCacheDir := computeDiscoverCacheDir(filepath.Join(home, ".kube", "cache", "discovery"), p.RestConfig.Host)

	discoveryConfig := discoveryRestConfig(p.RestConfig, p.DiscoveryTimeout)
	diskClient, err := diskcached.NewCachedDiscoveryClientForConfig(&discoveryConfig, discoveryCacheDir, httpCacheDir, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return memcached.NewMemCacheClient(diskClient), nil
}

// discoveryRestConfig returns a copy of base bounded to timeout for discovery
// HTTP requests. A zero (or negative) timeout leaves base untouched, preserving
// the historic unbounded behaviour. Otherwise the copy's Timeout is lowered to
// timeout only when it is unset or already larger, so a user-supplied tighter
// kubeconfig timeout still wins. base is taken by value and returned, so the
// caller's RestConfig (and the dynamic apply/read client built from it) is
// never mutated; only discovery is bounded (issue #344).
func discoveryRestConfig(base restclient.Config, timeout time.Duration) restclient.Config {
	if timeout > 0 && (base.Timeout == 0 || base.Timeout > timeout) {
		base.Timeout = timeout
	}
	return base
}

// ToRESTMapper returns a deferred-discovery REST mapper wrapped in the
// kubectl-style shortcut expander. Errors from ToDiscoveryClient are
// surfaced verbatim rather than collapsed into the historic "no
// restmapper" sentinel; on an empty (lazy_load) RestConfig the
// construction still succeeds, deferring the empty-Host failure to
// first use (verified by TestKubeProvider_RESTMapperAcceptsEmptyConfig).
func (p *KubeProvider) ToRESTMapper() (meta.RESTMapper, error) {
	discoveryClient, err := p.ToDiscoveryClient()
	if err != nil {
		return nil, fmt.Errorf("failed to build discovery client for rest mapper: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	expander := restmapper.NewShortcutExpander(mapper, discoveryClient, func(msg string) {
		log.Printf("[WARN] error in expander: %s", msg)
	})
	return expander, nil
}

// overlyCautiousIllegalFileCharacters matches characters that *might* not
// be supported as filesystem path components on every OS we run on.
// Windows is restrictive, so this regex is conservative.
var overlyCautiousIllegalFileCharacters = regexp.MustCompile(`[^(\w/\.)]`)

// computeDiscoverCacheDir builds a usually-non-colliding cache directory
// name from a parent dir and a kubernetes host. Collisions are possible
// but unlikely; even on collision the only failure mode is a short-lived
// cache miss.
func computeDiscoverCacheDir(parentDir, host string) string {
	schemelessHost := strings.Replace(strings.Replace(host, "https://", "", 1), "http://", "", 1)
	safeHost := overlyCautiousIllegalFileCharacters.ReplaceAllString(schemelessHost, "_")
	return filepath.Join(parentDir, safeHost)
}
