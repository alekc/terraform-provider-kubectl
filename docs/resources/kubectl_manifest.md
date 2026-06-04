# Resource: kubectl_manifest

Create a Kubernetes resource using raw YAML manifests.

This resource handles creation, deletion and even updating your Kubernetes resources. This allows complete lifecycle management of your Kubernetes resources as terraform resources!

Behind the scenes, this provider uses the same capability as the `kubectl apply` command, that is, you can update the YAML inline and the resource will be updated in place in Kubernetes.

> **TIP:** This resource only supports a single yaml resource. If you have a list of documents in your yaml file,
> use the [kubectl_path_documents](https://registry.terraform.io/providers/alekc/kubectl/latest/docs/data-sources/kubectl_path_documents) data source to split the files into individual resources.

## Example Usage

```hcl
resource "kubectl_manifest" "test" {
    yaml_body = <<YAML
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: test-ingress
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
    azure/frontdoor: enabled
spec:
  rules:
  - http:
      paths:
      - path: /testpath
        pathType: "Prefix"
        backend:
          serviceName: test
          servicePort: 80
YAML
}
```

> Note: When the kind is a Deployment, this provider will wait for the deployment to be rolled out automatically for you!

### With explicit `wait_for`

If `wait_for` is specified, upon applying the resource, provider will wait for **all** conditions to become true before proceeding further.  

```hcl
resource "kubectl_manifest" "test" {
  wait_for {
    field {
      key = "status.containerStatuses.[0].ready"
      value = "true"
    }
    field {
      key = "status.phase"
      value = "Running"
    }
    field {
      key = "status.podIP"
      value = "^(\\d+(\\.|$)){4}"
      value_type = "regex"
    }
    condition {
      type = "ContainersReady"
      status = "True"
    }
    condition {
      type = "Ready"
      status = "True"
    }
  }
  yaml_body = <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: nginx
spec:
  containers:
  - name: nginx
    image: registry.k8s.io/e2e-test-images/nginx:1.28.0-1
    readinessProbe:
      httpGet:
        path: "/"
        port: 80
      initialDelaySeconds: 10
YAML
}
```

## Argument Reference

* `yaml_body` - Required. YAML to apply to kubernetes.
* `sensitive_fields` - Optional. List of fields (dot-syntax) which are sensitive and should be obfuscated in output. Defaults to `["data"]` for Secrets.
* `force_new` - Optional. Forces delete & create of resources if the `yaml_body` changes. Default `false`.
* `upgrade_api_version` - Optional. When `true`, changing the `apiVersion` in `yaml_body` will update the resource in-place rather than forcing a delete and recreate. This leverages Kubernetes' ability to represent the same object across multiple API versions. Default `false`.
* `server_side_apply` - Optional. Allow using server-side-apply method. Default `false`.
* `field_manager` - Optional. Override the default field manager name. This is only relevent when using server-side apply. Default `kubectl`.
* `force_conflicts` - Optional. Allow using force_conflicts. Default `false`.
* `apply_only` - Optional. When `true`, the resource is never deleted on `terraform destroy`. Default `false`.
* `ignore_fields` - Optional. List of map fields to ignore when applying the manifest. See below for more details.
* `show_drift_values` - Optional. Controls how leaf values appear in the computed `drift` attribute. One of `none` (default; renders `<drift>` markers, paths only), `hash` (renders `<was:HASH now:HASH>` short-hash markers), or `full` (renders literal before/after values, parity with `kubectl diff`). Secret `data` / `stringData` paths and `mask_paths` globs always mask their leaves regardless of mode. See [Drift Visualisation](#drift-visualisation) below.
* `mask_paths` - Optional. Glob paths whose leaves are masked in the `drift` attribute regardless of `show_drift_values`. Supports `*` (one path segment) and `**` (zero or more segments). E.g. `spec.template.spec.containers.*.env.*.value` or `**.password`. Layers on top of the built-in Secret masking.
* `drift_engine` - Optional. Algorithm used to detect drift. `client` (default) compares the user's manifest to the live object client-side; fast (no extra API calls) but susceptible to false drift on arrays, server-side defaulting, and admission-webhook mutations. `server` runs an SSA dry-run patch against the apiserver and uses the response as the desired side of the comparison; same semantics as `kubectl diff`. Costs one extra API call per Read and requires PATCH on the resource's kind in RBAC. Falls back to `client` on patch failure with a `[WARN]` log.
* `override_namespace` - Optional. Override the namespace to apply the kubernetes resource to, ignoring any declared namespace in the `yaml_body`.
* `validate_schema` - Optional. Setting to `false` will mimic `kubectl apply --validate=false` mode. Default `true`.
* `wait` - Optional. When `true`, wait for finalizers to complete on deleted objects before returning. Default `false`.
* `wait_for_rollout` - Optional. Set this flag to wait or not for `Deployment`, `DaemonSet`, `StatefulSet` & `APIService`  resources to complete rollout. Default `true`.
* `wait_for` - Optional. If set, will wait until either all conditions are satisfied, or until timeout is reached (see [below for nested schema](#wait_for)). Under the hood [gojsonq](https://github.com/thedevsaddam/gojsonq) is used for querying, see the related syntax and examples.
* `delete_cascade` - Optional; `Background` or `Foreground` are valid options. If set this overrides the default provider behaviour which is to use `Background` unless `wait` is `true` when `Foreground` will be used. To duplicate the default behaviour of `kubectl` this should be explicitly set to `Background`.

### `wait_for`

Required, at least one of:

* `field` (Block List, Min: 0) Condition criteria for a field (see [below for nested schema](#wait_forfield))
* `condition` (Block List, Min: 0) Condition criteria for a condition (see [below for nested schema](#wait_forcondition))

### `wait_for.field`

Required:

* `key` (String) Key which should be matched from resulting object
* `value` (String) Value to wait for

Optional:

- `value_type` (String) Value type. Can be either a `eq` (equivalent) or `regex`

### `wait_for.condition`

Required:

* `type` (String) Type as expected from the resulting Condition object
* `status` (String) Status to wait for in the resulting Condition object

## Attribute Reference

* `yaml_body_parsed` - Obfuscated version of `yaml_body`, with `sensitive_fields` hidden.
* `api_version` - Extracted API Version from `yaml_body`.
* `kind` - Extracted object kind from `yaml_body`.
* `name` - Extracted object name from `yaml_body`.
* `namespace` - Extracted object namespace from `yaml_body`.
* `uid` - Kubernetes unique identifier from last run.
* `live_uid` - Current uuid from Kubernetes.
* `drift` - Human-readable YAML subtree showing the paths where the desired manifest differs from the live object. Empty string when in sync. Leaf rendering is controlled by `show_drift_values`; Secret kinds and `mask_paths` always mask regardless of mode. See [Drift Visualisation](#drift-visualisation).

## Drift Visualisation

In v2.x, drift between the desired manifest and the live object was tracked through the `yaml_incluster` and `live_manifest_incluster` attributes, both opaque sha256 fingerprints. When they diverged, Terraform plan output looked like:

```diff
~ yaml_incluster = (sensitive value) -> (sensitive value)
```

That told you *something* drifted but not *what*. Resolving it meant re-running with `TF_LOG=trace` and grepping for `yaml drift detected` lines. See [issue #54](https://github.com/alekc/terraform-provider-kubectl/issues/54).

The v3 `drift` attribute replaces that workflow. It is a YAML subtree of the paths where the desired manifest differs from the live object. Empty string means in sync; populated content renders as a multi-line diff in plan output. Three rendering modes via `show_drift_values`:

### `show_drift_values = "none"` (default)

Safe: only paths render, values appear as `<drift>` markers.

```yaml
metadata:
  annotations:
    foo: <drift>
spec:
  replicas: <drift>
```

### `show_drift_values = "hash"`

Confirms a value really changed without revealing it. Useful for sensitive workloads where the fact of the change is informative but the value is not.

```yaml
metadata:
  annotations:
    foo: <was:37b51d1 now:9d0c4ac>
spec:
  replicas: <was:d4735e3 now:4e07408>
```

### `show_drift_values = "full"`

Parity with `kubectl diff`: literal before/after values. Use for non-sensitive workloads where you want maximum visibility.

```yaml
metadata:
  annotations:
    foo: <was: "bar", now: "BAR">
spec:
  replicas: <was: 2, now: 3>
```

Even with `show_drift_values = "full"`, `data.*` and `stringData.*` on `kind: Secret` are always masked to `<drift sensitive>`, as are any paths matched by `mask_paths`. Example with a paranoid masking policy:

```hcl
resource "kubectl_manifest" "example" {
  yaml_body         = local.manifest_yaml
  show_drift_values = "full"
  mask_paths = [
    "**.password",
    "**.token",
    "spec.template.spec.containers.*.env.*.value",
  ]
}
```

### Detection engine: `drift_engine`

The provider supports two drift-detection algorithms with the same output shape:

| Mode | What it does | When to use |
|---|---|---|
| `client` (default) | Flattens the user's manifest and the live object into dotted paths and compares per-key. No extra API calls. Same algorithm v2 used. | Default. Stick with this unless you have a specific reason to switch. Existing `ignore_fields` lists are tuned against it. |
| `server` (opt-in) | Calls the apiserver with `Patch(ApplyPatchType, body, DryRun: All)` and uses the response as the desired side of the comparison. Same semantics `kubectl diff` uses. | Resources where the client engine reports false drift on every refresh — typically operator-managed objects, CRDs with server-side defaulting, manifests mutated by admission webhooks, or arrays whose order the apiserver reorganises. |

The `drift` attribute shape is identical between engines, so switching is purely a behaviour swap, not a schema change.

#### `client` engine

```hcl
resource "kubectl_manifest" "example" {
  yaml_body = local.manifest_yaml
  # drift_engine = "client"  # default; omit to use
}
```

The comparison walks every key the user wrote in `yaml_body`. Extra fields the apiserver / controllers add (status, server-side defaults, webhook injections, managed-fields metadata) are ignored — same as v2. Drift surfaces when:

- A value the user wrote differs from the live value (string equality, with whitespace trimmed)
- A field the user wrote is absent from the live object

Tends to over-report on objects that go through normalisation (memory units, durations) or admission-webhook mutation. The fix in v2 was to add the noisy path to `ignore_fields`; the same fix works here.

#### `server` engine

```hcl
resource "kubectl_manifest" "example" {
  yaml_body    = local.manifest_yaml
  drift_engine = "server"
}
```

Each Read issues an SSA dry-run patch against the apiserver. The patch response is the apiserver's view of what the post-apply object would look like (with all defaulting, normalisation, and mutating-webhook edits applied). That response becomes the desired side; the current live object is the live side; the renderer produces drift only for paths where they actually differ.

**Trade-offs versus `client`:**

- ✅ Strategic-merge comparison on arrays (no false drift from element reordering)
- ✅ Server-side defaulting absorbed (no false drift from CRD-injected defaults)
- ✅ Type coercion handled by the apiserver (`"2048Mi"` and `"2Gi"` agree)
- ✅ Mutating-webhook edits visible in `drift` — you see what apply will actually change
- ✅ `metadata.namespace` injected by `override_namespace` onto cluster-scoped resources gets stripped server-side, suppressing the long-standing false drift on `ClusterRole`/`CRD`
- ⚠ One extra API call per Read. With 500 manifests at default `parallelism=10`, that's roughly 5-15s added to plan time depending on apiserver latency.
- ⚠ Needs the `PATCH` verb on the resource's kind in the ServiceAccount's RBAC. Workloads with tightly-scoped RBAC (CREATE+UPDATE via apply, no PATCH) need a role broadening.
- ⚠ Admission webhooks may fire side effects despite `?dryRun=All`. **Read the warning below before enabling.**
- ⚠ A small set of CRDs (older ones without proper SSA schemas) reject `ApplyPatchType`. The engine falls back to `client` with a `[WARN]` log per resource.

`fieldManager` and `force_conflicts` from the same resource configuration are passed through to the dry-run patch, so the dry-run result represents what *this* resource's apply would do, not what some other writer's apply would do.

> **Warning — admission webhook side effects:**
>
> Mutating and validating admission webhooks SHOULD honour `?dryRun=All` by skipping any external side effects (no Slack messages, no audit-log writes to external systems, no CI triggers). The Kubernetes spec is clear on this, and most upstream webhooks (Gatekeeper, Kyverno, cert-manager, Istio sidecar injector) implement it correctly.
>
> Custom or third-party webhooks may not. Before enabling `drift_engine = "server"` cluster-wide, audit:
>
> 1. List your webhooks: `kubectl get validatingwebhookconfigurations,mutatingwebhookconfigurations`
> 2. For each non-upstream one, verify it checks `request.dryRun` before any external write
>
> If you're not sure, enable `drift_engine = "server"` on a single resource first and observe (`kubectl plan`, then check downstream systems for spurious activity) before rolling it out broadly. Switching back is one config line.

### Migration from v2 (`yaml_incluster`)

v3 dropped the legacy `yaml_incluster` and `live_manifest_incluster` attributes (both opaque sha256 fingerprints) in favour of `drift`. State migrates automatically via the resource state upgrader: existing state is decoded, the two fingerprint values are dropped, and `drift` is computed fresh on the next Read against the live cluster. No manual `terraform state` surgery required.

HCL that referenced the legacy attributes (rare — both values were opaque sensitive strings) breaks loudly at plan time with a clear missing-attribute message. The migration is one line:

```hcl
# Before (v2): detect drift by comparing the two opaque fingerprints
output "drifted" {
  value = kubectl_manifest.example.yaml_incluster != kubectl_manifest.example.live_manifest_incluster
}

# After (v3): drift is a string that's empty when in sync
output "drifted" {
  value = kubectl_manifest.example.drift != ""
}
```

`ignore_fields` lists carry over unchanged — the rules and the path syntax are identical to v2.

### Future default

The `client` engine is the default in v3 to preserve v2's drift-detection semantics for upgrading users (zero behaviour change beyond the attribute shape). A later v3.x minor may flip the default to `server` once the community feedback on dry-run reliability across cluster shapes (webhook coverage, CRD compatibility, RBAC scoping) is in. The opt-out (`drift_engine = "client"`) will remain supported in that case.

## Sensitive Fields

You can obfuscate fields in the diff output by setting the `sensitive_fields` option. This allows you to hide arbitrary field content by suppressing the information in the diff.

By default, this is set to `["data"]` for all `v1/Secret` manifests.

The fields provided should use dot-separator syntax to specify the field to obfuscate.

```hcl
resource "kubectl_manifest" "test" {
    sensitive_fields = [
        "metadata.annotations.my-secret-annotation"
    ]

    yaml_body = <<YAML
apiVersion: admissionregistration.k8s.io/v1beta1
kind: MutatingWebhookConfiguration
metadata:
  name: istio-sidecar-injector
  annotations:
    my-secret-annotation: "this is very secret"
webhooks:
  - clientConfig:
      caBundle: ""
YAML
}
```

> Note: Only Map values are supported to be made sensitive. If you need to make a value from a list (or sub-list) sensitive, you can set the high-level key as sensitive to suppress the entire tree output.

## Ignore Manifest Fields

You can configure a list of yaml keys to ignore changes to via the `ignore_fields` field.
Set these for fields set by Operators or other processes in kubernetes and as such you don't want to update.

By default, the following control fields are ignored:
  - `status`
  - `metadata.finalizers`
  - `metadata.initializers`
  - `metadata.ownerReferences`
  - `metadata.creationTimestamp`
  - `metadata.generation`
  - `metadata.resourceVersion`
  - `metadata.uid`
  - `metadata.annotations.kubectl.kubernetes.io/last-applied-configuration`

These syntax matches the Terraform style flattened-map syntax, whereby keys are separated by `.` paths.

For example, to ignore the `annotations`, set the `ignore_fields` path to `metadata.annotations`:

```hcl
resource "kubectl_manifest" "test" {
    yaml_body = <<YAML
apiVersion: v1
kind: ServiceAccount
metadata:
  name: name-here
  namespace: default
  annotations:
    this.should.be.ignored: "true"
YAML

    ignore_fields = ["metadata.annotations"]
}
```

For arrays, the syntax is indexed based on the element position. For example, to ignore the `caBundle` field in the
below manifest, would be: `webhooks.0.clientConfig.caBundle`

```hcl
resource "kubectl_manifest" "test" {
    yaml_body = <<YAML
apiVersion: admissionregistration.k8s.io/v1beta1
kind: MutatingWebhookConfiguration
metadata:
  name: istio-sidecar-injector
webhooks:
  - clientConfig:
      caBundle: ""
YAML

    ignore_fields = ["webhooks.0.clientConfig.caBundle"]
}
```

More examples can be found in the provider tests.

## Waiting for Rollout

By default, this resource will wait for `Deployment`, `DaemonSet`, `StatefulSet` & `APIService` to complete their rollout before proceeding.
You can disable this behavior by setting the `wait_for_rollout` field to `false`.

## Timeouts

The resource supports `create`, `update`, and `delete` timeouts via a standard Terraform
`timeouts` block. Each defaults to **10 minutes** and bounds the corresponding
`wait_for_rollout` / `wait_for` wait when that operation is in flight. Workloads that take
longer than ten minutes to roll out (Windows images, large StatefulSets, anything pulling a
multi-gigabyte image) should raise the relevant key.

```hcl
resource "kubectl_manifest" "my_deployment" {
  timeouts {
    create = "30m"
    update = "30m"
    delete = "5m"
  }

  yaml_body = <<YAML
apiVersion: apps/v1
kind: Deployment
# ...
YAML
}
```

`update` covers in-place changes to the manifest (the most common case for long rollouts —
scaling, image bumps, env tweaks). `create` covers the initial apply. `delete` bounds resource
deletion.

## Import

This provider supports importing existing resources. The ID format expected uses a double `//` as a deliminator (as apiVersion can have a forward-slash):

```shell
# Import the my-namespace Namespace
terraform import kubectl_manifest.my-namespace v1//Namespace//my-namespace

# Import the certmanager Issuer CRD named cluster-selfsigned-issuer-root-ca from the my-namespace namespace
$ terraform import -provider kubectl module.kubernetes.kubectl_manifest.crd-example certmanager.k8s.io/v1alpha1//Issuer//cluster-selfsigned-issuer-root-ca//my-namespace
```
