# A bare `annotations:` (YAML null) must not drift against last-applied (#401).
resource "kubectl_manifest" "test" {
  yaml_body = <<YAML
apiVersion: v1
kind: Service
metadata:
  name: name-here
  namespace: default
  annotations:
  labels:
    app.kubernetes.io/name: name-here
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: name-here
  ports:
  - name: grpc
    port: 80
    targetPort: 4245
YAML
}
