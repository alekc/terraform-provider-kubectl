# Contributing to terraform-provider-kubectl

Thanks for your interest in contributing! This document covers building the
provider from source, testing a custom build against your own Terraform
configurations, running the test suites, and submitting changes.

## Prerequisites

- [Go](https://go.dev/dl/) at the version pinned in [`go.mod`](./go.mod)
  (currently 1.26 or newer)
- `$(go env GOPATH)/bin` on your `$PATH`
- Terraform 0.14+ (`dev_overrides` was added in 0.14) if you want to point
  Terraform at a local build
- A Kubernetes cluster for acceptance tests ([`kind`](https://kind.sigs.k8s.io/),
  [`k3d`](https://k3d.io/), Docker Desktop, or any reachable cluster)

## Building the provider

```sh
git clone https://github.com/alekc/terraform-provider-kubectl
cd terraform-provider-kubectl
make build
```

`make build` runs `go install`, which places the binary at
`$(go env GOPATH)/bin/terraform-provider-kubectl`. The binary name must
stay `terraform-provider-kubectl` for Terraform to pick it up via
`dev_overrides`.

## Trying an unreleased version

If a fix you need is already on `master` but no release has been cut yet,
you can build the provider locally and point Terraform at the binary with a
[`dev_overrides`](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers)
block. This is the same mechanism the project's own CI smoke job uses,
and is the recommended way to test a regression fix before it ships to the
Terraform Registry.

### Build the version you want

```sh
git clone https://github.com/alekc/terraform-provider-kubectl
cd terraform-provider-kubectl
git checkout master           # or any branch / tag / commit
make build                    # binary at $(go env GOPATH)/bin/terraform-provider-kubectl
```

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

Run `go env GOPATH` to fill in the path if you are unsure. The `direct {}`
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
`terraform plan` or `terraform apply` onward Terraform prints the override
warning every run:

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

## Running tests

There are two test surfaces:

- **`make test`** runs unit tests for the provider Go packages. No
  cluster required. Runs with `-race` and coverage.
- **`make testacc`** runs acceptance tests against a live Kubernetes
  cluster. Reads `KUBE_CONFIG_PATH` (or `KUBECONFIG`) to find the
  cluster. The CI workflow uses [`kind`](https://kind.sigs.k8s.io/) via
  `helm/kind-action`; locally any reachable cluster works (kind, k3d,
  Docker Desktop, EKS, etc.). Tests are isolated by namespace / random
  suffix and may be run in parallel. `ACC_TESTARGS="-parallel 4"` is the
  default.

```sh
make test       # unit
make testacc    # acceptance, needs a cluster + KUBE_CONFIG_PATH
```

To narrow the run, pass `-run` through `ACC_TESTARGS`:

```sh
ACC_TESTARGS="-parallel 4 -run TestAccKubectlDataSourceManifest" make testacc
```

The CI workflow (`.github/workflows/tests.yaml`) runs the newest
Kubernetes x Terraform pair as a single **smoke** job; only if it passes
does it fan out to the rest of the matrix. This catches obvious-break
failures fast without burning 20 kind clusters on a syntax error.

> Acceptance tests create real Kubernetes resources. Against a managed
> cluster (EKS, GKE, AKS) they may incur cost. Point them at a throw-away
> cluster.

## Project layout

- `internal/framework/` is the entire provider (`terraform-plugin-framework`).
  It hosts every resource and data source, the ephemeral `kubectl_manifest`
  resource, and the authoritative provider configuration schema in
  `internal/framework/provider.go`. v3 completed the SDK v2 to plugin-framework
  migration; the old `tf6muxserver` hybrid is gone (see
  `docs/adr/0001-framework-migration-and-moved-blocks.md` for the history).
- `kubernetes/` is the transport-agnostic implementation layer the framework
  resources call into: the Kubernetes client (`BuildKubeProvider`,
  `ProviderConfig`), manifest lifecycle helpers (`ApplyManifest`,
  `ReadManifest`, `DeleteManifest`), and fetch / drift / wait-for / YAML
  logic. It no longer registers a provider, resource, or data source of its
  own.
- `internal/types/` holds small shared type helpers.
- `main.go` serves the framework provider directly via
  `providerserver.Serve`; there is no mux server.
- `docs/` holds the Terraform Registry-rendered documentation. Resource
  / data-source pages live under `docs/resources/`, `docs/data-sources/`,
  and `docs/ephemeral-resources/`.

## Submitting changes

1. Open an issue first if the change is non-trivial, so we can agree on
   the shape before code is written.
2. Branch off `master`. A descriptive branch name helps
   (`feat/issue-NNN-short-slug` works well).
3. Follow [Conventional Commits](https://www.conventionalcommits.org/)
   for commit subjects, e.g. `feat(provider): add lazy_load to defer
   kubeconfig errors`. Use the body to explain the why, not the what.
4. Sign off every commit with `git commit -s` so the
   `Signed-off-by` trailer is added. We follow
   [DCO](https://developercertificate.org/) for contributions.
5. Keep PRs focused. A diagnostic improvement and an unrelated refactor
   should be two PRs, not one.
6. Provider configuration lives in one place: `internal/framework/provider.go`.
   When you add or change a provider argument, update all three parts together:
   the wire schema in `Schema`, the decode model `providerConfigModel`, and the
   env-var / default handling in `Configure`. There is no second schema to keep
   in sync.
7. Update relevant docs in the same PR. New provider attribute means a
   line in `docs/index.md` "Argument Reference"; new behaviour means a
   note in Troubleshooting; new resource means a page under
   `docs/resources/`.
8. Run `make test` locally before opening the PR. CI will run the full
   matrix; the smoke job's job is to fail fast.
9. CodeRabbit also reviews PRs automatically. Resolve its comments or
   reply why they do not apply before requesting human review.

If your change is a fix for an open issue, reference it in the PR
description (`Fixes: alekc/terraform-provider-kubectl#NNN`) so GitHub
links and auto-closes on merge.

## Reporting bugs

[Open an issue](https://github.com/alekc/terraform-provider-kubectl/issues/new)
with:

- Provider version (`terraform providers`)
- Terraform version (`terraform version`)
- Kubernetes cluster type and version (`kubectl version`)
- A minimal config that reproduces the problem
- The full error output, not just the last line

If the error mentions provider configuration, include the relevant
`provider "kubectl"` block (with secrets redacted).
