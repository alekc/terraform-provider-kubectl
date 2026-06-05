# Ephemeral Resource: kubectl_manifest

Reads a single Kubernetes object from the cluster by `api_version` + `kind` + `name` (+ `namespace`) and optionally extracts user-supplied fields by dot-path. Unlike the [`kubectl_manifest` data source](../data-sources/kubectl_manifest.md), the value produced by this resource is **never written to Terraform state** — it is re-fetched on every plan and apply that references it, then discarded.

Use this resource when reading data that must not be persisted at rest: Secret payloads, freshly-minted tokens, private keys, etc.

Requires Terraform 1.10 or later (ephemeral resources are a Terraform 1.10+ feature).

## Example Usage

### Read a Secret without persisting it

Ephemeral values cannot be referenced from `output` blocks. They can only be consumed during apply — via a resource's write-only attribute (Terraform 1.11+), a provisioner, or a `check` block.

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

# Example consumer: write the secret to a local file during apply, never to state.
resource "local_file" "db_password" {
  filename            = "${path.module}/.db-password"
  content_wo          = ephemeral.kubectl_manifest.db_creds.results["password"]
  content_wo_revision = 1
}
```

### Verify cluster invariants during plan

`check` blocks can reference ephemeral data sources to assert on live cluster state without persisting anything.

```hcl
ephemeral "kubectl_manifest" "ca" {
  api_version = "v1"
  kind        = "ConfigMap"
  name        = "kube-root-ca.crt"
  namespace   = "kube-system"
}

check "ca_present" {
  assert {
    condition     = length(ephemeral.kubectl_manifest.ca.yaml) > 0
    error_message = "cluster CA ConfigMap is missing from kube-system"
  }
}
```

## Argument Reference

Same as the [data source](../data-sources/kubectl_manifest.md):

* `api_version` - **(Required)**
* `kind` - **(Required)**
* `name` - **(Required)**
* `namespace` - **(Optional)** — leave empty for cluster-scoped kinds.
* `fields` - **(Optional)** map of named extractions. Same dot-and-bracket path grammar as the data source's `fields` (plain dotted keys, `[N]` for array indices, `["key.with.dots"]` for map keys whose name contains dots or other reserved characters). See the data source's [Argument Reference](../data-sources/kubectl_manifest.md#argument-reference) for the full grammar.

## Attribute Reference

* `yaml` - The fetched object serialised as YAML.
* `json` - The fetched object serialised as JSON.
* `uid` - The `metadata.uid` of the fetched object.
* `results` - Map of extracted field values keyed by the names declared in `fields`.

## Behaviour notes

* **Ephemeral values cannot be exported via `output`.** Terraform forbids this — the value can only flow through write-only resource attributes, provisioners, and check blocks during apply.
* **Re-fetched every run.** The resource opens on each plan/apply that references it. Live cluster state changes are picked up automatically (and may produce different downstream behaviour run-to-run).
* **No state, no plan file.** The value never appears in `terraform.tfstate` or in `terraform plan -out` plan files. This is the protection ephemeral resources provide; if you need state-persistent reads, use the data source instead.
* Errors (object not found, `fields` path not resolving, malformed path syntax) abort the open and propagate as Terraform diagnostics, same as for the data source.
