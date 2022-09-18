package kubernetes

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/cli-runtime/pkg/resource"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"log"
	"reflect"
	"strings"
)

var patchTypes = map[string]types.PatchType{"json": types.JSONPatchType, "merge": types.MergePatchType, "strategic": types.StrategicMergePatchType}

func resourceKubectlPatch() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceKubectlPatchCreate,
		ReadContext: func(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
			return nil
		},
		DeleteContext: func(ctx context.Context, data *schema.ResourceData, meta interface{}) diag.Diagnostics {
			return nil
		},
		UpdateContext: func(ctx context.Context, data *schema.ResourceData, meta interface{}) diag.Diagnostics {
			return nil
		},
		Schema: map[string]*schema.Schema{
			"type": {
				Type:        schema.TypeString,
				Description: "Object to patch, i.e. secret, configmap, etc",
				Required:    true,
				ForceNew:    true,
			},
			"name": {
				Type:        schema.TypeString,
				Description: "Name of the object which should be patched",
				Required:    true,
				ForceNew:    true,
			},
			"namespace": {
				Type:        schema.TypeString,
				Description: "Namespace of the object which should be patched",
				Optional:    true,
				ForceNew:    true,
			},
			"patch_type": {
				Type:        schema.TypeString,
				Description: "Type of the patch. Can be json, merge, strategic",
				Default:     "strategic",
				Optional:    true,
			},
			"patch": {
				Type:        schema.TypeString,
				Description: "The patch to be applied to the resource JSON file.",
				Required:    true,
			},
			"field_manager": {
				Type:        schema.TypeString,
				Description: "Field manager value (who is applying the change)",
				Default:     "terraform_kubectl_patch",
				Optional:    true,
			},
			"patch_condition": {
				Type:        schema.TypeMap,
				Description: "If not empty, kubectl_patch will check for a given condition before running the apply operation",
				Optional:    true,
			},
			"fail_if_unchanged": {
				Type:        schema.TypeBool,
				Description: "If set to true, the operation will fail if the contents of the target object were not changed. Defaults to false",
				Optional:    true,
				Default:     false,
			},
		},
	}
}
func resourceKubectlPatchCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	provider := meta.(*KubeProvider)
	var err error
	factory := cmdutil.NewFactory(provider)

	patchType := patchTypes[strings.ToLower(d.Get("patch_type").(string))]
	if patchType == "" {
		log.Printf("[ERROR] invalid patch type: %+v", d.Get("patch_type"))
		return diag.FromErr(fmt.Errorf("Unsupported patch type %v", d.Get("patch_type")))
	}
	objectType := d.Get("type").(string)
	objectName := d.Get("name").(string)
	namespace := d.Get("namespace").(string)
	if namespace == "" {
		namespace = "default"
	}
	patchBytes := []byte(d.Get("patch").(string))
	patchBytes, err = yaml.ToJSON(patchBytes)
	if err != nil {
		log.Printf("[ERROR] invalid yaml xxx: %+v", err)
		return diag.FromErr(err)
	}

	r := factory.NewBuilder().
		Unstructured().
		ContinueOnError().
		NamespaceParam(namespace).DefaultNamespace().
		ResourceTypeOrNameArgs(
			false,
			objectType,
			objectName).
		Flatten().
		Do()
	if err := r.Err(); err != nil {
		return diag.FromErr(err)
	}
	err = r.Visit(func(info *resource.Info, err error) error {
		if err != nil {
			return err
		}
		mapping := info.ResourceMapping()
		client, err := factory.UnstructuredClientForMapping(mapping)
		if err != nil {
			return err
		}
		helper := resource.
			NewHelper(client, mapping).
			DryRun(false).
			WithFieldManager(d.Get("field_manager").(string))
		patchedObj, err := helper.Patch(
			info.Namespace,
			info.Name,
			patchType,
			patchBytes,
			nil,
		)
		if err != nil {
			return err
		}
		// check if there is a requirement for an object to be changed
		if d.Get("fail_if_unchanged").(bool) {
			didPatch := !reflect.DeepEqual(info.Object, patchedObj)
			if !didPatch {
				return fmt.Errorf("object was not affected by the patch")
			}
		}
		rawObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(patchedObj)
		if err != nil {
			return err
		}
		// find the object id
		id, found, err := unstructured.NestedString(rawObject, "metadata", "uid")
		switch {
		case err != nil:
			return err
		case !found:
			return fmt.Errorf("object not found post patch")
		default:
			d.SetId(id)
		}

		return nil
	})
	return diag.FromErr(err)
}
