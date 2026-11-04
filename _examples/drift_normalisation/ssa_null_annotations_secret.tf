# Server-side apply with null annotations and a null data next to stringData:
# the managedFields ownership checks must not report drift after apply (#402).
resource "kubectl_manifest" "test" {
  server_side_apply = true
  yaml_body         = <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: name-here
  namespace: default
  annotations:
data:
stringData:
  token: value
YAML
}
