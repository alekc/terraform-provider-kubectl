package kubernetes

import (
	"path/filepath"
	"strings"
	"testing"

	restclient "k8s.io/client-go/rest"
)

// TestBuildKubeProvider_ApplyRetryCountIsPerProvider pins the fix for issue
// #265: apply_retry_count was held in a package-level global so the last call
// to providerConfigure silently overwrote the value seen by every other
// aliased provider. Each call must populate its own *KubeProvider with its
// configured value.
func TestBuildKubeProvider_ApplyRetryCountIsPerProvider(t *testing.T) {
	t.Setenv("KUBECTL_PROVIDER_APPLY_RETRY_COUNT", "")

	cfg := func(retry int64) ProviderConfig {
		return ProviderConfig{
			ApplyRetryCount: retry,
			LoadConfigFile:  false,
			Host:            "http://example.invalid",
		}
	}

	a, err := BuildKubeProvider(cfg(1), "test")
	if err != nil {
		t.Fatalf("provider A: %v", err)
	}
	b, err := BuildKubeProvider(cfg(42), "test")
	if err != nil {
		t.Fatalf("provider B: %v", err)
	}

	if a.ApplyRetryCount != 1 {
		t.Errorf("provider A: ApplyRetryCount = %d, want 1", a.ApplyRetryCount)
	}
	if b.ApplyRetryCount != 42 {
		t.Errorf("provider B: ApplyRetryCount = %d, want 42 (was clobbered by provider B)", b.ApplyRetryCount)
	}
}

// TestBuildKubeProvider_ApplyRetryCountEnvOverride verifies the env var
// still wins over the schema value and that the override is captured
// per-call rather than leaking globally.
func TestBuildKubeProvider_ApplyRetryCountEnvOverride(t *testing.T) {
	t.Setenv("KUBECTL_PROVIDER_APPLY_RETRY_COUNT", "7")

	cfg := ProviderConfig{
		ApplyRetryCount: 1,
		LoadConfigFile:  false,
		Host:            "http://example.invalid",
	}
	p, err := BuildKubeProvider(cfg, "test")
	if err != nil {
		t.Fatalf("BuildKubeProvider: %v", err)
	}
	if p.ApplyRetryCount != 7 {
		t.Errorf("env override: ApplyRetryCount = %d, want 7", p.ApplyRetryCount)
	}
}

// TestBuildRestConfig_LazyLoadSwallowsClientcmdError pins the fix for
// issue #283. With lazy_load disabled (the default) buildRestConfig must
// surface the clientcmd error so users see the real reason their
// provider config is unusable. With lazy_load enabled the same call must
// return (nil, nil) so BuildKubeProvider substitutes &restclient.Config{}
// and lets `terraform plan` succeed while provider arguments are still
// unresolved.
//
// The fixture isolates clientcmd from the developer's real environment
// by pointing HOME at a fresh empty directory and clearing every KUBE_*
// env var; without that, a kubeconfig on the dev machine could mask the
// "no configuration provided" path that lazy_load is meant to swallow.
func TestBuildRestConfig_LazyLoadSwallowsClientcmdError(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("USERPROFILE", emptyHome)
	t.Setenv("KUBECONFIG", filepath.Join(emptyHome, "missing"))
	t.Setenv("KUBE_CONFIG", "")
	t.Setenv("KUBE_CONFIG_PATH", "")
	t.Setenv("KUBE_CONFIG_PATHS", "")
	t.Setenv("KUBE_HOST", "")
	t.Setenv("KUBE_USER", "")
	t.Setenv("KUBE_PASSWORD", "")
	t.Setenv("KUBE_TOKEN", "")
	t.Setenv("KUBE_CLIENT_CERT_DATA", "")
	t.Setenv("KUBE_CLIENT_KEY_DATA", "")
	t.Setenv("KUBE_CLUSTER_CA_CERT_DATA", "")
	t.Setenv("KUBERNETES_MASTER", "")

	t.Run("default surfaces error", func(t *testing.T) {
		_, err := buildRestConfig(ProviderConfig{LoadConfigFile: false, LazyLoad: false})
		if err == nil {
			t.Fatalf("expected clientcmd error, got nil")
		}
		if !strings.Contains(err.Error(), "invalid provider configuration") {
			t.Errorf("expected wrapped 'invalid provider configuration' diagnostic, got: %s", err)
		}
	})

	t.Run("lazy_load swallows error", func(t *testing.T) {
		cfg, err := buildRestConfig(ProviderConfig{LoadConfigFile: false, LazyLoad: true})
		if err != nil {
			t.Fatalf("expected lazy_load to swallow the clientcmd error, got: %s", err)
		}
		if cfg != nil {
			t.Errorf("expected nil cfg so BuildKubeProvider falls back to empty restclient.Config, got: %+v", cfg)
		}
	})
}

// TestBuildRestConfig_RejectsMultipleExecBlocks pins the runtime
// enforcement of the schema's old MaxItems = 1 constraint on the `exec`
// block. The framework half cannot express MaxItems at the protocol
// level (issue #275), so the check lives here.
func TestBuildRestConfig_RejectsMultipleExecBlocks(t *testing.T) {
	_, err := buildRestConfig(ProviderConfig{
		Exec: []ExecConfig{
			{APIVersion: "client.authentication.k8s.io/v1", Command: "first"},
			{APIVersion: "client.authentication.k8s.io/v1", Command: "second"},
		},
	})
	if err == nil {
		t.Fatalf("expected an error for two exec blocks")
	}
	if !strings.Contains(err.Error(), "at most one exec block") {
		t.Errorf("unexpected diagnostic: %s", err)
	}
}

// TestResolveConfigPaths_PrecedenceMirrorsSDKv2 mirrors the SDK v2 schema's
// declarative precedence (config_path > config_paths > KUBE_CONFIG_PATHS).
// The lookup is now explicit Go code, so the precedence has to stay
// pinned by a test.
func TestResolveConfigPaths_PrecedenceMirrorsSDKv2(t *testing.T) {
	t.Setenv("KUBE_CONFIG_PATHS", "/env/a"+string(filepath.ListSeparator)+"/env/b")

	cases := []struct {
		name string
		cfg  ProviderConfig
		want []string
	}{
		{
			"explicit config_path wins",
			ProviderConfig{ConfigPath: "/explicit", ConfigPaths: []string{"/list/a", "/list/b"}},
			[]string{"/explicit"},
		},
		{
			"config_paths beats env when no explicit path",
			ProviderConfig{ConfigPaths: []string{"/list/a", "/list/b"}},
			[]string{"/list/a", "/list/b"},
		},
		{
			"env wins when neither attribute set",
			ProviderConfig{},
			[]string{"/env/a", "/env/b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveConfigPaths(tc.cfg)
			if !equalSlices(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestKubeProvider_RESTMapperAcceptsEmptyConfig pins the lazy_load (#283)
// contract end-to-end through the RESTClientGetter surface. When
// buildRestConfig returns (nil, nil) under lazy_load = true, BuildKubeProvider
// substitutes an empty &restclient.Config{} and proceeds. Downstream callers
// then go through ToRESTMapper, which in turn calls ToDiscoveryClient.
// Both must succeed at construction time so plan can run; the actual
// empty-Host failure only resurfaces when the mapper or discovery client
// is used (which is what lazy_load wants: defer the failure to apply
// time).
//
// Without this test, a refactor that tightens ToRESTMapper's error
// handling (e.g., propagating any ToDiscoveryClient error eagerly)
// could silently regress the lazy_load contract. The test demonstrates
// that the contract holds against an explicit empty config.
func TestKubeProvider_RESTMapperAcceptsEmptyConfig(t *testing.T) {
	kp := &KubeProvider{RestConfig: restclient.Config{}}

	t.Run("ToDiscoveryClient", func(t *testing.T) {
		client, err := kp.ToDiscoveryClient()
		if err != nil {
			t.Fatalf("expected nil error on empty config, got: %v", err)
		}
		if client == nil {
			t.Fatalf("expected a non-nil discovery client on empty config")
		}
	})

	t.Run("ToRESTMapper", func(t *testing.T) {
		mapper, err := kp.ToRESTMapper()
		if err != nil {
			t.Fatalf("expected nil error on empty config, got: %v", err)
		}
		if mapper == nil {
			t.Fatalf("expected a non-nil REST mapper on empty config")
		}
	})
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
