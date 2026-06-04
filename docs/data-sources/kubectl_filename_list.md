# Data Source: kubectl_filename_list

This provider provides a `data` resource `kubectl_filename_list` to enable ease of working with directories of kubernetes manifests.

## Example Usage

```hcl
data "kubectl_filename_list" "manifests" {
    pattern = "./manifests/*.yaml"
}

resource "kubectl_manifest" "test" {
    count     = length(data.kubectl_filename_list.manifests.matches)
    yaml_body = file(element(data.kubectl_filename_list.manifests.matches, count.index))
}
```

## Attribute Reference

* `matches` - List of matching file names.
* `id` - sha256 fingerprint of the matched filenames, used as the Terraform resource id. Stable across plans when the matched list is unchanged. See [Upgrading from v2](#upgrading-from-v2) for the v2-to-v3 hash format change.

## Upgrading from v2

v3 changes the input passed to the `id` hash. v2 concatenated each entry with its bare index (`strconv.Itoa(i) + s`) and hashed the result, which left a small ambiguity window between lists like `["1foo"]` and `["", "1foo"]`. v3 length-prefixes each entry (`fmt.Sprintf("%d:%d:%s\n", i, len(s), s)`) so distinct lists always produce distinct hashes and the hash matches the sister `kubectl_kustomize_documents` data source.

The new hash shares no pre-images with the old one, so every existing state-stored `id` rotates on the first `terraform plan` after upgrading. Data sources re-read on every plan anyway, so for almost all users this is invisible churn that resolves itself on the same plan. The only case that needs action is configurations that wired `id` into a downstream `triggers` map:

```hcl
resource "null_resource" "render" {
  triggers = {
    manifests = data.kubectl_filename_list.docs.id
  }
}
```

Such resources will force-recreate once on the first post-upgrade apply. If that is undesirable, either replace the trigger with a content-derived hash you compute in HCL, or use `terraform state` surgery to align the trigger value to the new id before the apply.
