package kubernetes

import (
	"crypto/sha256"
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

	// Version is the apiserver's reported semver without any pre-release
	// suffix (everything after the first "-" stripped). For example a
	// GitVersion of "v1.32.1-alpha.0+abcdef" becomes "v1.32.1".
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
	discoveryClient, err := provider.ToDiscoveryClient()
	if err != nil {
		return nil, err
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
// The parser strips pre-release / build-metadata suffixes from the canonical
// `Version` field (everything after the first `-`) and from the synthesised
// `Patch` field. When the raw string contains fewer than three
// dot-separated segments (an extremely rare malformed case) the parser falls
// back to the `Major` / `Minor` fields supplied by apimachinery and leaves
// `Patch` empty.
func parseServerVersion(v *version.Info) *ServerVersionInfo {
	raw := v.String()
	info := &ServerVersionInfo{
		ID:         fmt.Sprintf("%x", sha256.Sum256([]byte(raw))),
		Version:    strings.Split(raw, "-")[0],
		GitVersion: v.GitVersion,
		GitCommit:  v.GitCommit,
		BuildDate:  v.BuildDate,
		Platform:   v.Platform,
	}

	semver := strings.Split(raw, ".")
	if len(semver) >= 3 {
		info.Major = strings.ReplaceAll(semver[0], "v", "")
		info.Minor = semver[1]
		info.Patch = strings.Split(semver[2], "-")[0]
	} else {
		info.Major = v.Major
		info.Minor = v.Minor
		info.Patch = ""
	}

	return info
}
