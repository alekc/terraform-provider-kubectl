# A custom resource with null annotations, an int-or-string field and a
# quantity-like map: CRDs keep strict comparison and must not drift (#401).
resource "kubectl_manifest" "test" {
  depends_on = [kubectl_manifest.definecrd]
  yaml_body  = <<YAML
apiVersion: drifttest.kubectl.alekc.dev/v1
kind: DriftWidget
metadata:
  name: name-here
  namespace: default
  annotations:
spec:
  replicas: 1
  surge: 1
  ratio: "0.5"
  tags:
  limits:
    cpu: 1
    memory: "1024Mi"
YAML
}

resource "kubectl_manifest" "definecrd" {
  yaml_body = <<YAML
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: driftwidgets.drifttest.kubectl.alekc.dev
spec:
  group: drifttest.kubectl.alekc.dev
  scope: Namespaced
  names:
    kind: DriftWidget
    plural: driftwidgets
    singular: driftwidget
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              replicas:
                type: integer
              surge:
                x-kubernetes-int-or-string: true
              ratio:
                type: string
              tags:
                type: array
                nullable: true
                items:
                  type: string
              limits:
                type: object
                additionalProperties:
                  x-kubernetes-int-or-string: true
YAML
}
