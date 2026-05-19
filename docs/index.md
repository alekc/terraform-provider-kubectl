# Kubectl Provider

This provider is the best way of managing Kubernetes resources in Terraform, by allowing you to use the thing 
Kubernetes loves best - yaml!

This core of this provider is the `kubectl_manifest` resource, allowing free-form yaml to be processed and applied against Kubernetes.
This yaml object is then tracked and handles creation, updates and deleted seamlessly - including drift detection!

A set of helpful data resources to process directories of yaml files and inline templating is available.

This `terraform-provider-kubectl` provider has been originally forked from `gavinbunney/kubectl` and followed a separate development path from the version 0.14.

## What's in this provider

| Type               | Name                                                                       | Purpose                                                                                              |
| ------------------ | -------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Resource           | [`kubectl_manifest`](./resources/kubectl_manifest.md)                      | Apply a raw YAML manifest to the cluster (full create / update / delete + drift detection).          |
| Resource           | [`kubectl_server_version`](./resources/kubectl_server_version.md)          | Read API-server version info, with `triggers` for use in `depends_on` chains.                        |
| Data source        | [`kubectl_manifest`](./data-sources/kubectl_manifest.md)                   | Read any object from the cluster by GVK + name (+ namespace) and extract fields by dot-path.         |
| Data source        | [`kubectl_server_version`](./data-sources/kubectl_server_version.md)       | Read API-server version info.                                                                        |
| Data source        | [`kubectl_file_documents`](./data-sources/kubectl_file_documents.md)       | Split a multi-document YAML string into individual documents.                                        |
| Data source        | [`kubectl_filename_list`](./data-sources/kubectl_filename_list.md)         | Glob a directory for YAML files.                                                                     |
| Data source        | [`kubectl_path_documents`](./data-sources/kubectl_path_documents.md)       | Glob a directory and split every matched file into individual documents.                             |
| Ephemeral resource | [`kubectl_manifest`](./ephemeral-resources/kubectl_manifest.md)            | Same shape as the data source, but the fetched value is **never written to state**. Terraform 1.10+. |

## Installation

### Terraform 0.13+

The provider can be installed and managed automatically by Terraform. Sample `versions.tf` file :

```hcl
terraform {
  required_version = ">= 0.13"

  required_providers {
    kubectl = {
      source  = "alekc/kubectl"
      version = ">= 2.0.0"
    }
  }
}
```

#### Install manually

If you don't want to use the one-liner above, you can download a binary for your system from the [release page](https://github.com/alekc/terraform-provider-kubectl/releases), 
then either place it at the root of your Terraform folder or in the Terraform plugin folder on your system. 

## Configuration

The provider supports the same configuration parameters as the [Kubernetes Terraform Provider](https://www.terraform.io/docs/providers/kubernetes/index.html),
with the addition of `load_config_file` and `apply_retry_count`.

> Note: Unlike the Terraform Kubernetes Provider, this provider will load the `KUBECONFIG` file if the environment variable is set.

```hcl
provider "kubectl" {
  host                   = var.eks_cluster_endpoint
  cluster_ca_certificate = base64decode(var.eks_cluster_ca)
  token                  = data.aws_eks_cluster_auth.main.token
  load_config_file       = false
}
```

### Argument Reference

The following arguments are supported:

* `apply_retry_count` - (Optional) Defines the number of attempts any create/update action will take. Default `1`.
* `load_config_file` - (Optional) Flag to enable/disable loading of the local kubeconf file. Default `true`. Can be sourced from `KUBE_LOAD_CONFIG_FILE`.
* `lazy_load` - (Optional) When `true`, kubeconfig resolution errors at provider-configure time are swallowed and the actual client is built lazily on first use. Lets `terraform plan` succeed when provider arguments (`host`, `token`, certs) are sourced from outputs of resources that have not been applied yet. Off by default; can be sourced from `KUBE_LAZY_LOAD`. See [Troubleshooting](#troubleshooting) for trade-offs.
* `host` - (Optional) The hostname (in form of URI) of the Kubernetes API. Can be sourced from `KUBE_HOST`.
* `username` - (Optional) The username to use for HTTP basic authentication when accessing the Kubernetes API. Can be sourced from `KUBE_USER`.
* `password` - (Optional) The password to use for HTTP basic authentication when accessing the Kubernetes API. Can be sourced from `KUBE_PASSWORD`.
* `insecure` - (Optional) Whether the server should be accessed without verifying the TLS certificate. Can be sourced from `KUBE_INSECURE`. Defaults to `false`.
* `client_certificate` - (Optional) PEM-encoded client certificate for TLS authentication. Can be sourced from `KUBE_CLIENT_CERT_DATA`.
* `client_key` - (Optional) PEM-encoded client certificate key for TLS authentication. Can be sourced from `KUBE_CLIENT_KEY_DATA`.
* `cluster_ca_certificate` - (Optional) PEM-encoded root certificates bundle for TLS authentication. Can be sourced from `KUBE_CLUSTER_CA_CERT_DATA`.
* `config_path` - (Optional) A path to a kube config file. Can be sourced from `KUBE_CONFIG_PATH` or `KUBECONFIG`.
* `config_paths` - (Optional) A list of paths to the kube config files. Can be sourced from `KUBE_CONFIG_PATHS`.
* `config_context` - (Optional) Context to choose from the config file. Can be sourced from `KUBE_CTX`.
* `config_context_auth_info` - (Optional) Authentication info context of the kube config (name of the kubeconfig user, `--user` flag in `kubectl`). Can be sourced from `KUBE_CTX_AUTH_INFO`.
* `config_context_cluster` - (Optional) Cluster context of the kube config (name of the kubeconfig cluster, `--cluster` flag in `kubectl`). Can be sourced from `KUBE_CTX_CLUSTER`.
* `token` - (Optional) Token of your service account.  Can be sourced from `KUBE_TOKEN`.
* `proxy_url` - (Optional) URL to the proxy to be used for all API requests. URLs with "http", "https", and "socks5" schemes are supported. Can be sourced from `KUBE_PROXY_URL`.
* `tls_server_name` - (Optional) Server name passed to the server for SNI and is used in the client to check server certificates against.
* `exec` - (Optional) Configuration block to use an [exec-based credential plugin] (https://kubernetes.io/docs/reference/access-authn-authz/authentication/#client-go-credential-plugins), e.g. call an external command to receive user credentials.
    * `api_version` - (Required) API version to use when decoding the ExecCredentials resource, e.g. `client.authentication.k8s.io/v1beta1`.
    * `command` - (Required) Command to execute.
    * `args` - (Optional) List of arguments to pass when executing the plugin.
    * `env` - (Optional) Map of environment variables to set when executing the plugin.

### Exec Plugin Support

As with the Kubernetes Terraform Provider, this provider also supports using a `exec` based plugin (for example when running on EKS).

```hcl
provider "kubectl" {
  apply_retry_count      = 15
  host                   = var.eks_cluster_endpoint
  cluster_ca_certificate = base64decode(var.eks_cluster_ca)
  load_config_file       = false

  exec {
    api_version = "client.authentication.k8s.io/v1alpha1"
    command     = "aws-iam-authenticator"
    args = [
      "token",
      "-i",
      module.eks.cluster_id,
    ]
  }
}
```

### Retry Support

The provider has an additional parameter `apply_retry_count` that allows kubernetes commands to be retried on failure.
This is useful if you have flaky CRDs or network connections and need to wait for the cluster state to be back in quorum.
This applies to both create and update operations. 

```hcl
provider "kubectl" {
  apply_retry_count = 15
}
```

## Troubleshooting

### `Failed to get RESTMapper client / cannot create discovery client: no client config`

This error means the provider configuration produced an empty REST config —
`host`, `token`, `cluster_ca_certificate`, `exec`, or a kubeconfig path either
wasn't supplied or wasn't resolvable when Terraform configured the provider.
The provider now reports the underlying `clientcmd` reason (missing host,
malformed cert, unresolved variable, …) directly in the diagnostic, so check
the full error text first.

The most common trigger is **deferred evaluation of provider arguments**:
Terraform configures providers up front, before any resources have been
applied. If `host`/`token`/etc. come from outputs of resources that haven't
been applied yet (or from a sibling module that errored during plan), those
values are unknown at provider-configure time and the provider falls back to
an empty config. The same pitfall affects `hashicorp/kubernetes` —
see Terraform's
[providers documentation](https://developer.hashicorp.com/terraform/language/providers/configuration#provider-versions)
for the broader pattern.

Workarounds, in order of preference:

1. **Two-stage apply** — apply the cluster (or whatever owns the credentials)
   in one root module, then apply the manifests in a separate root module
   that reads the cluster outputs via `terraform_remote_state` or a data
   source.
2. **Pin the credentials to a stable source** — e.g. `data
   "aws_eks_cluster"` / `data "google_container_cluster"` instead of the
   resource attribute, since data sources are re-read on every plan and
   don't carry the "unknown until apply" status that resource outputs do.
3. **Smoke-test with literal values** — replace `var.host` /
   `var.cluster_ca_certificate` etc. with hardcoded strings briefly to
   confirm the rest of the config is correct; if that succeeds the failure
   is the deferred-evaluation pattern above.
4. **Set `lazy_load = true`** to opt back into the pre-`v2.3.0` behaviour:
   `clientcmd` errors at provider-configure time are swallowed and the
   actual client is built lazily on first use. This trades the precise
   diagnostic above for a less specific late error if the configuration
   is genuinely broken, so prefer one of the other workarounds when they
   fit. Use this when the same-root cluster-plus-manifests pattern is
   the only shape that fits the workflow.

   ```hcl
   provider "kubectl" {
     host                   = module.eks.cluster_endpoint
     cluster_ca_certificate = base64decode(module.eks.cluster_ca_certificate)
     token                  = data.aws_eks_cluster_auth.this.token
     load_config_file       = false
     lazy_load              = true
   }
   ```

## Trying an unreleased version

If a fix you need is already on `master` but no release has been cut yet,
you can build the provider locally and point Terraform at the binary with a
[`dev_overrides`](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers)
block. This is the same mechanism the project's own CI smoke job uses, and
is the recommended way to test a regression fix before it ships to the
Terraform Registry.

### Prerequisites

- [Go](https://go.dev/dl/) at the version pinned in
  [`go.mod`](https://github.com/alekc/terraform-provider-kubectl/blob/master/go.mod)
  (currently 1.26 or newer)
- `$(go env GOPATH)/bin` on your `$PATH`
- Terraform 0.14+ (`dev_overrides` was added in 0.14)

### Build

```sh
git clone https://github.com/alekc/terraform-provider-kubectl
cd terraform-provider-kubectl
git checkout master           # or any branch / tag / commit you want to test
make build                    # places the binary at $(go env GOPATH)/bin/terraform-provider-kubectl
```

Alternatively, `go install` from the repo root produces the same binary.
The binary name must stay `terraform-provider-kubectl` for Terraform to
pick it up.

### Tell Terraform about the local binary

Create or edit `~/.terraformrc` (or `%APPDATA%\terraform.rc` on Windows)
and add a `dev_overrides` block pointing at the directory that holds the
binary, not the binary itself:

```hcl
provider_installation {
  dev_overrides {
    "alekc/kubectl" = "/path/to/your/gopath/bin"
  }
  direct {}
}
```

Run `go env GOPATH` to fill in the path if you are not sure. The `direct {}`
line keeps the default registry lookup for every other provider in your
config so they still resolve normally.

### Use it

In your Terraform configuration, keep the `required_providers` block
unchanged (the source still points at `alekc/kubectl`):

```hcl
terraform {
  required_providers {
    kubectl = {
      source  = "alekc/kubectl"
      version = ">= 2.0.0"
    }
  }
}
```

`terraform init` becomes a no-op under `dev_overrides`. From the next
`terraform plan` or `terraform apply` onward you will see Terraform print
the override warning every run:

```text
│ Warning: Provider development overrides are in effect
│
│ The following provider development overrides are set in the CLI
│ configuration:
│  - alekc/kubectl in /path/to/your/gopath/bin
```

That warning is the signal that your custom build is in use. It is
expected and cannot be suppressed.

### Caveats

- `dev_overrides` skips the registry checksum check and ignores the
  `version` constraint in `required_providers`. Anything in the override
  directory wins, even if it is older or newer than the constraint says.
- Other team members on the same Terraform configuration will keep using
  the released version unless they also configure a `dev_overrides`
  block. Do not commit `~/.terraformrc`.
- Remove the `dev_overrides` block (or comment it out) once a real
  release containing the fix is out, otherwise Terraform will keep using
  your stale local binary.

### Run the tests

If you want to verify your build against the provider's own suite before
trusting it:

```sh
make test       # unit tests, no cluster required
make testacc    # acceptance tests against a live cluster (KUBECONFIG / KUBE_CONFIG_PATH)
```

Acceptance tests create real Kubernetes objects. Point them at a throw-away
cluster (`kind`, `k3d`, Docker Desktop, …) rather than anything you care
about.

## Example

Loading a raw yaml manifest into kubernetes is simple, just set the `yaml_body` argument:

```hcl
provider "kubectl" {
  host                   = var.eks_cluster_endpoint
  cluster_ca_certificate = base64decode(var.eks_cluster_ca)
  token                  = data.aws_eks_cluster_auth.main.token
  load_config_file       = false
}

resource "kubectl_manifest" "test" {
    yaml_body = <<YAML
apiVersion: couchbase.com/v1
kind: CouchbaseCluster
metadata:
  name: name-here-cluster
spec:
  baseImage: name-here-image
  version: name-here-image-version
  authSecret: name-here-operator-secret-name
  exposeAdminConsole: true
  adminConsoleServices:
    - data
  cluster:
    dataServiceMemoryQuota: 256
    indexServiceMemoryQuota: 256
    searchServiceMemoryQuota: 256
    eventingServiceMemoryQuota: 256
    analyticsServiceMemoryQuota: 1024
    indexStorageSetting: memory_optimized
    autoFailoverTimeout: 120
    autoFailoverMaxCount: 3
    autoFailoverOnDataDiskIssues: true
    autoFailoverOnDataDiskIssuesTimePeriod: 120
    autoFailoverServerGroup: false
YAML
}
```

Updates are also preserved, so you can fully manage your kubernetes resources with ease!
