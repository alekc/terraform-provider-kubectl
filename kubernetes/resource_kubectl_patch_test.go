package kubernetes

import (
	"bytes"
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"log"
	"strings"
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
					rs, ok := state.RootModule().Resources["kubectl_patch.test"]
					if !ok {
						return fmt.Errorf("not found %v", objectName)
					}

					name, ns, err := idParts(rs.Primary.ID)
					if err != nil {
						return err
					}
					fmt.Println(name, ns)
					// provider := testAccProvider.Meta().(*KubeProvider)
					// provider.DynamicClient()
					// provider := testAccProvider.Meta().(*KubeProvider)
					// factory := cmdutil.NewFactory(provider)
					// r := factory.NewBuilder().
					// 	Unstructured().
					// 	ContinueOnError().
					// 	NamespaceParam("default").DefaultNamespace().
					// 	ResourceTypeOrNameArgs(
					// 		false,
					// 		state.g,
					// 		objectName).
					// 	Flatten().
					// 	Do()
					// cl, err := provider.DynamicClient()
					// if err != nil {
					// 	return err
					// }
					// // get
					// provider.ToRESTMapper()

					return nil
				},
			},
		},
	})
}

func idParts(id string) (string, string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 2 {
		err := fmt.Errorf("Unexpected ID format (%q), expected %q.", id, "namespace/name")
		return "", "", err
	}

	return parts[0], parts[1], nil
}
