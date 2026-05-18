package framework

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// kubectlFrameworkProvider is the plugin-framework half of the muxed provider.
// All provider configuration (kubeconfig, auth, etc.) is handled by the SDK v2
// half — this provider has no schema of its own and receives the configured
// SDK v2 meta via the SDKv2Meta callback. The Configure pass simply forwards
// that callback down to each resource (currently just the manifest ephemeral
// resource).
type kubectlFrameworkProvider struct {
	version   string
	SDKv2Meta func() any
}

var (
	_ provider.Provider                       = (*kubectlFrameworkProvider)(nil)
	_ provider.ProviderWithEphemeralResources = (*kubectlFrameworkProvider)(nil)
)

// New constructs the framework provider. The SDKv2Meta callback is the
// `Meta` method on the SDK v2 *schema.Provider — it returns the configured
// `*kubernetes.KubeProvider` once the SDK v2 provider has run its
// ConfigureContextFunc.
func New(version string, sdkv2Meta func() any) provider.Provider {
	return &kubectlFrameworkProvider{
		version:   version,
		SDKv2Meta: sdkv2Meta,
	}
}

func (p *kubectlFrameworkProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "kubectl"
	resp.Version = p.version
}

func (p *kubectlFrameworkProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{},
	}
}

func (p *kubectlFrameworkProvider) Configure(_ context.Context, _ provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	// SDK v2 owns provider configuration. Hand the SDKv2Meta callback to
	// resources via ResourceData / EphemeralResourceData so they can pull the
	// configured *kubernetes.KubeProvider at Open() time.
	resp.ResourceData = p.SDKv2Meta
	resp.EphemeralResourceData = p.SDKv2Meta
	resp.DataSourceData = p.SDKv2Meta
}

func (p *kubectlFrameworkProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{}
}

func (p *kubectlFrameworkProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func (p *kubectlFrameworkProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewManifestEphemeralResource,
	}
}
