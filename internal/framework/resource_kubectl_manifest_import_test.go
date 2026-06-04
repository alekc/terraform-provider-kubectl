package framework

import (
	"strings"
	"testing"
)

// TestParseManifestImportID exercises the ID parser used by the
// kubectl_manifest importer. The double-slash delimiter is load-bearing
// because apiVersion can contain a single slash (e.g. apps/v1).
func TestParseManifestImportID(t *testing.T) {
	cases := []struct {
		name          string
		id            string
		wantAPI       string
		wantKind      string
		wantName      string
		wantNamespace string
		wantErr       string
	}{
		{
			name:          "cluster-scoped v1",
			id:            "v1//Namespace//imported-ns",
			wantAPI:       "v1",
			wantKind:      "Namespace",
			wantName:      "imported-ns",
			wantNamespace: "",
		},
		{
			name:          "namespaced apps/v1",
			id:            "apps/v1//Deployment//my-app//app-ns",
			wantAPI:       "apps/v1",
			wantKind:      "Deployment",
			wantName:      "my-app",
			wantNamespace: "app-ns",
		},
		{
			name:          "namespaced CRD with slashed apiVersion",
			id:            "cert-manager.io/v1//Issuer//selfsigned-root//cert-manager",
			wantAPI:       "cert-manager.io/v1",
			wantKind:      "Issuer",
			wantName:      "selfsigned-root",
			wantNamespace: "cert-manager",
		},
		{
			name:    "too few parts",
			id:      "v1//Namespace",
			wantErr: "expected ID in format",
		},
		{
			name:    "too many parts",
			id:      "v1//Namespace//foo//bar//baz",
			wantErr: "expected ID in format",
		},
		{
			name:    "empty apiVersion",
			id:      "//Namespace//imported-ns",
			wantErr: "must all be non-empty",
		},
		{
			name:    "empty kind",
			id:      "v1////imported-ns",
			wantErr: "must all be non-empty",
		},
		{
			name:    "empty name",
			id:      "v1//Namespace//",
			wantErr: "must all be non-empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, kind, name, ns, err := parseManifestImportID(tc.id)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if api != tc.wantAPI {
				t.Errorf("apiVersion: got %q, want %q", api, tc.wantAPI)
			}
			if kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", kind, tc.wantKind)
			}
			if name != tc.wantName {
				t.Errorf("name: got %q, want %q", name, tc.wantName)
			}
			if ns != tc.wantNamespace {
				t.Errorf("namespace: got %q, want %q", ns, tc.wantNamespace)
			}
		})
	}
}

// TestBuildManifestImportYAMLStub verifies the minimal YAML the importer
// hands to yaml.ParseYAML for GVK discovery. The stub must include
// metadata.namespace iff a namespace was supplied; namespaced GETs against
// cluster-scoped objects (and vice versa) fail at the dynamic-client
// boundary.
func TestBuildManifestImportYAMLStub(t *testing.T) {
	t.Run("cluster-scoped omits namespace", func(t *testing.T) {
		got := buildManifestImportYAMLStub("v1", "Namespace", "imported-ns", "")
		want := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: imported-ns\n"
		if got != want {
			t.Fatalf("cluster-scoped stub mismatch\ngot:\n%s\nwant:\n%s", got, want)
		}
		if strings.Contains(got, "namespace:") {
			t.Errorf("cluster-scoped stub must not include namespace key, got %q", got)
		}
	})

	t.Run("namespaced includes namespace", func(t *testing.T) {
		got := buildManifestImportYAMLStub("apps/v1", "Deployment", "my-app", "app-ns")
		want := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  namespace: app-ns\n  name: my-app\n"
		if got != want {
			t.Fatalf("namespaced stub mismatch\ngot:\n%s\nwant:\n%s", got, want)
		}
	})
}
