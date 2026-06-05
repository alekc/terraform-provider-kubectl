package kubernetes

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStringifyValue(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"string", "hello", "hello"},
		{"empty string", "", ""},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int", 42, "42"},
		{"int32", int32(-5), "-5"},
		{"int64", int64(-7), "-7"},
		{"uint", uint(99), "99"},
		{"uint64", uint64(99), "99"},
		{"float64 integral", float64(3), "3"},
		{"float64 fractional", 3.14, "3.14"},
		{"map encoded as JSON", map[string]interface{}{"a": "b", "c": float64(1)}, `{"a":"b","c":1}`},
		{"slice of strings encoded as JSON", []interface{}{"a", "b"}, `["a","b"]`},
		{"nested map+slice encoded as JSON", map[string]interface{}{"x": []interface{}{float64(1), float64(2)}}, `{"x":[1,2]}`},
		{"nil renders as empty string", nil, ""},
		{"float64 large integral does not use scientific notation", float64(10000000), "10000000"},
		{"float64 very large integral", float64(1234567890), "1234567890"},
		{"int64 large", int64(1234567890123), "1234567890123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stringifyValue(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractFields(t *testing.T) {
	bodyJSON := `{
      "metadata": {
        "name": "demo",
        "labels": {"app": "nginx", "tier": "web"}
      },
      "spec": {
        "replicas": 3,
        "active": true,
        "lastScheduleTime": null,
        "containers": [
          {"name": "main", "image": "nginx:1.25"},
          {"name": "sidecar", "image": "envoy:1.30", "resources": null}
        ]
      }
    }`
	var body interface{}
	require.NoError(t, json.Unmarshal([]byte(bodyJSON), &body))

	t.Run("nil fields returns empty map without parsing the body", func(t *testing.T) {
		got, err := extractFields(body, nil)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{}, got)
	})

	t.Run("empty fields returns empty map", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{}, got)
	})

	t.Run("scalar string", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"name": "metadata.name"})
		require.NoError(t, err)
		assert.Equal(t, "demo", got["name"])
	})

	t.Run("scalar number stringifies without decimal point", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"r": "spec.replicas"})
		require.NoError(t, err)
		assert.Equal(t, "3", got["r"])
	})

	t.Run("scalar bool", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"a": "spec.active"})
		require.NoError(t, err)
		assert.Equal(t, "true", got["a"])
	})

	t.Run("array element via numeric index", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{
			"first":  "spec.containers.[0].image",
			"second": "spec.containers.[1].image",
		})
		require.NoError(t, err)
		assert.Equal(t, "nginx:1.25", got["first"])
		assert.Equal(t, "envoy:1.30", got["second"])
	})

	t.Run("map value is JSON-encoded and round-trippable via jsondecode", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"labels": "metadata.labels"})
		require.NoError(t, err)

		var labels map[string]string
		require.NoError(t, json.Unmarshal([]byte(got["labels"]), &labels))
		assert.Equal(t, map[string]string{"app": "nginx", "tier": "web"}, labels)
	})

	t.Run("nested object is JSON-encoded and round-trippable", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"first_container": "spec.containers.[0]"})
		require.NoError(t, err)

		var container map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(got["first_container"]), &container))
		assert.Equal(t, "main", container["name"])
		assert.Equal(t, "nginx:1.25", container["image"])
	})

	t.Run("null object field is extracted as empty string", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"last_schedule": "spec.lastScheduleTime"})
		require.NoError(t, err)
		assert.Equal(t, "", got["last_schedule"])
	})

	t.Run("null array element field is extracted as empty string", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"resources": "spec.containers.[1].resources"})
		require.NoError(t, err)
		assert.Equal(t, "", got["resources"])
	})

	t.Run("bare array index against null field is extracted as empty string", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{"resources": "spec.containers.1.resources"})
		require.NoError(t, err)
		assert.Equal(t, "", got["resources"])
	})

	t.Run("large integer-valued float does not render in scientific notation", func(t *testing.T) {
		bigJSON := `{"spec":{"generation":10000000}}`
		var bigBody interface{}
		require.NoError(t, json.Unmarshal([]byte(bigJSON), &bigBody))
		got, err := extractFields(bigBody, map[string]string{"gen": "spec.generation"})
		require.NoError(t, err)
		assert.Equal(t, "10000000", got["gen"])
	})

	t.Run("missing path returns error naming the field key", func(t *testing.T) {
		_, err := extractFields(body, map[string]string{"oops": "spec.doesnt.exist"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `fields["oops"]`)
		assert.Contains(t, err.Error(), `"spec.doesnt.exist"`)
	})

	t.Run("out-of-range array index returns not found", func(t *testing.T) {
		_, err := extractFields(body, map[string]string{"oops": "spec.containers.[2].image"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `fields["oops"]`)
		assert.Contains(t, err.Error(), `"spec.containers.[2].image"`)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("traversal through null returns not found", func(t *testing.T) {
		_, err := extractFields(body, map[string]string{"oops": "spec.lastScheduleTime.foo"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `fields["oops"]`)
		assert.Contains(t, err.Error(), `"spec.lastScheduleTime.foo"`)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("multiple fields extracted independently", func(t *testing.T) {
		got, err := extractFields(body, map[string]string{
			"name":  "metadata.name",
			"image": "spec.containers.[0].image",
		})
		require.NoError(t, err)
		assert.Equal(t, "demo", got["name"])
		assert.Equal(t, "nginx:1.25", got["image"])
	})
}

func TestBuildSelfLinkID(t *testing.T) {
	cases := []struct {
		name                                      string
		apiVersion, namespace, kind, resourceName string
		want                                      string
	}{
		{
			"namespaced core resource",
			"v1", "default", "ConfigMap", "demo",
			"v1/default/ConfigMap/demo",
		},
		{
			"cluster-scoped resource has empty namespace segment",
			"v1", "", "Namespace", "kube-system",
			"v1//Namespace/kube-system",
		},
		{
			"grouped api with namespace",
			"apps/v1", "web", "Deployment", "nginx",
			"apps/v1/web/Deployment/nginx",
		},
		{
			"CRD instance",
			"cert-manager.io/v1", "tls", "Certificate", "wildcard",
			"cert-manager.io/v1/tls/Certificate/wildcard",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, BuildSelfLinkID(tc.apiVersion, tc.namespace, tc.kind, tc.resourceName))
		})
	}
}
