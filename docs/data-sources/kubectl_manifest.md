# Data Source: kubectl_manifest

Reads a single Kubernetes object from the cluster by `api_version` + `kind` + `name` (+ `namespace`), and optionally extracts user-supplied fields by dot-path.

The fetched object is exposed as both raw YAML and raw JSON. A `fields` map can declare named extractions that are returned in `results`.

For reads of sensitive data that must **never** be written to Terraform state, use the [`kubectl_manifest` ephemeral resource](../ephemeral-resources/kubectl_manifest.md) instead.

## Example Usage

### Read a ConfigMap value

```hcl
data "kubectl_manifest" "ca" {
  api_version = "v1"
  kind        = "ConfigMap"
  name        = "kube-root-ca.crt"
  namespace   = "kube-system"

  fields = {
    ca = "data.ca\\.crt"
  }
}

output "ca_bundle" {
  value = data.kubectl_manifest.ca.results["ca"]
}
```

### Read a cluster-scoped object

`namespace` is optional. Leave it empty for cluster-scoped kinds.

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

### Read a CRD object and walk a nested array

`fields` paths use [gojsonq](https://github.com/thedevsaddam/gojsonq)
dot-notation. Array elements are addressed with the `[N]` form
(`containers.[0]`, not `containers.0`).

```hcl
data "kubectl_manifest" "dep" {
  api_version = "apps/v1"
  kind        = "Deployment"
  name        = "nginx"
  namespace   = "web"

  fields = {
    replicas        = "spec.replicas"
    first_image     = "spec.template.spec.containers.[0].image"
    labels          = "metadata.labels"
  }
}

# `labels` is a JSON-encoded object string. `jsondecode()` recovers it.
output "labels" {
  value = jsondecode(data.kubectl_manifest.dep.results["labels"])
}
```

### Reading sensitive data

This data source does **not** mark its outputs sensitive at the schema level. Callers reading sensitive data should opt in at the consumption site:

```hcl
data "kubectl_manifest" "tls" {
  api_version = "v1"
  kind        = "Secret"
  name        = "ingress-tls"
  namespace   = "default"

  fields = {
    crt = "data.tls\\.crt"
    key = "data.tls\\.key"
  }
}

# Opt-in: mark the output sensitive so it's redacted in plan/apply CLI output
# and downstream references inherit the marking.
output "tls_key" {
  value     = data.kubectl_manifest.tls.results["key"]
  sensitive = true
}

# Or inline at the reference site:
resource "some_other" "x" {
  cert = sensitive(data.kubectl_manifest.tls.results["crt"])
}
```

The value is still written to `terraform.tfstate` in cleartext. If state-persistence itself is unacceptable, switch to the ephemeral resource.

## Argument Reference

* `api_version` - **(Required)** The API version of the resource (e.g. `v1`, `apps/v1`, `cert-manager.io/v1`).
* `kind` - **(Required)** The Kind of the resource (e.g. `ConfigMap`, `Deployment`).
* `name` - **(Required)** The `metadata.name` of the resource.
* `namespace` - **(Optional)** The `metadata.namespace` of the resource. Leave empty for cluster-scoped kinds. For namespaced kinds, an empty value defaults to `default`. A namespace supplied for a cluster-scoped kind is silently ignored.
* `fields` - **(Optional)** Map of named extractions to perform on the fetched object. Each value is a gojsonq dot-path expression. Each path must resolve; missing paths produce an error.

## Attribute Reference

* `yaml` - The fetched object serialised as YAML.
* `json` - The fetched object serialised as JSON.
* `uid` - The `metadata.uid` of the fetched object.
* `results` - Map of extracted field values keyed by the names declared in `fields`. Scalar values are stringified; objects and arrays are JSON-encoded so callers can `jsondecode()` to recover structure.

## Notes

* If the requested object does not exist on the cluster, the data source fails with an error. (Convention shared with `data "kubernetes_secret"` in `terraform-provider-kubernetes`.)
* If `fields` declares a path that does not resolve, the read fails with an error naming the offending key. There is no per-field "lenient" mode today.
* `kind` is matched as-supplied — `Deployment` works, `deployment` does not. Match the actual Kubernetes object Kind exactly.
