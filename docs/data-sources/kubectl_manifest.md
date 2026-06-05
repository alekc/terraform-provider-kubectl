# Data Source: kubectl_manifest

Reads a single Kubernetes object from the cluster by `api_version` + `kind` + `name` (+ `namespace`), and optionally extracts user-supplied fields by dot-path.

The fetched object is exposed as both raw YAML and raw JSON. A `fields` map can declare named extractions that are returned in `results`.

> **Tip:** the `fields` map is **optional**. Use it for cheap scalar extractions on a single value; reach for `yamldecode(data.kubectl_manifest.x.yaml)` (or `jsondecode(data.kubectl_manifest.x.json)`, identical in shape) when you want to traverse the object structurally (arrays of objects, `for` expressions, nested maps you keep referencing). `fields` paths support dotted keys via a bracket form (`metadata.labels["app.kubernetes.io/name"]`); see [Argument Reference](#argument-reference) for the full path grammar.

For reads of sensitive data that must **never** be written to Terraform state, use the [`kubectl_manifest` ephemeral resource](../ephemeral-resources/kubectl_manifest.md) instead.

## Example Usage

### Read structured data without `fields`

```hcl
# Read a Service. Note: no `fields` declared.
data "kubectl_manifest" "kube_dns" {
  api_version = "v1"
  kind        = "Service"
  name        = "kube-dns"
  namespace   = "kube-system"
}

# Walk the structured object via yamldecode (or jsondecode on the
# `json` attribute, which is identical in shape). Arrays, nested
# objects, and keys containing dots all just work.
locals {
  kube_dns       = yamldecode(data.kubectl_manifest.kube_dns.yaml)
  dns_cluster_ip = local.kube_dns.spec.clusterIP
  dns_first_port = local.kube_dns.spec.ports[0].port
  dns_owner      = local.kube_dns.metadata.labels["app.kubernetes.io/name"]

  # Iterate the array. Terraform's `for` expression composes
  # naturally with the decoded object; `fields` paths address
  # a single value per entry, not a list of derived values.
  dns_port_names = [for p in local.kube_dns.spec.ports : p.name]
}
```

### Read a ConfigMap value

```hcl
data "kubectl_manifest" "ca" {
  api_version = "v1"
  kind        = "ConfigMap"
  name        = "kube-root-ca.crt"
  namespace   = "kube-system"

  fields = {
    # Bracket form for the dotted key `ca.crt`.
    ca = "data[\"ca.crt\"]"
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

`fields` paths use a simple dot-and-bracket grammar:

- `metadata.name`: plain dot-separated map keys.
- `spec.containers[0].image` or `spec.containers.[0].image`: array index by bracketed or dotted integer; both forms work, dot-only `spec.containers.0.image` works too.
- `metadata.labels["app.kubernetes.io/name"]`: quoted bracketed segment when the key itself contains dots, slashes, or any other character that a bare segment can't carry. Either `"..."` or `'...'` quotes are accepted; the content is taken as a literal map key.

A path that does not resolve fails the read with an error naming the offending key.

```hcl
data "kubectl_manifest" "dep" {
  api_version = "apps/v1"
  kind        = "Deployment"
  name        = "nginx"
  namespace   = "web"

  fields = {
    replicas        = "spec.replicas"
    first_image     = "spec.template.spec.containers[0].image"
    labels          = "metadata.labels"
    # Domain-prefixed label keys need the bracketed form.
    app_name        = "metadata.labels[\"app.kubernetes.io/name\"]"
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
    # Bracket form for the dotted keys `tls.crt` and `tls.key`.
    crt = "data[\"tls.crt\"]"
    key = "data[\"tls.key\"]"
  }
}

# Two things to do at the consumption site:
#   1. base64decode(): Kubernetes Secrets store `data.*` values
#      base64-encoded. Reads always come back encoded; consumers
#      almost always expect the cleartext PEM / token / password.
#   2. sensitive = true: opt the output into Terraform's redaction
#      in plan/apply CLI output and propagate the marking to
#      downstream references.
output "tls_key" {
  value     = base64decode(data.kubectl_manifest.tls.results["key"])
  sensitive = true
}

# Or inline at the reference site:
resource "some_other" "x" {
  cert = sensitive(base64decode(data.kubectl_manifest.tls.results["crt"]))
}
```

The value is still written to `terraform.tfstate` in cleartext. If state-persistence itself is unacceptable, switch to the ephemeral resource.

## Argument Reference

* `api_version` - **(Required)** The API version of the resource (e.g. `v1`, `apps/v1`, `cert-manager.io/v1`).
* `kind` - **(Required)** The Kind of the resource (e.g. `ConfigMap`, `Deployment`).
* `name` - **(Required)** The `metadata.name` of the resource.
* `namespace` - **(Optional)** The `metadata.namespace` of the resource. Leave empty for cluster-scoped kinds. For namespaced kinds, an empty value defaults to `default`. A namespace supplied for a cluster-scoped kind is silently ignored.
* `fields` - **(Optional)** Map of named extractions to perform on the fetched object. Each value is a dot-and-bracket path expression:
    * `metadata.name`: dot-separated map keys.
    * `spec.containers[0].image` (or `spec.containers.[0].image`, or `spec.containers.0.image`): array index.
    * `metadata.labels["app.kubernetes.io/name"]` (or single-quoted `'...'`): quoted bracketed segment for map keys that themselves contain dots, slashes, or any other character a bare segment can't carry.

    Each path must resolve; missing paths produce an error naming the offending key. Scalar values are returned as their natural string form; objects and arrays are JSON-encoded so callers can `jsondecode()` to recover structure.

* `wait_for` - **(Optional)** Block. If set, the read blocks until the target object exists and any supplied predicates match, or until `timeouts.read` elapses. See [Wait for the object to exist](#wait-for-the-object-to-exist).

* `timeouts` - **(Optional)** Block. Honoured only when `wait_for` is set. Supports a single attribute `read` (Go duration string, default `5m`).

## Wait for the object to exist

By default, reading an object that does not exist fails immediately with an error. When the object is created by another controller in the same plan/apply (e.g. an Operator that materialises a `Secret` after a CRD is applied), set `wait_for` to block the read until the object exists and any supplied predicates are satisfied.

The block mirrors the `kubectl_manifest` resource's `wait_for` shape so users have one mental model across resource / data source / ephemeral. Inside the block:

* `field` blocks compare a dot-and-bracket path value against a literal. Use `value_type = "regex"` for regex match; the default is `eq`.
* `condition` blocks match `status.conditions[type=X, status=Y]` entries, the standard Kubernetes "Ready / Available / Synced" pattern.

A bare `wait_for {}` block with no nested children waits only for the object to exist; predicates make the wait stricter.

```hcl
data "kubectl_manifest" "cert_tls" {
  api_version = "v1"
  kind        = "Secret"
  name        = "wildcard-tls"
  namespace   = "ingress"

  # Block until cert-manager has materialised the Secret AND the
  # parent Certificate's Ready condition is true.
  wait_for {
    field {
      key   = "type"
      value = "kubernetes.io/tls"
    }
  }

  timeouts {
    read = "10m"
  }
}

output "tls_crt_pem" {
  value     = base64decode(data.kubectl_manifest.cert_tls.results["crt"])
  sensitive = true
}
```

```hcl
data "kubectl_manifest" "argo_app" {
  api_version = "argoproj.io/v1alpha1"
  kind        = "Application"
  name        = "platform-bootstrap"
  namespace   = "argocd"

  # Wait until Argo CD reports both Synced=True and Healthy=True.
  wait_for {
    condition {
      type   = "Synced"
      status = "True"
    }
    condition {
      type   = "Healthy"
      status = "True"
    }
  }
}
```

Internally the helper runs a poll-then-watch hybrid: a short polling loop (1s initial, capped at 10s) until the apiserver returns the object, then a watch with field-selector on the resource name driven by the existing `wait_for` predicate engine. Conditions and fields are re-evaluated on each Added / Modified event. RBAC: the data source's service account must hold `get` and `watch` on the target kind for the wait to succeed.

## Attribute Reference

* `yaml` - The fetched object serialised as YAML.
* `json` - The fetched object serialised as JSON.
* `uid` - The `metadata.uid` of the fetched object.
* `results` - Map of extracted field values keyed by the names declared in `fields`. Scalar values are stringified; objects and arrays are JSON-encoded so callers can `jsondecode()` to recover structure.

## Notes

* If the requested object does not exist on the cluster AND `wait_for` is unset, the data source fails immediately. With `wait_for` set, the read blocks until the object exists or `timeouts.read` elapses.
* If `fields` declares a path that does not resolve, the read fails with an error naming the offending key. There is no per-field "lenient" mode today.
* `kind` is matched as-supplied — `Deployment` works, `deployment` does not. Match the actual Kubernetes object Kind exactly.
