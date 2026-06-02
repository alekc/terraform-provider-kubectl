package kubernetes

import (
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mitchellh/go-homedir"
	"k8s.io/apimachinery/pkg/api/meta"
	k8sresource "k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/discovery"
	diskcached "k8s.io/client-go/discovery/cached/disk"
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
	MainClientset       *kubernetes.Clientset
	RestConfig          restclient.Config
	AggregatorClientset *aggregator.Clientset
	// ApplyRetryCount is how many times an apply is retried with exponential
	// backoff before surfacing the error. Sourced from the
	// `apply_retry_count` provider arg (or KUBECTL_PROVIDER_APPLY_RETRY_COUNT).
	// Held per-provider so aliased blocks with different values do not
	// clobber each other (issue #265).
	ApplyRetryCount uint64
}

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

// ToDiscoveryClient returns a disk-cached discovery client scoped to the
// configured apiserver. The cache directory layout mirrors the kubectl
// convention (~/.kube/cache/discovery/<hostname>) so concurrent kubectl
// usage shares the cache.
func (p *KubeProvider) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	home, err := homedir.Dir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory for discovery cache: %w", err)
	}
	httpCacheDir := filepath.Join(home, ".kube", "http-cache")
	discoveryCacheDir := computeDiscoverCacheDir(filepath.Join(home, ".kube", "cache", "discovery"), p.RestConfig.Host)
	return diskcached.NewCachedDiscoveryClientForConfig(&p.RestConfig, discoveryCacheDir, httpCacheDir, 10*time.Minute)
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
