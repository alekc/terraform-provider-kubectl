package kubernetes

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/alekc/terraform-provider-kubectl/internal/types"
	"github.com/alekc/terraform-provider-kubectl/yaml"
	"k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/printers"
	k8sresource "k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	k8sdelete "k8s.io/kubectl/pkg/cmd/delete"
	"k8s.io/kubectl/pkg/validation"
	yamlWriter "sigs.k8s.io/yaml"
)

// manifest_lifecycle.go: pure-function implementations of the
// kubectl_manifest CRUD lifecycle. Each function takes a struct of plain
// inputs and returns a struct of plain outputs, so the caller (SDK v2
// *schema.ResourceData or plugin-framework state) handles its own state
// shaping. Used by both halves of the muxed provider during the v2 -> v3
// framework migration (issue #295).

// ApplyManifestOptions captures everything ApplyManifest reads from the
// caller. All fields are plain types so the framework half can construct
// the options without depending on the SDK v2 schema package.
type ApplyManifestOptions struct {
	YAMLBody          string
	OverrideNamespace string // "" means use the namespace from yaml_body
	ValidateSchema    bool   // pass false to skip schema validation
	ServerSideApply   bool
	FieldManager      string // only consulted if ServerSideApply
	ForceConflicts    bool   // only consulted if ServerSideApply
	WaitForRollout    bool
	WaitFor           *types.WaitFor // nil if no wait_for block
	Timeout           time.Duration  // applies to wait_for_rollout and wait_for
	IgnoreFields      []string       // for fingerprint calc
	// SensitiveFields lists dotted paths whose values are masked in
	// any [DEBUG]-level log emission of the manifest. Defaults to
	// "data" and "stringData" on Secret v1 when empty; same semantics
	// as BuildObfuscatedYAML. Apply does not otherwise interpret it.
	SensitiveFields []string
}

// ApplyManifestResult captures everything the caller needs to write back
// to state after a successful apply. All five values must be persisted.
type ApplyManifestResult struct {
	SelfLink                         string
	UID                              string // from the apply response
	LiveUID                          string // from the post-wait read
	YAMLInClusterFingerprint         string // from the apply response
	LiveManifestInClusterFingerprint string // from the post-wait read
}

// ApplyManifest runs `kubectl apply` against the given manifest, optionally
// waits for rollout / user-supplied conditions, and returns the final state
// the caller should persist.
//
// Errors are returned verbatim; the caller is responsible for wrapping
// them in framework / SDK v2 diagnostics.
func ApplyManifest(ctx context.Context, provider *KubeProvider, opts ApplyManifestOptions) (*ApplyManifestResult, error) {
	manifest, err := yaml.ParseYAML(opts.YAMLBody)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubernetes resource: %+v", err)
	}
	if opts.OverrideNamespace != "" {
		manifest.SetNamespace(opts.OverrideNamespace)
	}

	log.Printf("[DEBUG] %v apply kubernetes resource:\n%s",
		manifest, obfuscateForLog(opts.YAMLBody, opts.OverrideNamespace, opts.SensitiveFields))

	restClient := GetRestClientFromUnstructured(manifest, provider)
	if restClient.Error != nil {
		return nil, fmt.Errorf("%v failed to create kubernetes rest client for update of resource: %+v", manifest, restClient.Error)
	}

	// Re-serialise after the namespace override so the temp file matches
	// what we'll be diffing against the live state.
	yamlBody, err := manifest.AsYAML()
	if err != nil {
		return nil, fmt.Errorf("%v failed to convert to yaml: %+v", manifest, err)
	}

	tmpfile, err := os.CreateTemp("", "*kubectl_manifest.yaml")
	if err != nil {
		return nil, fmt.Errorf("%v failed to create temp file for apply: %+v", manifest, err)
	}
	defer func() {
		if rmErr := os.Remove(tmpfile.Name()); rmErr != nil {
			log.Printf("[WARN] failed to remove temp file %s: %v", tmpfile.Name(), rmErr)
		}
	}()
	if _, err := tmpfile.Write([]byte(yamlBody)); err != nil {
		return nil, fmt.Errorf("%v failed to write temp file for apply: %+v", manifest, err)
	}
	if err := tmpfile.Close(); err != nil {
		return nil, fmt.Errorf("%v failed to close temp file for apply: %+v", manifest, err)
	}

	applyOptions := NewApplyOptions(yamlBody)
	applyOptions.Builder = k8sresource.NewBuilder(k8sresource.RESTClientGetter(provider))
	applyOptions.DeleteOptions = &k8sdelete.DeleteOptions{
		FilenameOptions: k8sresource.FilenameOptions{
			Filenames: []string{tmpfile.Name()},
		},
	}
	applyOptions.ToPrinter = func(string) (printers.ResourcePrinter, error) {
		return printers.NewDiscardingPrinter(), nil
	}
	if !opts.ValidateSchema {
		applyOptions.Validator = validation.NullSchema{}
	}
	if opts.ServerSideApply {
		applyOptions.ServerSideApply = true
		applyOptions.FieldManager = opts.FieldManager
	}
	if opts.ForceConflicts {
		applyOptions.ForceConflicts = true
	}
	if manifest.HasNamespace() {
		applyOptions.Namespace = manifest.GetNamespace()
	}

	log.Printf("[INFO] %s perform apply of manifest", manifest)

	if err := applyOptions.Run(); err != nil {
		return nil, fmt.Errorf("%v failed to run apply: %+v", manifest, err)
	}

	log.Printf("[INFO] %v manifest applied, fetch resource from kubernetes", manifest)

	rawResponse, err := restClient.ResourceInterface.Get(ctx, manifest.GetName(), meta_v1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("%v failed to fetch resource from kubernetes: %+v", manifest, err)
	}
	response := yaml.NewFromUnstructured(rawResponse)

	result := &ApplyManifestResult{
		SelfLink:                         response.GetSelfLink(),
		UID:                              string(response.GetUID()),
		LiveUID:                          string(response.GetUID()),
		YAMLInClusterFingerprint:         GetLiveManifestFields(opts.IgnoreFields, manifest, response),
		LiveManifestInClusterFingerprint: GetLiveManifestFields(opts.IgnoreFields, manifest, response),
	}

	if opts.WaitForRollout {
		switch {
		case manifest.GetKind() == "Deployment":
			log.Printf("[INFO] %v waiting for Deployment rollout for %vmin", manifest, opts.Timeout.Minutes())
			if err := WaitForDeploymentRollout(ctx, provider, manifest.GetNamespace(), manifest.GetName(), opts.Timeout); err != nil {
				return nil, err
			}
		case manifest.GetKind() == "DaemonSet":
			log.Printf("[INFO] %v waiting for DaemonSet rollout for %vmin", manifest, opts.Timeout.Minutes())
			if err := WaitForDaemonSetRollout(ctx, provider, manifest.GetNamespace(), manifest.GetName(), opts.Timeout); err != nil {
				return nil, err
			}
		case manifest.GetKind() == "StatefulSet":
			log.Printf("[INFO] %v waiting for StatefulSet rollout for %vmin", manifest, opts.Timeout.Minutes())
			if err := WaitForStatefulSetRollout(ctx, provider, manifest.GetNamespace(), manifest.GetName(), opts.Timeout); err != nil {
				return nil, err
			}
		case manifest.GetKind() == "APIService" && manifest.GetAPIVersion() == "apiregistration.k8s.io/v1":
			log.Printf("[INFO] %v waiting for APIService for %vmin", manifest, opts.Timeout.Minutes())
			if err := WaitForApiService(ctx, provider, manifest.GetName(), opts.Timeout); err != nil {
				return nil, err
			}
		}
	}

	if opts.WaitFor != nil {
		if len(opts.WaitFor.Field) == 0 && len(opts.WaitFor.Condition) == 0 {
			return nil, fmt.Errorf("at least one of `field` or `condition` must be provided in `wait_for` block")
		}
		log.Printf("[INFO] %v waiting for wait conditions for %vmin", manifest, opts.Timeout.Minutes())
		if err := WaitForConditions(ctx, restClient, opts.WaitFor.Field, opts.WaitFor.Condition, manifest.GetName(), opts.Timeout); err != nil {
			return nil, err
		}
	}

	// Re-read after waits so live_uid and live_manifest_incluster reflect
	// any post-wait drift the controllers introduced (status fields, etc.).
	readResult, err := readManifestUsingClient(ctx, restClient.ResourceInterface, manifest, opts.IgnoreFields) //nolint:contextcheck // ctx is the apply ctx, intentionally reused for the post-wait read
	if err != nil {
		return nil, err
	}
	if readResult.Found {
		result.LiveUID = readResult.LiveUID
		result.LiveManifestInClusterFingerprint = readResult.LiveManifestInClusterFingerprint
	}

	return result, nil
}

// ReadManifestOptions captures everything ReadManifest reads from the
// caller. All fields are plain types.
type ReadManifestOptions struct {
	YAMLBody          string
	OverrideNamespace string
	IgnoreFields      []string
}

// ReadManifestResult captures the live state observed during a Read.
// Found = false means the resource no longer exists in the cluster; the
// caller should clear it from state.
type ReadManifestResult struct {
	Found                            bool
	InvalidType                      bool // RestClientInvalidTypeError
	LiveUID                          string
	LiveManifestInClusterFingerprint string
}

// ReadManifest fetches the current state of the manifest from the cluster
// and returns the live UID and fingerprint. Not-found is signalled via
// Found = false rather than an error so the caller can clear state cleanly.
func ReadManifest(ctx context.Context, provider *KubeProvider, opts ReadManifestOptions) (*ReadManifestResult, error) {
	manifest, err := yaml.ParseYAML(opts.YAMLBody)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubernetes resource: %+v", err)
	}
	if opts.OverrideNamespace != "" {
		manifest.SetNamespace(opts.OverrideNamespace)
	}

	restClient := GetRestClientFromUnstructured(manifest, provider)
	if restClient.Status == RestClientInvalidTypeError {
		return &ReadManifestResult{Found: false, InvalidType: true}, nil
	}
	if restClient.Error != nil {
		return nil, fmt.Errorf("failed to create kubernetes rest client for read of resource: %+v", restClient.Error)
	}

	return readManifestUsingClient(ctx, restClient.ResourceInterface, manifest, opts.IgnoreFields)
}

// readManifestUsingClient is the inner Read used both by ReadManifest and
// by ApplyManifest's post-wait re-read. Takes an already-resolved client
// so the apply path can reuse the one it built.
func readManifestUsingClient(ctx context.Context, client dynamic.ResourceInterface, manifest *yaml.Manifest, ignoreFields []string) (*ReadManifestResult, error) {
	rawResponse, err := client.Get(ctx, manifest.GetName(), meta_v1.GetOptions{})
	if err != nil {
		if errors.IsGone(err) || errors.IsNotFound(err) {
			return &ReadManifestResult{Found: false}, nil
		}
		return nil, fmt.Errorf("%v failed to get resource from kubernetes: %+v", manifest, err)
	}
	if rawResponse.GetUID() == "" {
		return nil, fmt.Errorf("%v failed to parse item and get UUID: %+v", manifest, rawResponse)
	}

	live := yaml.NewFromUnstructured(rawResponse)
	return &ReadManifestResult{
		Found:                            true,
		LiveUID:                          string(live.GetUID()),
		LiveManifestInClusterFingerprint: GetLiveManifestFields(ignoreFields, manifest, live),
	}, nil
}

// BuildObfuscatedYAML returns the manifest with any field listed in
// sensitiveFields (or, for Kind: Secret apiVersion: v1, "data" and
// "stringData" by default) replaced by the literal `(sensitive value)`
// string. Useful for surfacing a plan-friendly representation of the
// manifest that does not leak secret values into Terraform plan output.
//
// Only map values are supported; passing a sensitiveFields path that
// resolves to a non-map returns an error.
func BuildObfuscatedYAML(yamlBody, overrideNamespace string, sensitiveFields []string) (string, error) {
	obfuscated, err := yaml.ParseYAML(yamlBody)
	if err != nil {
		return "", err
	}
	if obfuscated.Raw.Object == nil {
		obfuscated.Raw.Object = make(map[string]interface{})
	}
	if overrideNamespace != "" {
		obfuscated.SetNamespace(overrideNamespace)
	}
	fields := sensitiveFields
	if len(fields) == 0 && obfuscated.GetKind() == "Secret" && obfuscated.GetAPIVersion() == "v1" {
		fields = []string{"data", "stringData"}
	}
	for _, s := range fields {
		path := strings.Split(s, ".")
		_, exists, lookupErr := meta_v1_unstruct.NestedFieldNoCopy(obfuscated.Raw.Object, path...)
		if lookupErr != nil {
			return "", fmt.Errorf("failed to access sensitive field %q: %v", s, lookupErr)
		}
		if !exists {
			log.Printf("[TRACE] sensitive field %s skipped, does not exist", s)
			continue
		}
		if setErr := meta_v1_unstruct.SetNestedField(obfuscated.Raw.Object, "(sensitive value)", path...); setErr != nil {
			return "", fmt.Errorf("failed to obfuscate sensitive field %q: %v (only map values are supported)", s, setErr)
		}
	}
	out, marshalErr := yamlWriter.Marshal(obfuscated.Raw.Object)
	if marshalErr != nil {
		return "", fmt.Errorf("failed to serialise obfuscated yaml: %v", marshalErr)
	}
	return string(out), nil
}

// obfuscateForLog returns the manifest YAML with sensitive fields
// masked, suitable for inclusion in [DEBUG] log output. Fails closed:
// if BuildObfuscatedYAML errors (malformed YAML, invalid sensitive
// field path) the raw body is suppressed and a placeholder is
// returned, so secret material never reaches the log even when the
// caller's sensitive_fields config is wrong.
//
// The wrapped error is deliberately NOT included in the returned
// string. BuildObfuscatedYAML surfaces errors from the k8s
// nested-field machinery, which embed the offending value verbatim
// ("supersecret is of the type string, expected ..."); echoing the
// error into the log would defeat the obfuscation. Operators can
// re-derive the failure by running `terraform plan`, where the same
// helper runs against yaml_body_parsed and the error surfaces as a
// regular (non-secret) plan diagnostic.
func obfuscateForLog(yamlBody, overrideNamespace string, sensitiveFields []string) string {
	out, err := BuildObfuscatedYAML(yamlBody, overrideNamespace, sensitiveFields)
	if err != nil {
		return "(yaml obfuscation failed; body suppressed)"
	}
	return out
}

// DeleteManifestOptions captures everything DeleteManifest reads from the
// caller.
type DeleteManifestOptions struct {
	YAMLBody          string
	OverrideNamespace string
	ApplyOnly         bool
	Wait              bool
	DeleteCascade     string // "Background" | "Foreground" | "" (auto)
	Timeout           time.Duration
	// SensitiveFields lists dotted paths whose values are masked in
	// any [DEBUG]-level log emission of the manifest. Defaults to
	// "data" and "stringData" on Secret v1 when empty; same semantics
	// as BuildObfuscatedYAML. Delete does not otherwise interpret it.
	SensitiveFields []string
}

// DeleteManifest deletes the manifest from the cluster and (optionally)
// waits for it to disappear. ApplyOnly = true makes this a no-op.
func DeleteManifest(ctx context.Context, provider *KubeProvider, opts DeleteManifestOptions) error {
	if opts.ApplyOnly {
		return nil
	}
	manifest, err := yaml.ParseYAML(opts.YAMLBody)
	if err != nil {
		return fmt.Errorf("failed to parse kubernetes resource: %+v", err)
	}
	if opts.OverrideNamespace != "" {
		manifest.SetNamespace(opts.OverrideNamespace)
	}

	log.Printf("[DEBUG] %v delete kubernetes resource:\n%s",
		manifest, obfuscateForLog(opts.YAMLBody, opts.OverrideNamespace, opts.SensitiveFields))

	restClient := GetRestClientFromUnstructured(manifest, provider)
	if restClient.Error != nil {
		return fmt.Errorf("%v failed to create kubernetes rest client for delete of resource: %+v", manifest, restClient.Error)
	}

	log.Printf("[INFO] %s perform delete of manifest", manifest)

	var propagationPolicy meta_v1.DeletionPropagation
	if len(opts.DeleteCascade) > 0 {
		propagationPolicy = meta_v1.DeletionPropagation(opts.DeleteCascade)
	} else if opts.Wait {
		propagationPolicy = meta_v1.DeletePropagationForeground
	} else {
		propagationPolicy = meta_v1.DeletePropagationBackground
	}

	err = restClient.ResourceInterface.Delete(ctx, manifest.GetName(), meta_v1.DeleteOptions{PropagationPolicy: &propagationPolicy})
	resourceGone := errors.IsGone(err) || errors.IsNotFound(err)
	if err != nil && !resourceGone {
		return fmt.Errorf("%v failed to delete kubernetes resource: %+v", manifest, err)
	}

	if opts.Wait && !resourceGone {
		log.Printf("[INFO] %s waiting for delete of manifest to complete", manifest)
		if err := WaitForDelete(ctx, restClient, manifest.GetName(), opts.Timeout); err != nil {
			return err
		}
	}

	return nil
}
