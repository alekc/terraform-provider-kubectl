package kubernetes

import (
	"strings"
	"testing"
)

// TestParsePathTemplate covers the HCL template renderer the framework-side
// kubectl_path_documents data source uses. Pure function tests; no
// filesystem or cluster.
func TestParsePathTemplate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		template string
		vars     map[string]string
		want     string
		wantErr  string
	}{
		{
			name:     "no interpolation passes through",
			template: "kind: ConfigMap\nmetadata:\n  name: plain\n",
			want:     "kind: ConfigMap\nmetadata:\n  name: plain\n",
		},
		{
			name:     "single var substitution",
			template: `name: ${cluster}`,
			vars:     map[string]string{"cluster": "prod"},
			want:     `name: prod`,
		},
		{
			name:     "multiple vars + stdlib function",
			template: `region: ${upper(region)}-${env}`,
			vars:     map[string]string{"region": "eu", "env": "prod"},
			want:     "region: EU-prod",
		},
		{
			name:     "yamlencode roundtrip",
			template: `tags: ${yamlencode(split(",", tags))}`,
			vars:     map[string]string{"tags": "a,b,c"},
			want:     "tags: - \"a\"\n- \"b\"\n- \"c\"\n",
		},
		{
			name:     "undefined var produces error",
			template: `name: ${unset}`,
			wantErr:  "Unknown variable",
		},
		{
			name:     "malformed HCL produces error",
			template: `name: ${`,
			wantErr:  "Missing expression",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePathTemplate(tc.template, tc.vars)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error: got %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidatePathDocumentsVars exercises the primitive-only enforcement on
// the vars / sensitive_vars maps. Pure helper, no framework wiring.
func TestValidatePathDocumentsVars(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   map[string]any
		wantErr string
	}{
		{
			name:  "all scalars ok",
			input: map[string]any{"a": "x", "b": "y"},
		},
		{
			name:  "mixed primitive types ok",
			input: map[string]any{"s": "hi", "n": 42, "b": true, "f": 3.14},
		},
		{
			name:  "nil value tolerated",
			input: map[string]any{"a": nil},
		},
		{
			name:    "untyped list value rejected",
			input:   map[string]any{"a": []any{"x", "y"}},
			wantErr: "a (list)",
		},
		{
			name:    "concrete []string rejected via reflect",
			input:   map[string]any{"a": []string{"x", "y"}},
			wantErr: "a (list)",
		},
		{
			name:    "fixed-size array rejected",
			input:   map[string]any{"a": [2]int{1, 2}},
			wantErr: "a (list)",
		},
		{
			name:    "untyped map value rejected",
			input:   map[string]any{"a": map[string]any{"k": "v"}},
			wantErr: "a (map)",
		},
		{
			name:    "concrete map[string]string rejected via reflect",
			input:   map[string]any{"a": map[string]string{"k": "v"}},
			wantErr: "a (map)",
		},
		{
			name:    "concrete map[int]int rejected via reflect",
			input:   map[string]any{"a": map[int]int{1: 2}},
			wantErr: "a (map)",
		},
		{
			name:  "empty map is fine",
			input: map[string]any{},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePathDocumentsVars("vars", tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error: got %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
