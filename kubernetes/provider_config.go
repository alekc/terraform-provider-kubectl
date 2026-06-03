package kubernetes

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mitchellh/go-homedir"
	apimachineryschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	aggregator "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset"
)

// ProviderConfig is the typed view of the provider configuration block that
// BuildKubeProvider consumes. Callers (the framework provider's Configure
// method, or a unit test) populate it from request config + env-var
// fallbacks + attribute defaults, then hand it to BuildKubeProvider which
// returns the configured client carrier.
//
// String fields use the empty string to mean "unset"; bool fields are
// typed as a *bool when the unset / true / false trichotomy matters
// (load_config_file and lazy_load have non-false defaults). Numeric
// fields default to zero unless populated. The Exec slice models the
// repeating `exec {}` block; the runtime check rejects length > 1
// (the schema can no longer express MaxItems = 1 on the framework side,
// see issue #275).
type ProviderConfig struct {
	ApplyRetryCount      int64
	Host                 string
	Username             string
	Password             string
	Insecure             bool
	ClientCertificate    string
	ClientKey            string
	ClusterCACertificate string
	ConfigPaths          []string
	ConfigPath           string
	ConfigContext        string
	ConfigContextAuth    string
	ConfigContextCluster string
	Token                string
	ProxyURL             string
	LoadConfigFile       bool
	LazyLoad             bool
	TLSServerName        string
	Exec                 []ExecConfig
}

// ExecConfig models one exec block on the provider. The framework declares
// it as a ListNestedBlock and the SDK v2 historically did too; runtime
// enforces at-most-one block via BuildKubeProvider's length check.
type ExecConfig struct {
	APIVersion string
	Command    string
	Args       []string
	Env        map[string]string
}

// BuildKubeProvider materialises a KubeProvider client carrier from a
// fully-resolved ProviderConfig. The caller is responsible for applying
// env-var fallbacks and attribute defaults before invoking this; the
// builder does not consult os.Getenv except for KUBECTL_PROVIDER_APPLY_RETRY_COUNT
// (which historically overrides the schema value at runtime).
//
// terraformVersion is interpolated into the User-Agent header so the
// apiserver's audit log identifies the caller's tooling version. Pass
// "" for non-framework callers (unit tests) and the builder substitutes
// the legacy "0.11+compatible" sentinel.
func BuildKubeProvider(cfg ProviderConfig, terraformVersion string) (*KubeProvider, error) {
	if terraformVersion == "" {
		terraformVersion = "0.11+compatible"
	}

	restCfg, err := buildRestConfig(cfg)
	if err != nil {
		return nil, err
	}
	if restCfg == nil {
		restCfg = &restclient.Config{}
	}

	if cfg.ApplyRetryCount < 0 {
		return nil, fmt.Errorf("apply_retry_count must be >= 0, got %d", cfg.ApplyRetryCount)
	}
	applyRetryCount := uint64(cfg.ApplyRetryCount)
	if v := os.Getenv("KUBECTL_PROVIDER_APPLY_RETRY_COUNT"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("KUBECTL_PROVIDER_APPLY_RETRY_COUNT: %w", err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("KUBECTL_PROVIDER_APPLY_RETRY_COUNT must be >= 0, got %d", parsed)
		}
		applyRetryCount = uint64(parsed)
	}

	restCfg.QPS = 100.0
	restCfg.Burst = 100
	restCfg.UserAgent = fmt.Sprintf("HashiCorp/1.0 Terraform/%s", terraformVersion)

	k, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to configure: %s", err)
	}
	a, err := aggregator.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to configure: %s", err)
	}

	return &KubeProvider{
		MainClientset:       k,
		RestConfig:          *restCfg,
		AggregatorClientset: a,
		ApplyRetryCount:     applyRetryCount,
	}, nil
}

// buildRestConfig is the typed-input replacement for the SDK v2
// initializeConfiguration helper. It resolves kubeconfig precedence
// (config_path, then config_paths, then KUBE_CONFIG_PATHS env, then
// implicit ~/.kube/config via the default loader), applies overrides
// from the explicit provider arguments, and honours the lazy_load
// opt-out for `terraform plan` against not-yet-applied bootstraps
// (see issue #283).
//
// Returns (nil, nil) when lazy_load = true and clientcmd surfaces an
// error: the caller substitutes an empty restclient.Config so plan can
// proceed and the real failure resurfaces at apply time.
func buildRestConfig(cfg ProviderConfig) (*restclient.Config, error) {
	overrides := &clientcmd.ConfigOverrides{}
	loader := &clientcmd.ClientConfigLoadingRules{}

	configPaths := resolveConfigPaths(cfg)

	if len(cfg.Exec) > 1 {
		return nil, fmt.Errorf("provider supports at most one exec block, got %d", len(cfg.Exec))
	}
	if len(cfg.Exec) == 1 {
		exec := &clientcmdapi.ExecConfig{
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
			APIVersion:      cfg.Exec[0].APIVersion,
			Command:         cfg.Exec[0].Command,
			Args:            cfg.Exec[0].Args,
		}
		for k, v := range cfg.Exec[0].Env {
			exec.Env = append(exec.Env, clientcmdapi.ExecEnvVar{Name: k, Value: v})
		}
		overrides.AuthInfo.Exec = exec
	} else if cfg.LoadConfigFile && len(configPaths) > 0 {
		expandedPaths := make([]string, 0, len(configPaths))
		for _, p := range configPaths {
			path, err := homedir.Expand(p)
			if err != nil {
				return nil, err
			}
			log.Printf("[DEBUG] Using kubeconfig: %s", path)
			expandedPaths = append(expandedPaths, path)
		}
		if len(expandedPaths) == 1 {
			loader.ExplicitPath = expandedPaths[0]
		} else {
			loader.Precedence = expandedPaths
		}

		if cfg.ConfigContext != "" || cfg.ConfigContextAuth != "" || cfg.ConfigContextCluster != "" {
			if cfg.ConfigContext != "" {
				overrides.CurrentContext = cfg.ConfigContext
				log.Printf("[DEBUG] Using custom current context: %q", overrides.CurrentContext)
			}
			overrides.Context = clientcmdapi.Context{}
			if cfg.ConfigContextAuth != "" {
				overrides.Context.AuthInfo = cfg.ConfigContextAuth
			}
			if cfg.ConfigContextCluster != "" {
				overrides.Context.Cluster = cfg.ConfigContextCluster
			}
			log.Printf("[DEBUG] Using overidden context: %#v", overrides.Context)
		}
	}

	if cfg.Insecure {
		overrides.ClusterInfo.InsecureSkipTLSVerify = true
	}
	if cfg.ClusterCACertificate != "" {
		overrides.ClusterInfo.CertificateAuthorityData = bytes.NewBufferString(cfg.ClusterCACertificate).Bytes()
	}
	if cfg.ClientCertificate != "" {
		overrides.AuthInfo.ClientCertificateData = bytes.NewBufferString(cfg.ClientCertificate).Bytes()
	}
	if cfg.Host != "" {
		// Host has to be the full address (scheme://hostname:port). overrides are
		// processed too late to be taken into account by defaultServerUrlFor(),
		// so we replicate the URL normalisation here. See
		// https://github.com/kubernetes/client-go/blob/v12.0.0/rest/url_utils.go#L85-L87
		hasCA := len(overrides.ClusterInfo.CertificateAuthorityData) != 0
		hasCert := len(overrides.AuthInfo.ClientCertificateData) != 0
		defaultTLS := hasCA || hasCert || overrides.ClusterInfo.InsecureSkipTLSVerify
		host, _, err := restclient.DefaultServerURL(cfg.Host, "", apimachineryschema.GroupVersion{}, defaultTLS)
		if err != nil {
			return nil, fmt.Errorf("failed to parse host: %s", err)
		}
		overrides.ClusterInfo.Server = host.String()
	}
	if cfg.Username != "" {
		overrides.AuthInfo.Username = cfg.Username
	}
	if cfg.Password != "" {
		overrides.AuthInfo.Password = cfg.Password
	}
	if cfg.ClientKey != "" {
		overrides.AuthInfo.ClientKeyData = bytes.NewBufferString(cfg.ClientKey).Bytes()
	}
	if cfg.Token != "" {
		overrides.AuthInfo.Token = cfg.Token
	}
	if cfg.ProxyURL != "" {
		overrides.ClusterDefaults.ProxyURL = cfg.ProxyURL
	}
	if cfg.TLSServerName != "" {
		overrides.ClusterInfo.TLSServerName = cfg.TLSServerName
	}

	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides)
	restCfg, err := cc.ClientConfig()
	if err != nil {
		// Default: surface the underlying clientcmd error so users see the real
		// reason their provider config is unusable. Opt-out: lazy_load = true
		// swallows it so plan can proceed against not-yet-applied bootstraps;
		// the empty restclient.Config falls through and the real failure
		// resurfaces at the first cluster call (#283).
		if cfg.LazyLoad {
			log.Printf("[WARN] lazy_load: swallowing clientcmd error: %s", err)
			return nil, nil
		}
		return nil, fmt.Errorf("invalid provider configuration: %w", err)
	}
	return restCfg, nil
}

// resolveConfigPaths applies the same precedence the SDK v2 schema used
// to encode declaratively: explicit config_path wins, then config_paths,
// then the KUBE_CONFIG_PATHS env var split on the path separator.
func resolveConfigPaths(cfg ProviderConfig) []string {
	if cfg.ConfigPath != "" {
		return []string{cfg.ConfigPath}
	}
	if len(cfg.ConfigPaths) > 0 {
		return append([]string(nil), cfg.ConfigPaths...)
	}
	if v := os.Getenv("KUBE_CONFIG_PATHS"); v != "" {
		return filepath.SplitList(v)
	}
	return nil
}
