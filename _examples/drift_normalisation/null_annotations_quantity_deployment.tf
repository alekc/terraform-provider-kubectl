# Null annotations plus the controller's revision annotation, and quantities
# the apiserver rewrites (1 -> "1", 0.5 -> "500m", 1024Mi -> "1Gi"), #401.
resource "kubectl_manifest" "test" {
  wait_for_rollout = false
  yaml_body        = <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: name-here
  namespace: default
  annotations:
spec:
  replicas: 0
  selector:
    matchLabels:
      app: name-here
  template:
    metadata:
      labels:
        app: name-here
    spec:
      initContainers:
      - name: init
        image: registry.k8s.io/pause:3.10
        resources:
          limits:
            cpu: 1
            memory: 1Gi
          requests:
            cpu: 0.5
            memory: "1024Mi"
      containers:
      - name: main
        image: registry.k8s.io/pause:3.10
        resources:
          limits:
            cpu: 2
            memory: 512Mi
YAML
}
