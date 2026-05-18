# Kubernetes "kubectl" Provider 

![Build Status](https://github.com/alekc/terraform-provider-kubectl/actions/workflows/tests.yaml/badge.svg) [![user guide](https://img.shields.io/badge/-user%20guide-blue)](https://registry.terraform.io/providers/alekc/kubectl)

This provider offers the most effective method for handling Kubernetes resources in Terraform. It empowers you to leverage what Kubernetes values most — YAML.

At the heart of this provider lies the `kubectl_manifest` resource, enabling the processing and application of free-form YAML directly to Kubernetes. This YAML object is monitored across its full lifecycle — creation, updates, drift detection and deletion.

The `terraform-provider-kubectl` has gained widespread adoption in numerous large Kubernetes installations, serving as the primary tool for orchestrating the complete lifecycle of Kubernetes resources.

## What's in this provider

| Type               | Name                                                                                 | Purpose                                                                                              |
| ------------------ | ------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| Resource           | [`kubectl_manifest`](./docs/resources/kubectl_manifest.md)                           | Apply a raw YAML manifest to the cluster (full create / update / delete + drift detection).          |
| Resource           | [`kubectl_server_version`](./docs/resources/kubectl_server_version.md)               | Read API-server version info, with `triggers` for use in `depends_on` chains.                        |
| Data source        | [`kubectl_manifest`](./docs/data-sources/kubectl_manifest.md)                        | Read any object from the cluster by GVK + name (+ namespace) and extract fields by dot-path.         |
| Data source        | [`kubectl_server_version`](./docs/data-sources/kubectl_server_version.md)            | Read API-server version info.                                                                        |
| Data source        | [`kubectl_file_documents`](./docs/data-sources/kubectl_file_documents.md)            | Split a multi-document YAML string into individual documents.                                        |
| Data source        | [`kubectl_filename_list`](./docs/data-sources/kubectl_filename_list.md)              | Glob a directory for YAML files.                                                                     |
| Data source        | [`kubectl_path_documents`](./docs/data-sources/kubectl_path_documents.md)            | Glob a directory and split every matched file into individual documents.                             |
| Ephemeral resource | [`kubectl_manifest`](./docs/ephemeral-resources/kubectl_manifest.md)                 | Same shape as the data source, but the fetched value is **never written to state**. Terraform 1.10+. |

## Supported Kubernetes and Terraform versions

Every PR is exercised against the matrix below on `kind`. The matrix is regenerated from [endoflife.date](https://endoflife.date) on each CI run, so it tracks the four most recent active Kubernetes release cycles and the four most recent stable Terraform minors, plus a legacy `1.5.7` cell (the last MPL-licensed Terraform release).

|                 | Terraform 1.15 | Terraform 1.14 | Terraform 1.13 | Terraform 1.12 | Terraform 1.5.7 |
| --------------- | :------------: | :------------: | :------------: | :------------: | :-------------: |
| Kubernetes 1.36 | smoke + ✅      | ✅              | ✅              | ✅              | ✅               |
| Kubernetes 1.35 | ✅              | ✅              | ✅              | ✅              | ✅               |
| Kubernetes 1.34 | ✅              | ✅              | ✅              | ✅              | ✅               |
| Kubernetes 1.33 | ✅              | ✅              | ✅              | ✅              | ✅               |

The versions in the table are the snapshot resolved at the time of writing; the live matrix moves with the upstream release cadence. The newest pair (latest Kubernetes × latest Terraform) is run first as a single **smoke** job; the remaining 19 combinations fan out only after smoke passes. Combinations outside this grid may still work — your mileage may vary.

## Installation

### Terraform 0.13+

The provider can be installed and managed automatically by Terraform. Sample `versions.tf` file:

```hcl
terraform {
  required_version = ">= 0.13"

  required_providers {
    kubectl = {
      source  = "alekc/kubectl"
      version = "~> 2.0"
    }
  }
}
```

### Install manually

If you don't want to use the one-liner above, you can download a binary for your system from the [release page](https://github.com/alekc/terraform-provider-kubectl/releases), then either place it at the root of your Terraform folder or in the Terraform plugin folder on your system.

## Quick Start

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

See the [User Guide](https://registry.terraform.io/providers/alekc/kubectl/latest) for details on installation and all the provided data and resource types.

### Reading existing objects

A `data "kubectl_manifest"` block reads any cluster object by `api_version` + `kind` + `name` (+ `namespace`) and extracts user-named fields via gojsonq dot-paths. The fetched object is also exposed as raw `yaml` and `json`.

```hcl
data "kubectl_manifest" "ns" {
  api_version = "v1"
  kind        = "Namespace"
  name        = "kube-system"
  fields = {
    phase = "status.phase"
  }
}
```

See [`docs/data-sources/kubectl_manifest.md`](./docs/data-sources/kubectl_manifest.md) for the full reference.

### Reading sensitive data without persisting it

For data that must never reach `terraform.tfstate` (Secret payloads, freshly-minted tokens, private keys), use the `ephemeral "kubectl_manifest"` resource — same shape, but the value is re-fetched on every plan / apply and never persisted. Requires Terraform 1.10+.

```hcl
ephemeral "kubectl_manifest" "db_creds" {
  api_version = "v1"
  kind        = "Secret"
  name        = "postgres-credentials"
  namespace   = "data"
  fields = {
    password = "data.password"
  }
}
```

See [`docs/ephemeral-resources/kubectl_manifest.md`](./docs/ephemeral-resources/kubectl_manifest.md) for the full reference and consumer patterns.

---

## Development Guide

You will need [Go](http://www.golang.org) (the version pinned in [`go.mod`](./go.mod) — currently 1.26 or newer) and a correctly set up [GOPATH](http://golang.org/doc/code.html#GOPATH), with `$GOPATH/bin` on your `$PATH`.

To compile the provider, run `make build`. This puts the provider binary at `$GOPATH/bin/terraform-provider-kubectl`.

### Building the provider

```sh
git clone https://github.com/alekc/terraform-provider-kubectl
cd terraform-provider-kubectl
make build
```

To point Terraform at your local build, create or edit `~/.terraformrc` and add the dev-override block below. Replace the path with your own `$GOPATH/bin/` (run `go env GOPATH` if you're unsure):

```hcl
provider_installation {
  dev_overrides {
    "alekc/kubectl" = "<your-gopath>/bin/"
  }
  direct {}
}
```

Run `terraform init` (it's a no-op under dev_overrides) and your local build is now in use. On every plan/apply you'll see Terraform note the override:

```text
╷
│ Warning: Provider development overrides are in effect
│ 
│ The following provider development overrides are set in the CLI configuration:
│  - alekc/kubectl in <your-gopath>/bin
```

### Testing

There are two test surfaces:

- **`make test`** — unit tests for the provider Go packages. No cluster required. Runs with `-race` and coverage.
- **`make testacc`** — acceptance tests against a live Kubernetes cluster. Reads `KUBE_CONFIG_PATH` (or `KUBECONFIG`) to find the cluster. The CI workflow uses [`kind`](https://kind.sigs.k8s.io/) via `helm/kind-action`; locally any reachable cluster works (kind, k3d, Docker Desktop, EKS, …). Tests are isolated by namespace / random suffix and may be run in parallel — `ACC_TESTARGS="-parallel 4"` is the default.

```sh
make test       # unit
make testacc    # acceptance — needs a cluster + KUBE_CONFIG_PATH
```

To narrow the run, pass `-run` through `ACC_TESTARGS`:

```sh
ACC_TESTARGS="-parallel 4 -run TestAccKubectlDataSourceManifest" make testacc
```

The CI workflow (`.github/workflows/tests.yaml`) runs the newest Kubernetes × Terraform pair as a single **smoke** job; only if it passes does it fan out to the rest of the matrix. This catches "obvious break" failures fast without burning 20 kind clusters on a syntax error.

*Note:* acceptance tests create real Kubernetes resources. Against a managed cluster (EKS, GKE, AKS) they may incur cost.

## Changing providers for existing resources

When you used another fork of this provider in the past, you can switch the provider on all existing resources within your state. A common use-case is moving from `gavinbunney/kubectl` to this fork.

Change the `required_providers` block in your root module and all child modules to use `alekc/kubectl` as shown in the [Installation](#terraform-013) section above. Then use `state replace-provider` to update existing state:

```sh
terraform state replace-provider gavinbunney/kubectl alekc/kubectl
```

Run `terraform init` afterwards; subsequent terraform actions will use this provider.

### Inspiration

Thanks to the original provider by [gavinbunney](https://github.com/gavinbunney/terraform-provider-kubectl) — this fork was originally based on version 1.14 and has followed a separate development path since.
