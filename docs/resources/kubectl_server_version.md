# Resource: kubectl_server_version

This provider provides a resource `kubectl_server_version` to enable looking up of a kubernetes server version information.
This is exactly the same as the `data` source `kubectl_server_version` but allows you to better chain dependencies, such that data sources are notoriously tricky for use `depends_on` with!
This is particularily helpful if you need to match specific components with the kubernetes server version, e.g. `kube-proxy`.

## Example Usage

```hcl
resource "kubectl_server_version" "current" { }
```

## Argument Reference

* `triggers` - Optional, `map(string)`. Any change to the map forces re-creation of the resource, which refreshes the version fields from the apiserver. Behaves the same as `null_resource.triggers`. **Values must be strings.** Wrap non-string inputs with `tostring()`; see [Upgrading from v2](#upgrading-from-v2) below.

## Attribute Reference

* `version` - Version of the server, e.g. `v1.34.0`.
* `major` - Major version, semver if available, e.g. `1`.
* `minor` - Minor version, semver if available, e.g. `34`.
* `patch` - Patch version, semver if available, e.g. `0`.
* `git_version` - Version of the server, e.g. `v1.34.0-eks-aae39f`.
* `git_commit` - Git sha commit, e.g. `aae39f4697508697bf16c0de4a5687d464f4da81`.
* `build_date` - Date the server binaries were built, e.g. `2025-08-27T08:19:12Z`.
* `platform` - Server platform name, e.g. `linux/amd64`.

## Upgrading from v2

v3 ports `kubectl_server_version` from the Terraform SDK v2 to the plugin framework. The user-visible behaviour is identical except for one contract tightening: the `triggers` attribute is now declared as `map(string)` instead of an untyped map. v2 accepted values of any type and stringified them implicitly at decode time; v3 rejects non-string values at `terraform validate` with `Inappropriate value for attribute "triggers": element [...] must be string`.

Stringify any non-string input with `tostring()`:

```hcl
# v2 (number value, implicitly stringified)
resource "kubectl_server_version" "current" {
  triggers = {
    count = var.cluster_count
  }
}

# v3 (explicit string)
resource "kubectl_server_version" "current" {
  triggers = {
    count = tostring(var.cluster_count)
  }
}
```

This applies to numbers, booleans, and any other non-string scalar that was previously accepted. String-valued triggers configurations are unaffected.
