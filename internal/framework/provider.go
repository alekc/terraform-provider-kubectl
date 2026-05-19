package framework

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// kubectlFrameworkProvider is the plugin-framework half of the muxed provider.
// All provider configuration (kubeconfig, auth, etc.) is handled by the SDK v2
// half — this provider has no behaviour of its own and receives the configured
// SDK v2 meta via the SDKv2Meta callback. The Configure pass simply forwards
// that callback down to each resource (currently just the manifest ephemeral
// resource).
//
// The Schema() method below MUST stay byte-for-byte identical to the SDK v2
// provider's Schema in `kubernetes/provider.go`. The muxer
// (`tf6muxserver.NewMuxServer`) compares the provider configuration schema
// returned by every muxed server and refuses to start if they differ — that
// regression is tracked by issue #275 and pinned by the unit test in
// `internal/mux/mux_test.go`. If you add, rename, or change the description
// of any field on either side, update both.
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
	// Mirror of the SDK v2 schema in `kubernetes/provider.go`. Keep field
	// names, types, optional/required flags, and descriptions identical on
	// both sides; the muxer enforces byte-for-byte parity. Defaults
	// (DefaultFunc / EnvDefaultFunc) live on the SDK v2 side only — they do
	// not propagate to the wire schema, so they aren't mirrored here.
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"apply_retry_count": schema.Int64Attribute{
				Optional:    true,
				Description: "Defines the number of attempts any create/update action will take",
			},
			"host": schema.StringAttribute{
				Optional:    true,
				Description: "The hostname (in form of URI) of Kubernetes master.",
			},
			"username": schema.StringAttribute{
				Optional:    true,
				Description: "The username to use for HTTP basic authentication when accessing the Kubernetes master endpoint.",
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Description: "The password to use for HTTP basic authentication when accessing the Kubernetes master endpoint.",
			},
			"insecure": schema.BoolAttribute{
				Optional:    true,
				Description: "Whether server should be accessed without verifying the TLS certificate.",
			},
			"client_certificate": schema.StringAttribute{
				Optional:    true,
				Description: "PEM-encoded client certificate for TLS authentication.",
			},
			"client_key": schema.StringAttribute{
				Optional:    true,
				Description: "PEM-encoded client certificate key for TLS authentication.",
			},
			"cluster_ca_certificate": schema.StringAttribute{
				Optional:    true,
				Description: "PEM-encoded root certificates bundle for TLS authentication.",
			},
			"config_paths": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "A list of paths to kube config files. Can be set with KUBE_CONFIG_PATHS environment variable.",
			},
			"config_path": schema.StringAttribute{
				Optional:    true,
				Description: "Path to the kube config file, defaults to ~/.kube/config",
			},
			"config_context": schema.StringAttribute{
				Optional: true,
			},
			"config_context_auth_info": schema.StringAttribute{
				Optional: true,
			},
			"config_context_cluster": schema.StringAttribute{
				Optional: true,
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Description: "Token to authentifcate an service account",
			},
			"proxy_url": schema.StringAttribute{
				Optional:    true,
				Description: "URL to the proxy to be used for all API requests",
			},
			"load_config_file": schema.BoolAttribute{
				Optional:    true,
				Description: "Load local kubeconfig.",
			},
			"lazy_load": schema.BoolAttribute{
				Optional:    true,
				Description: "When true, kubeconfig resolution errors at provider-configure time are swallowed and the actual client is built lazily on first use. Lets `terraform plan` succeed when provider arguments (host, token, certs) are sourced from outputs of resources that have not been applied yet. Off by default; see Troubleshooting in the provider docs for trade-offs.",
			},
			"tls_server_name": schema.StringAttribute{
				Optional:    true,
				Description: "Server name passed to the server for SNI and is used in the client to check server certificates against.",
			},
		},
		Blocks: map[string]schema.Block{
			"exec": schema.ListNestedBlock{
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"api_version": schema.StringAttribute{
							Required: true,
						},
						"command": schema.StringAttribute{
							Required: true,
						},
						"env": schema.MapAttribute{
							Optional:    true,
							ElementType: types.StringType,
						},
						"args": schema.ListAttribute{
							Optional:    true,
							ElementType: types.StringType,
						},
					},
				},
			},
		},
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
