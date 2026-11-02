# Null labels and annotations on a Namespace: the apiserver re-adds
# kubernetes.io/metadata.name after apply, which must not drift (#401).
resource "kubectl_manifest" "test" {
  yaml_body = <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: name-here
  annotations:
  labels:
YAML
}
