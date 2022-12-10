resource "kubectl_manifest" "deployment" {
  yaml_body = <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .name }}
  namespace: {{ .namespace }}
spec:
  replicas: 2
  selector:
    matchLabels:
      app: caddy
  template:
    metadata:
      labels:
        app: caddy
    spec:
      containers:
      - name: {{ .name }}-ctr
        image: caddy:alpine
EOF
}

resource "kubectl_patch" "test" {
  depends_on = [kubectl_manifest.deployment]
  name       = "{{ .name }}"
  type       = "deployment"
  namespace = "{{ .namespace }}"
  patch      = <<EOF
{"spec": {"replicas": 2}}
EOF
}