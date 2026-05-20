package kubernetes

import (
	"testing"

	"k8s.io/apimachinery/pkg/version"
)

// TestParseServerVersion is a pure-function table test for parseServerVersion.
// No discovery client involved; every case is a synthetic `version.Info`
// covering the parser's branches and the edge cases real apiservers emit
// (pre-release suffixes, build metadata, missing `v` prefix, malformed
// short strings).
func TestParseServerVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		input       version.Info
		wantVersion string
		wantMajor   string
		wantMinor   string
		wantPatch   string
	}{
		{
			name: "vanilla three-segment",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "v1.32.1",
			},
			wantVersion: "v1.32.1",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "1",
		},
		{
			name: "pre-release suffix stripped",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "v1.32.1-alpha.0",
			},
			wantVersion: "v1.32.1",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "1",
		},
		{
			name: "build metadata after dash is also stripped",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "v1.32.1-rc.0+abc123",
			},
			wantVersion: "v1.32.1",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "1",
		},
		{
			name: "k3s build metadata without pre-release",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "v1.32.1+k3s1",
			},
			wantVersion: "v1.32.1",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "1",
		},
		{
			name: "rke2 build metadata without pre-release",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "v1.32.0+rke2r1",
			},
			wantVersion: "v1.32.0",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "0",
		},
		{
			name: "openshift / downstream rebuild stamp",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "v1.32.0+abc1234",
			},
			wantVersion: "v1.32.0",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "0",
		},
		{
			name: "no v prefix is tolerated",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "1.32.1",
			},
			wantVersion: "1.32.1",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "1",
		},
		{
			name: "kind k8s style with hash suffix",
			input: version.Info{
				Major:      "1",
				Minor:      "35",
				GitVersion: "v1.35.1-rc.0+abcdef0123",
			},
			wantVersion: "v1.35.1",
			wantMajor:   "1",
			wantMinor:   "35",
			wantPatch:   "1",
		},
		{
			name: "minor with trailing plus is preserved untouched",
			input: version.Info{
				Major:      "1",
				Minor:      "32+",
				GitVersion: "v1.32.1",
			},
			wantVersion: "v1.32.1",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "1",
		},
		{
			name: "fallback path: GitVersion lacks two dots",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "v1.32",
			},
			wantVersion: "v1.32",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "",
		},
		{
			name: "fallback path: GitVersion empty",
			input: version.Info{
				Major:      "1",
				Minor:      "32",
				GitVersion: "",
			},
			wantVersion: "",
			wantMajor:   "1",
			wantMinor:   "32",
			wantPatch:   "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseServerVersion(&tc.input)
			if got.Version != tc.wantVersion {
				t.Errorf("Version: got %q, want %q", got.Version, tc.wantVersion)
			}
			if got.Major != tc.wantMajor {
				t.Errorf("Major: got %q, want %q", got.Major, tc.wantMajor)
			}
			if got.Minor != tc.wantMinor {
				t.Errorf("Minor: got %q, want %q", got.Minor, tc.wantMinor)
			}
			if got.Patch != tc.wantPatch {
				t.Errorf("Patch: got %q, want %q", got.Patch, tc.wantPatch)
			}
			if got.ID == "" {
				t.Errorf("ID empty; expected sha256 hex")
			}
			if len(got.ID) != 64 {
				t.Errorf("ID length %d, want 64 (sha256 hex)", len(got.ID))
			}
		})
	}
}

// TestParseServerVersion_IDStability guarantees parseServerVersion produces
// the same ID for identical inputs, which the framework state-id contract
// relies on for plan convergence.
func TestParseServerVersion_IDStability(t *testing.T) {
	t.Parallel()
	in := &version.Info{Major: "1", Minor: "32", GitVersion: "v1.32.1"}
	first := parseServerVersion(in).ID
	second := parseServerVersion(in).ID
	if first != second {
		t.Fatalf("ID not stable across calls: %q vs %q", first, second)
	}
}

// TestFetchServerVersion_NilGuard verifies the function returns a descriptive
// error rather than panicking when the caller passes a nil provider. This
// matters because the framework type-assertion pattern
// `meta.(*KubeProvider)` returns `ok=true` for a typed-nil pointer, so the
// guard is the actual safety net.
func TestFetchServerVersion_NilGuard(t *testing.T) {
	t.Parallel()
	info, err := FetchServerVersion(nil)
	if err == nil {
		t.Fatal("expected error for nil provider, got nil")
	}
	if info != nil {
		t.Errorf("expected nil ServerVersionInfo when error is returned, got %#v", info)
	}
}

// TestParseServerVersion_PassesThroughOpaqueFields confirms GitCommit,
// BuildDate, and Platform are surfaced verbatim - the parser only mutates
// the version-string fields, never the metadata fields.
func TestParseServerVersion_PassesThroughOpaqueFields(t *testing.T) {
	t.Parallel()
	in := &version.Info{
		Major:      "1",
		Minor:      "32",
		GitVersion: "v1.32.1",
		GitCommit:  "deadbeefcafebabe",
		BuildDate:  "2026-01-01T00:00:00Z",
		Platform:   "linux/amd64",
	}
	got := parseServerVersion(in)
	if got.GitCommit != in.GitCommit {
		t.Errorf("GitCommit: got %q, want %q", got.GitCommit, in.GitCommit)
	}
	if got.BuildDate != in.BuildDate {
		t.Errorf("BuildDate: got %q, want %q", got.BuildDate, in.BuildDate)
	}
	if got.Platform != in.Platform {
		t.Errorf("Platform: got %q, want %q", got.Platform, in.Platform)
	}
	if got.GitVersion != in.GitVersion {
		t.Errorf("GitVersion: got %q, want %q", got.GitVersion, in.GitVersion)
	}
}
