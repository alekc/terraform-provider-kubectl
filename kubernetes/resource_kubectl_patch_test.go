package kubernetes

import (
	"bytes"
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"log"
	"testing"
	"text/template"
)

// parses go template with any variables attached
func parseGoTemplate(fileName string, data any) string {
	t := template.Must(template.ParseFiles(fileName))
	var out bytes.Buffer
	err := t.Execute(&out, data)
	if err != nil {
		// todo: logging
		log.Printf("[ERROR] cannot render go template: %+v", err)
		return ""
	}
	return out.String()
}
func TestAccKubectl_Patch(t *testing.T) {
	// start := time.Now()
	const objectName = "patch-demo-simple"
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccCheckkubectlDestroy,
		Steps: []resource.TestStep{
			{
				Config: parseGoTemplate("test_files/patch/simple_01.tf", map[string]string{
					"namespace": "default",
					"name":      objectName,
				}),
				Check: func(state *terraform.State) error {
					// obtain the name, type, ns from the state
					name, objType, ns, err := nameNsFromState(state, "kubectl_patch.test")
					if err != nil {
						return err
					}
					rawObject, err := readUnstructuredFromK8s(
						testAccProvider.Meta().(*KubeProvider),
						name,
						ns,
						objType)
					if err != nil {
						return err
					}
					// check that the patch worked correctly
					unstruct, err := runtime.DefaultUnstructuredConverter.ToUnstructured(rawObject)
					if err != nil {
						return err
					}
					replicas, b, err := unstructured.NestedInt64(unstruct, "spec", "replicas")
					switch {
					case err != nil:
						return err
					case !b:
						return fmt.Errorf("not found")
					case replicas != 2:
						return fmt.Errorf("Invalid value for spec.replica. Wanted v, got %v", replicas)
					}
					return nil
				},
			},
		},
	})
}

func nameNsFromState(state *terraform.State, resourceName string) (name, objectType, ns string, err error) {
	rs, ok := state.RootModule().Resources[resourceName]
	if !ok {
		err = fmt.Errorf("not found %v", resourceName)
		return
	}

	attributes := rs.Primary.Attributes
	name = attributes["name"]
	objectType = attributes["type"]
	ns = attributes["namespace"]
	return
}
