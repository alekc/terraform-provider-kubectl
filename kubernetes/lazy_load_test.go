package kubernetes

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// TestLazyLoadSwallowsClientcmdError pins the fix for issue #283.
//
// With lazy_load disabled (the default), initializeConfiguration must
// surface the clientcmd error so users see the real reason their provider
// config is unusable. With lazy_load enabled, the same call must return
// (nil, nil) so providerConfigure can substitute &restclient.Config{} and
// let `terraform plan` succeed while provider arguments are still
// unresolved.
//
// The fixture isolates clientcmd from the developer's real environment by
// pointing HOME at a fresh empty directory and clearing every KUBE_* env
// var; without that, a kubeconfig on the dev machine could mask the
// "no configuration provided" path that lazy_load is meant to swallow.
func TestLazyLoadSwallowsClientcmdError(t *testing.T) {
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

	provider := Provider()

	t.Run("default surfaces error", func(t *testing.T) {
		d := schema.TestResourceDataRaw(t, provider.Schema, map[string]interface{}{
			"load_config_file": false,
			"lazy_load":        false,
		})
		cfg, err := initializeConfiguration(d)
		if err == nil {
			t.Fatalf("expected clientcmd error, got cfg=%v err=nil", cfg)
		}
		if !strings.Contains(err.Error(), "invalid provider configuration") {
			t.Errorf("expected wrapped 'invalid provider configuration' diagnostic, got: %s", err)
		}
	})

	t.Run("lazy_load swallows error", func(t *testing.T) {
		d := schema.TestResourceDataRaw(t, provider.Schema, map[string]interface{}{
			"load_config_file": false,
			"lazy_load":        true,
		})
		cfg, err := initializeConfiguration(d)
		if err != nil {
			t.Fatalf("expected lazy_load to swallow the clientcmd error, got: %s", err)
		}
		if cfg != nil {
			t.Errorf("expected nil cfg so providerConfigure falls back to empty restclient.Config, got: %+v", cfg)
		}
	})
}

// TestLazyLoadEnvDefault confirms the KUBE_LAZY_LOAD env var feeds the
// schema default and is picked up by initializeConfiguration without an
// explicit value in the provider block.
func TestLazyLoadEnvDefault(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("USERPROFILE", emptyHome)
	t.Setenv("KUBECONFIG", filepath.Join(emptyHome, "missing"))
	t.Setenv("KUBE_LAZY_LOAD", "true")
	t.Setenv("KUBE_CONFIG", "")
	t.Setenv("KUBE_CONFIG_PATH", "")
	t.Setenv("KUBE_CONFIG_PATHS", "")
	t.Setenv("KUBE_HOST", "")
	t.Setenv("KUBE_TOKEN", "")
	t.Setenv("KUBE_CLIENT_CERT_DATA", "")
	t.Setenv("KUBE_CLIENT_KEY_DATA", "")
	t.Setenv("KUBE_CLUSTER_CA_CERT_DATA", "")
	t.Setenv("KUBERNETES_MASTER", "")

	provider := Provider()
	d := schema.TestResourceDataRaw(t, provider.Schema, map[string]interface{}{
		"load_config_file": false,
	})

	// Sanity-check the schema picked up the env default.
	if got := d.Get("lazy_load").(bool); !got {
		t.Fatalf("KUBE_LAZY_LOAD=true should default lazy_load to true, got %v", got)
	}

	cfg, err := initializeConfiguration(d)
	if err != nil {
		t.Fatalf("expected env-default lazy_load to swallow the clientcmd error, got: %s", err)
	}
	if cfg != nil {
		t.Errorf("expected nil cfg, got: %+v", cfg)
	}
}
