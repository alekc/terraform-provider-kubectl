package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/thedevsaddam/gojsonq/v2"
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
// fields maps a user-chosen key to a gojsonq dot-path (e.g. "spec.replicas",
// "spec.template.spec.containers.0.image"). Missing paths cause FetchManifest
// to return an error naming the offending key.
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

	results, err := extractFields(string(jsonBytes), fields)
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

// extractFields runs each user-supplied gojsonq path against the JSON body
// and stringifies the value. Missing paths produce an error naming the
// offending key. Scalars become their natural string form; maps and slices
// are JSON-encoded so callers can `jsondecode()` to recover structure.
func extractFields(jsonBody string, fields map[string]string) (map[string]string, error) {
	if len(fields) == 0 {
		return map[string]string{}, nil
	}

	out := make(map[string]string, len(fields))
	gq := gojsonq.New().FromString(jsonBody)

	for key, path := range fields {
		value := gq.Reset().Find(path)
		if value == nil {
			exists, err := jsonPathExists(jsonBody, path)
			if err != nil {
				return nil, fmt.Errorf("fields[%q]: %w", key, err)
			}
			if !exists {
				return nil, fmt.Errorf("fields[%q]: path %q not found in fetched object", key, path)
			}
		}
		s, err := stringifyValue(value)
		if err != nil {
			return nil, fmt.Errorf("fields[%q]: %w", key, err)
		}
		out[key] = s
	}
	return out, nil
}

// jsonPathExists checks whether a dot-separated path exists in a JSON body.
// Array segments may use bare or bracketed indices, as parsed by parsePathIndex.
// Keep this path parsing behavior aligned with gojsonq path resolution semantics.
func jsonPathExists(jsonBody, path string) (bool, error) {
	var doc interface{}
	if err := json.Unmarshal([]byte(jsonBody), &doc); err != nil {
		return false, fmt.Errorf("failed to parse fetched object JSON: %w", err)
	}

	current := doc
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return false, nil
		}

		switch node := current.(type) {
		case map[string]interface{}:
			value, ok := node[part]
			if !ok {
				return false, nil
			}
			current = value
		case []interface{}:
			index, ok := parsePathIndex(part)
			if !ok || index < 0 || index >= len(node) {
				return false, nil
			}
			current = node[index]
		default:
			return false, nil
		}
	}
	return true, nil
}

// parsePathIndex parses an array index from either "N" or "[N]" path segments.
// It returns the parsed index and whether the segment was a valid index.
func parsePathIndex(part string) (int, bool) {
	if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
		part = strings.TrimPrefix(strings.TrimSuffix(part, "]"), "[")
	}
	index, err := strconv.Atoi(part)
	if err != nil {
		return 0, false
	}
	return index, true
}

func stringifyValue(v interface{}) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool, float64, float32, int, int32, int64, uint, uint32, uint64:
		return fmt.Sprintf("%v", t), nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "", fmt.Errorf("failed to JSON-encode value: %w", err)
		}
		return string(b), nil
	}
}
