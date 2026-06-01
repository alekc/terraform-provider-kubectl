# Kubernetes "kubectl" Provider 

![Build Status](https://github.com/alekc/terraform-provider-kubectl/actions/workflows/tests.yaml/badge.svg) [![user guide](https://img.shields.io/badge/-user%20guide-blue)](https://registry.terraform.io/providers/alekc/kubectl)

This provider offers the most effective method for handling Kubernetes resources in Terraform. It empowers you to leverage what Kubernetes values most — YAML.

At the heart of this provider lies the `kubectl_manifest` resource, enabling the processing and application of free-form YAML directly to Kubernetes. This YAML object is monitored across its full lifecycle — creation, updates, drift detection and deletion.

For reads, the provider exposes both a `data "kubectl_manifest"` source for ordinary lookups and an `ephemeral "kubectl_manifest"` resource (Terraform 1.10+) that fetches Secret payloads, freshly-minted tokens, and any other sensitive data without ever writing the value to `terraform.tfstate`.

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
| Ephemeral resource | [`kubectl_manifest`](./docs/ephemeral-resources/kubectl_manifest.md)                 | Read any cluster object without ever writing the value to `terraform.tfstate` or the plan file. Required for Secret payloads, freshly-minted tokens, private keys, and anything else you must keep out of state. Re-fetched on every plan / apply. Terraform 1.10+. |

## Supported Kubernetes, Terraform, and OpenTofu versions

Every PR is exercised against the matrices below on `kind`. The matrices are regenerated from [endoflife.date](https://endoflife.date) on each CI run, so they track the five most recent active Kubernetes release cycles, the five most recent stable Terraform minors (plus a legacy `1.5.7` pin, the last MPL-licensed Terraform release), and the five most recent OpenTofu versions.

### Terraform

|                 | Terraform 1.15 | Terraform 1.14 | Terraform 1.13 | Terraform 1.12 | Terraform 1.11 | Terraform 1.5.7 |
| --------------- | :------------: | :------------: | :------------: | :------------: | :------------: | :-------------: |
| Kubernetes 1.36 | smoke + ✅      | ✅              | ✅              | ✅              | ✅              | ✅               |
| Kubernetes 1.35 | ✅              | ✅              | ✅              | ✅              | ✅              | ✅               |
| Kubernetes 1.34 | ✅              | ✅              | ✅              | ✅              | ✅              | ✅               |
| Kubernetes 1.33 | ✅              | ✅              | ✅              | ✅              | ✅              | ✅               |
| Kubernetes 1.32 | ✅              | ✅              | ✅              | ✅              | ✅              | ✅               |

### OpenTofu

|                 | OpenTofu 1.11 | OpenTofu 1.10 | OpenTofu 1.9 | OpenTofu 1.8 | OpenTofu 1.7 |
| --------------- | :-----------: | :-----------: | :----------: | :----------: | :----------: |
| Kubernetes 1.36 | smoke + ✅     | ✅             | ✅            | ✅            | ✅            |
| Kubernetes 1.35 | ✅             | ✅             | ✅            | ✅            | ✅            |
| Kubernetes 1.34 | ✅             | ✅             | ✅            | ✅            | ✅            |
| Kubernetes 1.33 | ✅             | ✅             | ✅            | ✅            | ✅            |
| Kubernetes 1.32 | ✅             | ✅             | ✅            | ✅            | ✅            |

The versions in the tables are the snapshot resolved at the time of writing; the live matrices move with the upstream release cadences. Each engine has its own **smoke** job (latest Kubernetes × latest CLI for that engine); the rest of that engine's matrix fans out only after its smoke passes. The Terraform and OpenTofu halves run independently, so an issue on one side does not block the other. Combinations outside this grid may still work; your mileage may vary.

## Installation

### Terraform 0.13+

The provider can be installed and managed automatically by Terraform. Sample `versions.tf` file:

```hcl
terraform {
  required_version = ">= 1.0"

  required_providers {
    kubectl = {
      source  = "alekc/kubectl"
      version = "~> 2.3"
    }
  }
}
```

The provider itself works back to Terraform 0.13. The example above pins `>= 1.0` because most users will be on a supported Terraform release; if you need to run older Terraform, you can drop the constraint to `>= 0.13`.

If your configuration uses the `ephemeral "kubectl_manifest"` block (covered below), the floor moves up to **Terraform 1.10**, because ephemeral resources are a 1.10+ language feature regardless of the provider version. Bump `required_version` to `">= 1.10"` in that case.

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

### Reading sensitive data without persisting it

For data that must never reach `terraform.tfstate` (Secret payloads, freshly-minted tokens, private keys), use the `ephemeral "kubectl_manifest"` resource. It has the same lookup shape as the data source, but the value is re-fetched on every plan / apply and never persisted. Requires Terraform 1.10+.

Ephemeral values cannot flow through `output` blocks. They are consumed during apply through a resource's write-only attribute (Terraform 1.11+), a provisioner, or a `check` block:

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

# Example consumer: stage the password into a sibling tool's config
# during apply, never to state. `content_wo` is a write-only attribute
# (Terraform 1.11+); the value is forgotten after the file is written.
resource "local_file" "db_password" {
  filename            = "${path.module}/.db-password"
  content_wo          = ephemeral.kubectl_manifest.db_creds.results["password"]
  content_wo_revision = 1
}
```

See [`docs/ephemeral-resources/kubectl_manifest.md`](./docs/ephemeral-resources/kubectl_manifest.md) for the full reference, additional consumer patterns (including `check` blocks for cluster invariants), and behaviour notes.

### Reading existing objects

A `data "kubectl_manifest"` block reads any cluster object by `api_version` + `kind` + `name` (+ `namespace`) and extracts user-named fields via gojsonq dot-paths. The fetched object is also exposed as raw `yaml` and `json`. Use this when the value is non-sensitive and you want it cached in state; reach for the ephemeral resource above when it is not.

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

---

## Contributing

Building from source, pointing Terraform at a local build with
`dev_overrides`, running the test suites, and the PR workflow are all
documented in [CONTRIBUTING.md](./CONTRIBUTING.md). Start there if you
want to try an unreleased fix from `master` or submit a change.

## Migrating from gavinbunney/kubectl

If you previously used `gavinbunney/kubectl`, you can switch existing `kubectl_manifest` resources to this fork without destroying and recreating anything. Pick the recipe that matches your CLI.

### Terraform 1.8+: a `moved` block

This provider implements native cross-provider state move for `kubectl_manifest`, so a `moved` block migrates state in place during a normal `plan` / `apply`, with no separate state surgery and no resource churn. Cross-provider move support requires the framework-based release of this provider (the release that ships the plugin-framework `kubectl_manifest`); pin to that release or newer in `required_providers`.

Terraform's HCL validator rejects `moved` blocks whose `from` and `to` addresses are identical, even when the provider FQN differs, so the migration uses a rename-and-rename-back pattern. Pick a transitional address (any unused name; `_v3` works):

```hcl
terraform {
  required_providers {
    kubectl = {
      source = "alekc/kubectl"
    }
  }
}

moved {
  from = kubectl_manifest.my_app          # was gavinbunney/kubectl
  to   = kubectl_manifest.my_app_v3       # now alekc/kubectl
}

resource "kubectl_manifest" "my_app_v3" {
  # ... same yaml_body / attributes as before
}
```

Run `terraform init -upgrade`, then `terraform plan`. The plan reports the resource as moved with no in-place changes:

```
# kubectl_manifest.my_app has moved to kubectl_manifest.my_app_v3
    resource "kubectl_manifest" "my_app_v3" {
        # (N unchanged attributes hidden)
    }
Plan: 0 to add, 0 to change, 0 to destroy.
```

Run `terraform apply` to commit the move (no-op for the resource itself; only the state address changes). After the move applies you can either keep the new name or rename back: drop the `_v3` from the resource block, add a second `moved` block pointing `my_app_v3` to `my_app`, and apply again. Once the addresses match, remove the `moved` blocks entirely.

Note that `terraform plan -detailed-exitcode` returns 2 on the first plan because `moved` annotations count as "changes present" even when the resource summary is `0 to add, 0 to change, 0 to destroy`. The second plan after apply returns 0 cleanly. Treat the summary line as authoritative.

The 20 attributes shared with gavinbunney carry over unchanged. The four attributes that exist only on this fork take their defaults on the moved resource, all of which are no-ops for an unchanged manifest:

| Attribute | Default after move | Effect |
| --- | --- | --- |
| `upgrade_api_version` | `false` | Keeps the conservative behaviour: an `api_version` change still forces replacement unless you opt in. |
| `field_manager` | `kubectl` | Only consulted when `server_side_apply = true`; matches the historic default. |
| `wait_for` | unset | No extra cluster-side wait conditions. |
| `delete_cascade` | unset | Delete propagation stays automatic (`Foreground` when `wait = true`, else `Background`). |

One behaviour changes, for the better: on `gavinbunney/kubectl` an `api_version` change always forced a destroy-and-recreate; on this fork it applies in place when `upgrade_api_version = true`. The default (`false`) matches gavinbunney, so the move itself never changes how your existing resources plan.

### OpenTofu: `state replace-provider`

OpenTofu 1.11.x does not invoke the destination provider's `MoveStateResource` handler on cross-provider transitions, so a `moved` block migrates the state address but leaves the attribute payload untranslated. The first plan on OpenTofu after a `moved`-block migration therefore reports an in-place change on every moved resource, which is not what you want. Until OpenTofu ships cross-provider move dispatch, use `state replace-provider` instead. Change the `required_providers` block in your root module and all child modules to use `alekc/kubectl`, then run:

```sh
tofu state replace-provider gavinbunney/kubectl alekc/kubectl
tofu init
```

The next `tofu plan` is a no-op. The four alekc-only attributes show as `+ field_manager`, `+ upgrade_api_version`, etc. on the first plan because `state replace-provider` does not invoke `MoveStateResource` either; running `tofu apply` once absorbs them and subsequent plans are clean.

### Terraform older than 1.8

Same recipe as OpenTofu: use `terraform state replace-provider gavinbunney/kubectl alekc/kubectl` to flip the provider FQN on every resource, then `terraform init`. The first plan after the swap shows the four alekc-only attributes as additions; one `terraform apply` absorbs them.

### Note on `kubectl_kustomize_documents`

`gavinbunney/kubectl` added a `kubectl_kustomize_documents` data source after this fork diverged, and this fork does not (yet) provide it. If your configuration uses that data source, a full switch is not possible without re-architecting that part (for example, rendering `kustomize build` output through the `external` data source, or keeping `gavinbunney/kubectl` aliased alongside this provider for that one data source). Everything else migrates cleanly.

### Inspiration

Thanks to the original provider by [gavinbunney](https://github.com/gavinbunney/terraform-provider-kubectl), this fork was originally based on version 1.14 and has followed a separate development path since.
