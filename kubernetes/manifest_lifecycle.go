package kubernetes

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/alekc/terraform-provider-kubectl/internal/types"
	"github.com/alekc/terraform-provider-kubectl/yaml"
	backoff "github.com/cenkalti/backoff/v4"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apimachinery_types "k8s.io/apimachinery/pkg/types"
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

// DriftEngine names the algorithm used to detect drift between the
// desired manifest and the live cluster object. ClientDriftEngine is the
// default; ServerDriftEngine is opt-in.
type DriftEngine string

const (
	// ClientDriftEngine compares the user-provided manifest against the
	// live object client-side, flattening both into dotted paths and
	// comparing per-key. Fast (no extra API calls), but susceptible to
	// false drift on arrays, server-side defaulting, and admission
	// webhook mutations.
	ClientDriftEngine DriftEngine = "client"

	// ServerDriftEngine runs an SSA dry-run patch against the apiserver
	// and uses the response (the apiserver's view of the post-apply
	// object) as the desired side of the comparison. Same semantics as
	// `kubectl diff`. Costs one extra API call per Read. Falls back to
	// the client engine on patch failure (e.g. CRDs that don't accept
	// ApplyPatchType, webhook rejection, RBAC missing PATCH verb).
	ServerDriftEngine DriftEngine = "server"
)

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
	IgnoreFields      []string       // for drift calculation
	// SensitiveFields lists dotted paths whose values are masked in
	// any [DEBUG]-level log emission of the manifest. Defaults to
	// "data" and "stringData" on Secret v1 when empty; same semantics
	// as BuildObfuscatedYAML. Apply does not otherwise interpret it.
	SensitiveFields []string

	// ShowDriftValues controls how drifted leaf values appear in the
	// `drift` attribute. "none" (default) shows paths only;
	// "hash" shows short-hash markers; "full" shows literal before /
	// after values. Secret kinds and MaskPaths still mask regardless
	// of mode. See kubernetes/drift.go for the rendering rules.
	ShowDriftValues ShowMode
	// MaskPaths lists glob-paths whose leaves render as a sensitive
	// marker in the `drift` attribute, on top of the Secret data /
	// stringData auto-masking.
	MaskPaths []string
	// DriftEngine selects the detection algorithm. Empty == client.
	DriftEngine DriftEngine
}

// ApplyManifestResult captures everything the caller needs to write back
// to state after a successful apply. All values must be persisted.
type ApplyManifestResult struct {
	SelfLink string
	UID      string // from the apply response
	LiveUID  string // from the post-wait read
	// Drift is a human-readable YAML subtree containing only the paths
	// where the desired manifest differs from the live object, masked
	// per ShowDriftValues / MaskPaths / Secret-kind rules. Empty string
	// means no drift detected. Persisted into the `drift` state
	// attribute. See kubernetes/drift.go.
	Drift string
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

	log.Printf("[INFO] %s perform apply of manifest", manifest)

	// applyAndFetch is the unit-of-retry: discover the REST client for
	// the manifest's GroupVersionKind, build a fresh ApplyOptions
	// (apply.ApplyOptions tracks visitor state internally and cannot
	// be safely re-run), run apply once, then re-read the object so
	// the caller can persist its fingerprint. Retried under
	// exponential backoff when provider.ApplyRetryCount > 0; called
	// once directly otherwise. The retryCount == 0 short-circuit is
	// for intent and readability (issue #228); the backoff path would
	// also produce exactly one attempt for N = 0 because Retry calls
	// the operation before consulting NextBackOff.
	//
	// Discovery is inside the retry boundary so that the
	// CRD-then-instance race (issue #274 / _examples/crds/couchbase.tf)
	// can recover when apply_retry_count is set: a freshly applied CRD
	// is not always visible to the apiserver's discovery endpoint by
	// the time the dependent instance's apply runs.
	//
	// restClient is the last successful REST client; rawResponse the
	// last successful Get. Both are nil until at least one attempt
	// writes them, and the retry loop returns a non-nil error before
	// any caller can read a stale value, so partial-write leak is
	// impossible.
	var restClient *RestClientResult
	var rawResponse *meta_v1_unstruct.Unstructured
	applyAndFetch := func() error {
		// Use the ctx-aware variant so a cancelled parent ctx
		// (Ctrl-C, resource timeout, plan abort) bounds the whole
		// retry attempt, not just the inter-retry sleep. Plain
		// GetRestClientFromUnstructured has its own 60s timer that
		// ignores ctx; an in-flight discovery on a slow cluster
		// could otherwise hang each attempt for up to 60s after
		// cancellation.
		restClient = GetRestClientFromUnstructuredWithContext(ctx, manifest, provider)
		if restClient.Error != nil {
			return fmt.Errorf("%v failed to create kubernetes rest client for update of resource: %+v", manifest, restClient.Error)
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

		if err := applyOptions.Run(); err != nil {
			return fmt.Errorf("%v failed to run apply: %+v", manifest, err)
		}
		log.Printf("[INFO] %v manifest applied, fetch resource from kubernetes", manifest)
		var err error
		rawResponse, err = restClient.ResourceInterface.Get(ctx, manifest.GetName(), meta_v1.GetOptions{})
		if err != nil {
			return fmt.Errorf("%v failed to fetch resource from kubernetes: %+v", manifest, err)
		}
		return nil
	}

	if provider.ApplyRetryCount == 0 {
		if err := applyAndFetch(); err != nil {
			return nil, err
		}
	} else {
		exp := backoff.NewExponentialBackOff()
		exp.InitialInterval = 3 * time.Second
		exp.MaxInterval = 30 * time.Second
		// RandomizationFactor = 0 makes MaxInterval a hard ceiling.
		// Default 0.5 jitter is applied after MaxInterval clamps the
		// base interval, so NextBackOff can return up to 1.5 *
		// MaxInterval; users tuning apply_retry_count expect the
		// documented 30s ceiling to actually hold.
		exp.RandomizationFactor = 0
		// MaxElapsedTime = 0 disables the wall-clock stop condition so
		// WithMaxRetries below is the only thing bounding the loop.
		// Default 15 minutes would silently truncate retries on slow
		// clusters with large N.
		exp.MaxElapsedTime = 0
		// WithContext makes a cancelled ctx (Ctrl-C, resource timeout,
		// parent plan abort) interrupt the inter-retry sleep instead
		// of waiting out the full backoff window. Without this wrap,
		// backoff.Retry's internal getContext falls back to
		// context.Background() and a user who hits Ctrl-C during a
		// 30s backoff would hang for the remainder of that sleep.
		policy := backoff.WithContext(
			backoff.WithMaxRetries(exp, provider.ApplyRetryCount),
			ctx,
		)
		retryErr := backoff.Retry(func() error {
			if err := applyAndFetch(); err != nil {
				log.Printf("[ERROR] applying manifest failed: %+v", err)
				return err
			}
			return nil
		}, policy)
		if retryErr != nil {
			return nil, retryErr
		}
	}
	response := yaml.NewFromUnstructured(rawResponse)

	result := &ApplyManifestResult{
		SelfLink: response.GetSelfLink(),
		UID:      string(response.GetUID()),
		LiveUID:  string(response.GetUID()),
		// Immediately post-apply the live object is, by definition,
		// what the user just sent (plus any server-side defaulting
		// pulled in by the apply path). Drift on the post-wait
		// re-read below may still surface non-empty content if a
		// controller mutates the object during the wait window;
		// applyResultToModel persists the post-wait value as
		// authoritative. Apply path always uses the client engine
		// here because we already have the apply response in hand;
		// running an extra SSA dry-run would be redundant.
		Drift: computeDriftClient(manifest, response, opts.IgnoreFields, opts.MaskPaths, opts.ShowDriftValues),
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

	// Re-read after waits so live_uid and drift reflect any post-wait
	// mutations the controllers introduced (status fields, defaulting,
	// admission webhooks, etc.).
	readResult, err := readManifestUsingClient(ctx, restClient.ResourceInterface, manifest, opts.IgnoreFields, opts.MaskPaths, opts.ShowDriftValues, opts.DriftEngine, opts.FieldManager, opts.ForceConflicts) //nolint:contextcheck // ctx is the apply ctx, intentionally reused for the post-wait read
	if err != nil {
		return nil, err
	}
	if readResult.Found {
		result.LiveUID = readResult.LiveUID
		// Post-wait drift is the authoritative view.
		result.Drift = readResult.Drift
	}

	return result, nil
}

// ReadManifestOptions captures everything ReadManifest reads from the
// caller. All fields are plain types.
type ReadManifestOptions struct {
	YAMLBody          string
	OverrideNamespace string
	IgnoreFields      []string
	// ShowDriftValues + MaskPaths control the rendering of the Drift
	// field in the result. Defaults (zero values) are safe: ShowNone
	// mode and no extra masks.
	ShowDriftValues ShowMode
	MaskPaths       []string
	// DriftEngine selects the detection algorithm. Empty == client.
	DriftEngine DriftEngine
	// FieldManager + ForceConflicts feed into the SSA dry-run patch
	// when DriftEngine is "server". Same defaults as the apply path:
	// FieldManager "kubectl" if empty; ForceConflicts false.
	FieldManager   string
	ForceConflicts bool
}

// ReadManifestResult captures the live state observed during a Read.
// Found = false means the resource no longer exists in the cluster; the
// caller should clear it from state.
type ReadManifestResult struct {
	Found       bool
	InvalidType bool // RestClientInvalidTypeError
	LiveUID     string
	// Drift is a human-readable YAML subtree of paths where desired
	// differs from live. Empty string means in sync. Same rules as
	// ApplyManifestResult.Drift.
	Drift string
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

	return readManifestUsingClient(ctx, restClient.ResourceInterface, manifest, opts.IgnoreFields, opts.MaskPaths, opts.ShowDriftValues, opts.DriftEngine, opts.FieldManager, opts.ForceConflicts)
}

// readManifestUsingClient is the inner Read used both by ReadManifest and
// by ApplyManifest's post-wait re-read. Takes an already-resolved client
// so the apply path can reuse the one it built.
func readManifestUsingClient(ctx context.Context, client dynamic.ResourceInterface, manifest *yaml.Manifest, ignoreFields, maskPaths []string, showMode ShowMode, engine DriftEngine, fieldManager string, forceConflicts bool) (*ReadManifestResult, error) {
	rawResponse, err := client.Get(ctx, manifest.GetName(), meta_v1.GetOptions{})
	if err != nil {
		if k8serrors.IsGone(err) || k8serrors.IsNotFound(err) {
			return &ReadManifestResult{Found: false}, nil
		}
		return nil, fmt.Errorf("%v failed to get resource from kubernetes: %+v", manifest, err)
	}
	if rawResponse.GetUID() == "" {
		return nil, fmt.Errorf("%v failed to parse item and get UUID: %+v", manifest, rawResponse)
	}

	live := yaml.NewFromUnstructured(rawResponse)
	return &ReadManifestResult{
		Found:   true,
		LiveUID: string(live.GetUID()),
		Drift:   computeDrift(ctx, client, manifest, live, ignoreFields, maskPaths, showMode, engine, fieldManager, forceConflicts),
	}, nil
}

// computeDrift routes the drift calculation to either the client- or
// server-side engine. The server engine runs an SSA dry-run patch
// against the apiserver and uses the response as the desired side of
// the comparison; on any failure (RBAC, webhook rejection, CRD that
// doesn't accept ApplyPatchType) it logs a [WARN] and falls back to the
// client engine so Read still completes successfully.
func computeDrift(ctx context.Context, client dynamic.ResourceInterface, desired, live *yaml.Manifest, ignoreFields, maskPaths []string, mode ShowMode, engine DriftEngine, fieldManager string, forceConflicts bool) string {
	if engine == ServerDriftEngine {
		serverDesired, err := serverSideApplyDryRun(ctx, client, desired, fieldManager, forceConflicts)
		if err == nil {
			return RenderDrift(serverDesired.Object, live.Raw.Object, DriftOptions{
				IgnoreFields: ignoreFields,
				MaskPaths:    maskPaths,
				ShowMode:     mode,
				Kind:         desired.GetKind(),
				APIVersion:   desired.GetAPIVersion(),
			})
		}
		// Log the failure mode without echoing the raw error: a
		// rejected dry-run apply (admission webhook, validating
		// schema) can include the manifest body or other field
		// values in its message, which would defeat the drift
		// renderer's secret masking. The user can rerun with
		// TF_LOG=trace to get the full error from k8s client logs.
		log.Printf("[WARN] %v/%v server-side drift engine failed (%s), falling back to client engine", desired.GetAPIVersion(), desired.GetKind(), classifySSAError(err))
		// fall through to client engine
	}
	return computeDriftClient(desired, live, ignoreFields, maskPaths, mode)
}

// classifySSAError maps an SSA dry-run failure to a short, non-sensitive
// label suitable for [WARN] logs. The raw error from the apiserver may
// embed the offending manifest content (validation messages quote
// rejected values verbatim, webhook denials sometimes echo payload
// fragments), so the renderer's mask_paths / Secret protections would
// be defeated by logging it. Categories are coarse on purpose: enough
// for an operator to know whether to grant PATCH RBAC, audit webhooks,
// or check the CRD's SSA schema support; the full error is still
// available via the standard k8s client TRACE-log path.
func classifySSAError(err error) string {
	if err == nil {
		return "no-error"
	}
	if k8serrors.IsForbidden(err) || k8serrors.IsUnauthorized(err) {
		return "rbac-denied"
	}
	if k8serrors.IsBadRequest(err) || k8serrors.IsInvalid(err) {
		return "invalid-or-unsupported"
	}
	if k8serrors.IsNotFound(err) || k8serrors.IsMethodNotSupported(err) {
		return "patch-unsupported"
	}
	if k8serrors.IsTimeout(err) || k8serrors.IsServerTimeout(err) || k8serrors.IsServiceUnavailable(err) {
		return "transient"
	}
	return "apiserver-error"
}

// computeDriftClient is the client-side drift engine: flatten desired and
// live, exclude ignore_fields and the built-in control fields, render
// per-key diffs as YAML. Handles the Secret v1 stringData -> data
// normalisation on a deep copy of the desired manifest, so the caller's
// pointer is never mutated (issue #269 class).
func computeDriftClient(desired, live *yaml.Manifest, ignoreFields, maskPaths []string, mode ShowMode) string {
	if desired == nil || desired.Raw == nil || live == nil || live.Raw == nil {
		return ""
	}
	desiredObj := desired.Raw.Object
	if desired.GetKind() == "Secret" && desired.GetAPIVersion() == "v1" {
		if stringData, found := desiredObj["stringData"]; found {
			if sdMap, ok := stringData.(map[string]interface{}); ok {
				desiredObj = desired.Raw.DeepCopy().Object
				for k, v := range sdMap {
					encoded := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%v", v)))
					if err := meta_v1_unstruct.SetNestedField(desiredObj, encoded, "data", k); err != nil {
						log.Printf("[WARN] computeDriftClient: failed to encode Secret stringData.%s: %v", k, err)
						continue
					}
				}
				meta_v1_unstruct.RemoveNestedField(desiredObj, "stringData")
			}
		}
	}
	return RenderDrift(desiredObj, live.Raw.Object, DriftOptions{
		IgnoreFields: ignoreFields,
		MaskPaths:    maskPaths,
		ShowMode:     mode,
		Kind:         desired.GetKind(),
		APIVersion:   desired.GetAPIVersion(),
	})
}

// serverSideApplyDryRun issues an SSA dry-run patch against the
// apiserver and returns the response (the apiserver's view of what the
// post-apply object would look like). The fieldManager and forceConflicts
// values must match the values used by the real apply path so the
// dry-run computes the same patch the apply would produce; without that
// alignment the dry-run is from a different field manager's perspective
// and the drift signal is inaccurate.
//
// Defaults: empty fieldManager becomes "kubectl" to match the apply path.
func serverSideApplyDryRun(ctx context.Context, client dynamic.ResourceInterface, desired *yaml.Manifest, fieldManager string, forceConflicts bool) (*meta_v1_unstruct.Unstructured, error) {
	if desired == nil || desired.Raw == nil {
		return nil, fmt.Errorf("nil manifest")
	}
	if fieldManager == "" {
		fieldManager = "kubectl"
	}
	body, err := desired.Raw.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal manifest for dry-run: %w", err)
	}
	patchOpts := meta_v1.PatchOptions{
		FieldManager: fieldManager,
		DryRun:       []string{meta_v1.DryRunAll},
	}
	if forceConflicts {
		// Force ownership transfer in dry-run mirrors what the apply
		// path would do; without this the dry-run can fail with
		// conflicts that a real apply would have resolved.
		f := true
		patchOpts.Force = &f
	}
	return client.Patch(ctx, desired.GetName(), apimachinery_types.ApplyPatchType, body, patchOpts)
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
	// Normalize before the Secret v1 default check. Without this,
	// sensitiveFields = [""] (a common shape from a misconfigured HCL
	// list or a templated variable that resolves to "") would make
	// len(fields) != 0 and silently suppress the default "data" /
	// "stringData" masking on a Secret manifest, leaking the very
	// payload the masking exists to hide.
	fields := NormalizeSensitiveFields(sensitiveFields)
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

// NormalizeSensitiveFields returns s with empty / whitespace-only
// entries removed. Returns nil rather than an empty slice so callers
// that use `len(out) == 0` to detect "no fields" (e.g. for the
// Secret v1 default in BuildObfuscatedYAML) behave the same way they
// would for an unset input. SDK v2 and plugin-framework adapters
// call this on user input from the sensitive_fields attribute
// before populating ApplyManifestOptions / DeleteManifestOptions, so
// a misconfigured `sensitive_fields = [""]` collapses to nil rather
// than masking the default-secret-field path.
func NormalizeSensitiveFields(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	resourceGone := k8serrors.IsGone(err) || k8serrors.IsNotFound(err)
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
