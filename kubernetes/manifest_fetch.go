package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/alekc/terraform-provider-kubectl/yaml"
)

// ManifestFetchResult is the serialized form of a Kubernetes object plus any
// user-requested field extractions, returned by FetchManifest.
type ManifestFetchResult struct {
	YAML    string
	JSON    string
	UID     string
	Results map[string]string
}

// ErrManifestNotFound is returned by FetchManifest when the target object
// does not exist on the cluster. Callers should surface this as a diag error
// (data sources) or open-time error (ephemeral resources).
var ErrManifestNotFound = fmt.Errorf("manifest not found")

// BuildSelfLinkID returns the deterministic identifier the kubectl_manifest
// data source uses for state. Shape:
//
//	<apiVersion>/<namespace>/<kind>/<name>
//
// Cluster-scoped objects collapse the namespace segment to empty. The shape
// is part of the data source's state contract; changing it forces a replace
// on every consumer, so treat it as stable.
func BuildSelfLinkID(apiVersion, namespace, kind, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s", apiVersion, namespace, kind, name)
}

// FetchManifest fetches a single Kubernetes object by GVK + name (+ namespace)
// and optionally extracts user-supplied dot-path fields into a string map.
// Shared by the SDK v2 data source and the plugin-framework ephemeral resource.
//
// namespace may be empty. The underlying client helper defaults namespaced
// resources to "default" and ignores the namespace for cluster-scoped kinds.
//
// fields maps a user-chosen key to a dot-and-bracket path
// expression (e.g. "spec.replicas",
// "spec.template.spec.containers[0].image",
// `metadata.labels["app.kubernetes.io/name"]`). See
// path_walker.go for the full grammar. Missing paths cause
// FetchManifest to return an error naming the offending key.
func FetchManifest(
	ctx context.Context,
	provider *KubeProvider,
	apiVersion, kind, name, namespace string,
	fields map[string]string,
) (*ManifestFetchResult, error) {
	lookup := &meta_v1_unstruct.Unstructured{}
	lookup.SetAPIVersion(apiVersion)
	lookup.SetKind(kind)
	lookup.SetName(name)
	if namespace != "" {
		lookup.SetNamespace(namespace)
	}
	manifest := yaml.NewFromUnstructured(lookup)

	clientResult := GetRestClientFromUnstructured(manifest, provider)
	if clientResult.Error != nil {
		return nil, clientResult.Error
	}

	obj, err := clientResult.ResourceInterface.Get(ctx, name, meta_v1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s %s/%s", ErrManifestNotFound, apiVersion, kind, namespace, name)
		}
		return nil, fmt.Errorf("failed to read %s/%s %s/%s: %w", apiVersion, kind, namespace, name, err)
	}

	jsonBytes, err := obj.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal fetched object to JSON: %w", err)
	}

	yamlStr, err := yaml.NewFromUnstructured(obj).AsYAML()
	if err != nil {
		return nil, fmt.Errorf("failed to render fetched object as YAML: %w", err)
	}

	// extractFields walks the already-decoded unstructured content
	// directly rather than re-parsing jsonBytes; obj.UnstructuredContent
	// is the same map[string]interface{} json.Unmarshal would produce
	// on the JSON we just rendered, but without the marshal / unmarshal
	// round-trip.
	results, err := extractFields(obj.UnstructuredContent(), fields)
	if err != nil {
		return nil, err
	}

	return &ManifestFetchResult{
		YAML:    yamlStr,
		JSON:    string(jsonBytes),
		UID:     string(obj.GetUID()),
		Results: results,
	}, nil
}

// extractFields walks each user-supplied dot-and-bracket path on the
// pre-decoded unstructured doc and stringifies the value. The walker
// returns (value, found) explicitly: a path that does not resolve
// produces an error naming the offending key. A path that resolves
// to a JSON null stringifies to the empty string, matching the
// stringifyValue contract. Scalars become their natural string form
// via strconv (no scientific notation for large floats); maps and
// slices are JSON-encoded so callers can `jsondecode()` to recover
// structure.
func extractFields(doc interface{}, fields map[string]string) (map[string]string, error) {
	if len(fields) == 0 {
		return map[string]string{}, nil
	}

	out := make(map[string]string, len(fields))
	for key, path := range fields {
		value, found, err := ExtractByPath(doc, path)
		if err != nil {
			return nil, fmt.Errorf("fields[%q]: %w", key, err)
		}
		if !found {
			return nil, fmt.Errorf("fields[%q]: path %q not found in fetched object", key, path)
		}
		s, err := stringifyValue(value)
		if err != nil {
			return nil, fmt.Errorf("fields[%q]: %w", key, err)
		}
		out[key] = s
	}
	return out, nil
}

// stringifyValue renders a walked value as a Terraform string. A
// JSON null comes through as the empty string (consistent with
// extractFields' docstring contract). Floats use strconv with -1
// precision so integer-valued large floats (e.g. metadata.generation
// past 1e6, which json.Unmarshal decodes to float64) render as plain
// decimals rather than fmt.Sprintf's %v default of scientific
// notation. Other scalars route through strconv; complex values
// (maps, slices, anything else) go through json.Marshal so callers
// can `jsondecode()` to recover structure.
func stringifyValue(v interface{}) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32), nil
	case int:
		return strconv.FormatInt(int64(t), 10), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "", fmt.Errorf("failed to JSON-encode value: %w", err)
		}
		return string(b), nil
	}
}
