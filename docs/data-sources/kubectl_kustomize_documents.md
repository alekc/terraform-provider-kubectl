# Data Source: kubectl_kustomize_documents

This provider provides a `data` resource `kubectl_kustomize_documents` to run `kustomize build` against a target directory and surface the rendered YAML documents to Terraform. Wraps the `sigs.k8s.io/kustomize/api` library, so no external `kustomize` binary is required at apply time.

## Example Usage

### Apply rendered manifests via for_each (recommended)

```hcl
data "kubectl_kustomize_documents" "manifests" {
  target = "${path.module}/kustomize/overlays/prod"
}

resource "kubectl_manifest" "this" {
  for_each  = { for i, doc in data.kubectl_kustomize_documents.manifests.documents : i => doc }
  yaml_body = each.value
}
```

### Pin the kustomize loader and add the managed-by label

```hcl
data "kubectl_kustomize_documents" "manifests" {
  target               = "${path.module}/kustomize/overlays/prod"
  load_restrictor      = "rootOnly"
  add_managed_by_label = true
}
```

## Argument Reference

* `target` - Required. Path to the kustomization directory, evaluated relative to the Terraform working directory. Same semantics as `kustomize build <target>` on the command line.
* `load_restrictor` - Optional. Kustomize loader restriction. `rootOnly` (default) prevents bases from loading files outside the target directory; `none` removes the restriction. An empty string falls back to the default. Any other non-empty value is rejected at config-validate time.
* `add_managed_by_label` - Optional. When true, kustomize stamps an `app.kubernetes.io/managed-by` label on every rendered resource. Default false.

## Attribute Reference

* `documents` - List of YAML documents (list[string]) in kustomize build order. Best used with `count` or a `for_each` expression as shown above.
* `id` - sha256 fingerprint of the rendered document list. Stable across plans when the rendered output is unchanged.
