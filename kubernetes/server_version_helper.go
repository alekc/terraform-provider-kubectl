package kubernetes

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/version"
)

// ServerVersionInfo holds the parsed result of a Kubernetes apiserver
// discovery `ServerVersion()` call. The framework-side `kubectl_server_version`
// resource and data source both consume this struct so their state shapes
// stay in sync.
type ServerVersionInfo struct {
	// ID is sha256 of the raw server-version string. Used as the Terraform
	// resource/data-source ID so plans converge while the apiserver returns
	// the same version.
	ID string

	// Version is the apiserver's reported semver with both build metadata
	// (everything after "+") and any pre-release suffix (everything after
	// the first "-") stripped. For example "v1.32.1-alpha.0+abcdef" and
	// "v1.32.1+k3s1" both reduce to "v1.32.1".
	Version string

	Major      string
	Minor      string
	Patch      string
	GitVersion string
	GitCommit  string
	BuildDate  string
	Platform   string
}

// FetchServerVersion queries the cluster's discovery API and returns the
// parsed version info. The discovery client's cache is invalidated before the
// call so callers always observe the current apiserver build. Parsing
// delegates to parseServerVersion, which is a pure function and unit-tested
// in server_version_helper_test.go.
func FetchServerVersion(provider *KubeProvider) (*ServerVersionInfo, error) {
	// Defensive: an upstream caller could pass a typed-nil *KubeProvider
	// after an `ok`-style assertion on the mux callback (the assertion
	// succeeds for a nil pointer of the right type). Without this guard we
	// would panic in provider.ToDiscoveryClient() instead of producing a
	// useful diagnostic.
	if provider == nil {
		return nil, errors.New("kubectl_server_version: provider is nil; framework Configure() never ran or SDK v2 meta is missing")
	}

	discoveryClient, err := provider.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	if discoveryClient == nil {
		return nil, errors.New("kubectl_server_version: ToDiscoveryClient returned a nil client without an error")
	}

	discoveryClient.Invalidate()
	serverVersion, err := discoveryClient.ServerVersion()
	if err != nil {
		return nil, err
	}

	return parseServerVersion(serverVersion), nil
}

// parseServerVersion converts an apiserver `*version.Info` into the flat
// ServerVersionInfo state shape. Pure function: same input maps to the same
// output every time, and ID is a deterministic sha256 of the raw version
// string so successive reads against an unchanged cluster yield the same ID.
//
// Both the canonical `Version` field and the synthesised `Patch` field are
// reported with SemVer 2.0.0 pre-release and build-metadata suffixes stripped.
// Build metadata is normalised first (everything after a `+`) so distributions
// like K3s (`v1.32.1+k3s1`) and downstream-rebuild RKE2 / OpenShift / EKS
// stamps don't leak into the Patch attribute. Pre-release suffixes (`-rc.0`,
// `-alpha.0`, etc.) are stripped after that.
//
// When the cleaned string contains fewer than three dot-separated segments
// (an extremely rare malformed case) the parser falls back to the `Major` /
// `Minor` fields supplied by apimachinery and leaves `Patch` empty.
func parseServerVersion(v *version.Info) *ServerVersionInfo {
	raw := v.String()

	// SemVer build metadata first (everything after "+"), then pre-release
	// suffix (everything after "-"). After this both transformations the
	// remainder is a clean "vMAJOR.MINOR.PATCH" string suitable for the
	// dot-split.
	noBuild := strings.SplitN(raw, "+", 2)[0]
	clean := strings.SplitN(noBuild, "-", 2)[0]

	info := &ServerVersionInfo{
		ID:         fmt.Sprintf("%x", sha256.Sum256([]byte(raw))),
		Version:    clean,
		GitVersion: v.GitVersion,
		GitCommit:  v.GitCommit,
		BuildDate:  v.BuildDate,
		Platform:   v.Platform,
	}

	semver := strings.Split(clean, ".")
	if len(semver) >= 3 {
		info.Major = strings.ReplaceAll(semver[0], "v", "")
		info.Minor = semver[1]
		info.Patch = semver[2]
	} else {
		info.Major = v.Major
		info.Minor = v.Minor
		info.Patch = ""
	}

	return info
}
