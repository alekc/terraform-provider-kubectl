# Quantities under spec.hard written as a number and as a non-canonical string.
resource "kubectl_manifest" "test" {
  yaml_body = <<YAML
apiVersion: v1
kind: ResourceQuota
metadata:
  name: name-here
  namespace: default
spec:
  hard:
    cpu: 4
    requests.cpu: 0.5
    memory: "2048Mi"
    pods: 10
YAML
}
