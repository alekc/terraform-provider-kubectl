package framework

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/alekc/terraform-provider-kubectl/kubernetes"
)

// kubectlFrameworkProvider is the framework-only provider that replaces the
// muxed (SDK v2 + framework) hybrid after Phase F (#297). Provider config
// schema is declared natively here, env-var fallbacks and attribute
// defaults are applied in Configure (the SDK v2 schema's DefaultFunc /
// EnvDefaultFunc do not propagate to the wire protocol, so they have to
// live in code now), and the configured *kubernetes.KubeProvider is
// stored on resp.ResourceData / DataSourceData / EphemeralResourceData
// for each resource type to read directly via type assertion.
type kubectlFrameworkProvider struct {
	version string
}

var (
	_ provider.Provider                       = (*kubectlFrameworkProvider)(nil)
	_ provider.ProviderWithEphemeralResources = (*kubectlFrameworkProvider)(nil)
)

// New constructs the framework provider for the given version. The version
// is interpolated into the User-Agent header that BuildKubeProvider sets
// on every Kubernetes REST call.
func New(version string) provider.Provider {
	return &kubectlFrameworkProvider{version: version}
}

// providerConfigModel mirrors the schema declared in Schema(). Field names
// and types must stay aligned: the framework decodes req.Config into this
// struct, and Configure then turns it into a kubernetes.ProviderConfig.
type providerConfigModel struct {
	ApplyRetryCount      types.Int64  `tfsdk:"apply_retry_count"`
	Host                 types.String `tfsdk:"host"`
	Username             types.String `tfsdk:"username"`
	Password             types.String `tfsdk:"password"`
	Insecure             types.Bool   `tfsdk:"insecure"`
	ClientCertificate    types.String `tfsdk:"client_certificate"`
	ClientKey            types.String `tfsdk:"client_key"`
	ClusterCACertificate types.String `tfsdk:"cluster_ca_certificate"`
	ConfigPaths          types.List   `tfsdk:"config_paths"`
	ConfigPath           types.String `tfsdk:"config_path"`
	ConfigContext        types.String `tfsdk:"config_context"`
	ConfigContextAuth    types.String `tfsdk:"config_context_auth_info"`
	ConfigContextCluster types.String `tfsdk:"config_context_cluster"`
	Token                types.String `tfsdk:"token"`
	ProxyURL             types.String `tfsdk:"proxy_url"`
	LoadConfigFile       types.Bool   `tfsdk:"load_config_file"`
	LazyLoad             types.Bool   `tfsdk:"lazy_load"`
	TLSServerName        types.String `tfsdk:"tls_server_name"`
	Exec                 types.List   `tfsdk:"exec"`
}

type providerExecModel struct {
	APIVersion types.String `tfsdk:"api_version"`
	Command    types.String `tfsdk:"command"`
	Env        types.Map    `tfsdk:"env"`
	Args       types.List   `tfsdk:"args"`
}

// Metadata sets the provider type name and version. Implements
// provider.Provider.
func (p *kubectlFrameworkProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "kubectl"
	resp.Version = p.version
}

// Schema declares the provider configuration block. Wire-level shape is
// preserved exactly from the pre-#297 SDK v2 schema in
// kubernetes/provider.go so existing user configurations work unchanged.
// Defaults (env-var fallbacks, attribute defaults) live in Configure;
// they are not part of the wire schema.
func (p *kubectlFrameworkProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"apply_retry_count": schema.Int64Attribute{
				Optional: true,
				Description: "Number of retries to attempt against the apiserver after the " +
					"initial apply fails. `0` disables retries (single-shot apply). `N >= 1` " +
					"produces up to `N + 1` total attempts with exponential backoff " +
					"(3s initial, 30s max). Can be sourced from " +
					"`KUBECTL_PROVIDER_APPLY_RETRY_COUNT`. Defaults to `1`.",
				Validators: []validator.Int64{int64AtLeastValidator{min: 0}},
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
				Description: "Token to authenticate a service account",
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

// Configure resolves the typed provider config, applies env-var fallbacks
// and attribute defaults, builds the *kubernetes.KubeProvider, and stores
// it on each downstream-data field so resources and data sources read it
// directly via Configure → req.ProviderData. Implements provider.Provider.
func (p *kubectlFrameworkProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	// Deferred actions (Terraform Stacks and other deferral-aware
	// workflows): when the client allows deferral and the provider
	// configuration is not yet fully known (e.g. host / token /
	// cluster_ca_certificate sourced from a not-yet-applied component's
	// outputs), defer rather than build a client from unknown values. The
	// framework then automatically defers every resource and data source
	// for this provider, so Terraform core no longer calls
	// ValidateResourceConfig against an unconfigured provider (#354).
	// This only fires under the experimental deferral client capability;
	// the classic plan/apply flow and the lazy_load fallback are unaffected.
	if req.ClientCapabilities.DeferralAllowed && !req.Config.Raw.IsFullyKnown() {
		resp.Deferred = &provider.Deferred{
			Reason: provider.DeferredReasonProviderConfigUnknown,
		}
		return
	}

	var data providerConfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg, diags := buildProviderConfig(ctx, data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	kp, err := kubernetes.BuildKubeProvider(cfg, p.version)
	if err != nil {
		resp.Diagnostics.AddError("kubectl provider: configure failed", err.Error())
		return
	}

	resp.ResourceData = kp
	resp.DataSourceData = kp
	resp.EphemeralResourceData = kp
}

// buildProviderConfig folds env-var fallbacks and attribute defaults into
// the model decoded from the provider block. Mirrors the EnvDefaultFunc /
// DefaultFunc behaviour the SDK v2 schema used to encode declaratively.
// The result is a fully resolved kubernetes.ProviderConfig ready for
// BuildKubeProvider; any decode diagnostics surface in the returned
// diag.Diagnostics so Configure can append them to its response.
func buildProviderConfig(ctx context.Context, m providerConfigModel) (kubernetes.ProviderConfig, diag.Diagnostics) {
	var diags diag.Diagnostics

	cfg := kubernetes.ProviderConfig{
		ApplyRetryCount:      int64EnvDefault(m.ApplyRetryCount, 1),
		Host:                 stringEnvDefault(m.Host, "KUBE_HOST", ""),
		Username:             stringEnvDefault(m.Username, "KUBE_USER", ""),
		Password:             stringEnvDefault(m.Password, "KUBE_PASSWORD", ""),
		Insecure:             boolEnvDefault(m.Insecure, "KUBE_INSECURE", false),
		ClientCertificate:    stringEnvDefault(m.ClientCertificate, "KUBE_CLIENT_CERT_DATA", ""),
		ClientKey:            stringEnvDefault(m.ClientKey, "KUBE_CLIENT_KEY_DATA", ""),
		ClusterCACertificate: stringEnvDefault(m.ClusterCACertificate, "KUBE_CLUSTER_CA_CERT_DATA", ""),
		ConfigPath:           stringMultiEnvDefault(m.ConfigPath, []string{"KUBE_CONFIG", "KUBECONFIG", "KUBE_CONFIG_PATH"}, "~/.kube/config"),
		ConfigContext:        stringEnvDefault(m.ConfigContext, "KUBE_CTX", ""),
		ConfigContextAuth:    stringEnvDefault(m.ConfigContextAuth, "KUBE_CTX_AUTH_INFO", ""),
		ConfigContextCluster: stringEnvDefault(m.ConfigContextCluster, "KUBE_CTX_CLUSTER", ""),
		Token:                stringEnvDefault(m.Token, "KUBE_TOKEN", ""),
		ProxyURL:             stringEnvDefault(m.ProxyURL, "KUBE_PROXY_URL", ""),
		LoadConfigFile:       boolEnvDefault(m.LoadConfigFile, "KUBE_LOAD_CONFIG_FILE", true),
		LazyLoad:             boolEnvDefault(m.LazyLoad, "KUBE_LAZY_LOAD", false),
		TLSServerName:        stringEnvDefault(m.TLSServerName, "KUBE_TLS_SERVER_NAME", ""),
	}

	if !m.ConfigPaths.IsNull() && !m.ConfigPaths.IsUnknown() {
		var paths []string
		diags.Append(m.ConfigPaths.ElementsAs(ctx, &paths, false)...)
		cfg.ConfigPaths = paths
	}

	if !m.Exec.IsNull() && !m.Exec.IsUnknown() {
		var execs []providerExecModel
		diags.Append(m.Exec.ElementsAs(ctx, &execs, false)...)
		for _, e := range execs {
			ec := kubernetes.ExecConfig{
				APIVersion: e.APIVersion.ValueString(),
				Command:    e.Command.ValueString(),
			}
			if !e.Args.IsNull() && !e.Args.IsUnknown() {
				var args []string
				diags.Append(e.Args.ElementsAs(ctx, &args, false)...)
				ec.Args = args
			}
			if !e.Env.IsNull() && !e.Env.IsUnknown() {
				env := map[string]string{}
				diags.Append(e.Env.ElementsAs(ctx, &env, false)...)
				ec.Env = env
			}
			cfg.Exec = append(cfg.Exec, ec)
		}
	}

	return cfg, diags
}

func stringEnvDefault(v types.String, envKey, defaultValue string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	if envKey != "" {
		if env := os.Getenv(envKey); env != "" {
			return env
		}
	}
	return defaultValue
}

func stringMultiEnvDefault(v types.String, envKeys []string, defaultValue string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	for _, k := range envKeys {
		if env := os.Getenv(k); env != "" {
			return env
		}
	}
	return defaultValue
}

func boolEnvDefault(v types.Bool, envKey string, defaultValue bool) bool {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueBool()
	}
	if envKey != "" {
		if env := os.Getenv(envKey); env != "" {
			if b, err := strconv.ParseBool(env); err == nil {
				return b
			}
		}
	}
	return defaultValue
}

// int64EnvDefault is the int64 analogue used for apply_retry_count. The
// legacy SDK v2 schema only set a literal default of 1; the env override
// (KUBECTL_PROVIDER_APPLY_RETRY_COUNT) is applied later in
// kubernetes.BuildKubeProvider, not here.
func int64EnvDefault(v types.Int64, defaultValue int64) int64 {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueInt64()
	}
	return defaultValue
}

// Resources lists the framework-served resources. Implements
// provider.Provider.
func (p *kubectlFrameworkProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewServerVersionResource,
		NewManifestResource,
	}
}

// DataSources lists the framework-served data sources. Implements
// provider.Provider.
func (p *kubectlFrameworkProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewServerVersionDataSource,
		NewFilenameListDataSource,
		NewFileDocumentsDataSource,
		NewPathDocumentsDataSource,
		NewKustomizeDocumentsDataSource,
		NewManifestDataSource,
	}
}

// EphemeralResources lists the framework-served ephemeral resources.
// Implements provider.ProviderWithEphemeralResources.
func (p *kubectlFrameworkProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewManifestEphemeralResource,
	}
}

// int64AtLeastValidator is a small inline validator that rejects values
// below a configured minimum. Mirrors the existing stringOneOfValidator
// pattern in resource_kubectl_manifest.go: kept inline to avoid pulling
// in terraform-plugin-framework-validators for a single call site. If a
// second int64 validator lands, swap to that module wholesale.
type int64AtLeastValidator struct {
	min int64
}

// Description returns a one-line plaintext summary. Implements validator.Int64.
func (v int64AtLeastValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be at least %d", v.min)
}

// MarkdownDescription returns the same summary as Description; no Markdown
// is needed for a numeric bound. Implements validator.Int64.
func (v int64AtLeastValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateInt64 rejects any non-null, non-unknown value below the minimum.
// Null and unknown pass through so the validator composes cleanly with
// Optional attributes whose value may not be set in the config. Implements
// validator.Int64.
func (v int64AtLeastValidator) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	got := req.ConfigValue.ValueInt64()
	if got < v.min {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Attribute Value",
			fmt.Sprintf("Attribute %s value must be at least %d, got: %d.", req.Path, v.min, got),
		)
	}
}
