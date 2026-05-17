package kubernetes

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceKubectlManifest() *schema.Resource {
	return &schema.Resource{
		Description: "Reads a single Kubernetes object from the cluster by apiVersion + kind + name (+ namespace) " +
			"and optionally extracts user-supplied fields by dot-path. " +
			"Outputs are not marked sensitive at the schema level; callers needing redaction should set " +
			"`sensitive = true` on the consuming output block or wrap references with `sensitive(...)`. " +
			"For guaranteed non-persistence to state, use the `kubectl_manifest` ephemeral resource instead.",
		ReadContext: dataSourceKubectlManifestRead,
		Schema: map[string]*schema.Schema{
			"api_version": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The API version of the resource to read (e.g. `v1`, `apps/v1`).",
			},
			"kind": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The Kind of the resource to read (e.g. `ConfigMap`, `Deployment`).",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The metadata.name of the resource to read.",
			},
			"namespace": {
				Type:     schema.TypeString,
				Optional: true,
				Default:  "",
				Description: "The metadata.namespace of the resource. Leave empty for cluster-scoped kinds; " +
					"for namespaced kinds an empty value defaults to `default`.",
			},
			"fields": {
				Type:     schema.TypeMap,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Description: "Map of result-key to gojsonq dot-path expressions to extract from the fetched " +
					"object (e.g. `replicas = \"spec.replicas\"`, `image = \"spec.template.spec.containers.0.image\"`). " +
					"Each path must resolve; missing paths produce an error.",
			},

			"yaml": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The fetched object serialised as YAML.",
			},
			"json": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The fetched object serialised as JSON.",
			},
			"uid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The metadata.uid of the fetched object.",
			},
			"results": {
				Type:     schema.TypeMap,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Description: "Map of extracted field values keyed by the names declared in `fields`. " +
					"Scalar values are stringified; objects and arrays are JSON-encoded.",
			},
		},
	}
}

func dataSourceKubectlManifestRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	provider, ok := meta.(*KubeProvider)
	if !ok || provider == nil {
		return diag.Errorf("kubectl_manifest: provider not configured (expected *KubeProvider, got %T)", meta)
	}

	apiVersion := d.Get("api_version").(string)
	kind := d.Get("kind").(string)
	name := d.Get("name").(string)
	namespace := d.Get("namespace").(string)

	fields := map[string]string{}
	if raw, ok := d.GetOk("fields"); ok {
		for k, v := range raw.(map[string]interface{}) {
			fields[k] = fmt.Sprintf("%v", v)
		}
	}

	result, err := FetchManifest(ctx, provider, apiVersion, kind, name, namespace, fields)
	if err != nil {
		if errors.Is(err, ErrManifestNotFound) {
			return diag.Errorf("kubectl_manifest: %s", err)
		}
		return diag.FromErr(err)
	}

	d.SetId(buildSelfLinkID(apiVersion, namespace, kind, name))
	if err := d.Set("yaml", result.YAML); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("json", result.JSON); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("uid", result.UID); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("results", result.Results); err != nil {
		return diag.FromErr(err)
	}
	return nil
}

// buildSelfLinkID returns a deterministic id of the form
//
//	<apiVersion>/<namespace>/<kind>/<name>
//
// Cluster-scoped objects collapse the namespace segment to empty.
func buildSelfLinkID(apiVersion, namespace, kind, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s", apiVersion, namespace, kind, name)
}
