package kubernetes

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sresource "k8s.io/cli-runtime/pkg/resource"
	restclient "k8s.io/client-go/rest"
)

func TestKubeProviderToRESTConfigIsolation(t *testing.T) {
	original := restclient.Config{
		Host:    "https://example.invalid",
		APIPath: "/api",
		ContentConfig: restclient.ContentConfig{
			ContentType:  "application/json",
			GroupVersion: &schema.GroupVersion{Version: "v1"},
		},
	}
	provider := &KubeProvider{RestConfig: original}
	first, err := provider.ToRESTConfig()
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.ToRESTConfig()
	if err != nil {
		t.Fatal(err)
	}

	// Match cli-runtime's unstructuredClientForGroupVersion assignments.
	first.ContentConfig = k8sresource.UnstructuredPlusDefaultContentConfig()
	first.GroupVersion = &schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"}
	first.APIPath = "/apis"

	if !reflect.DeepEqual(*second, original) {
		t.Error("REST config changes leaked into another caller")
	}
	if !reflect.DeepEqual(provider.RestConfig, original) {
		t.Error("REST config changes leaked into the provider")
	}

	second.ContentConfig = k8sresource.UnstructuredPlusDefaultContentConfig()
	second.GroupVersion = &schema.GroupVersion{Version: "v1"}
	second.APIPath = "/api"
	if first.APIPath != "/apis" || first.GroupVersion.Group != "rbac.authorization.k8s.io" {
		t.Error("REST config changes leaked back into the first caller")
	}
}

func TestKubeProviderToRESTConfigParallel(t *testing.T) {
	provider := &KubeProvider{RestConfig: restclient.Config{Host: "https://example.invalid"}}
	for _, group := range []string{"", "rbac.authorization.k8s.io"} {
		t.Run(group, func(t *testing.T) {
			t.Parallel()
			for range 100 {
				config, err := provider.ToRESTConfig()
				if err != nil {
					t.Fatal(err)
				}
				config.ContentConfig = k8sresource.UnstructuredPlusDefaultContentConfig()
				config.GroupVersion = &schema.GroupVersion{Group: group, Version: "v1"}
				if group == "" {
					config.APIPath = "/api"
				} else {
					config.APIPath = "/apis"
				}
				if config.GroupVersion.Group != group {
					t.Fatal("REST config changes leaked between concurrent callers")
				}
			}
		})
	}
}
