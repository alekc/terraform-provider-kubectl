package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alekc/terraform-provider-kubectl/flatten"
	"github.com/alekc/terraform-provider-kubectl/internal/types"
	"github.com/alekc/terraform-provider-kubectl/yaml"
	"github.com/thedevsaddam/gojsonq/v2"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/kubectl/pkg/scheme"

	apiMachineryTypes "k8s.io/apimachinery/pkg/types"
	apiregistration "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"k8s.io/kubectl/pkg/cmd/apply"

	apps_v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	meta_v1_unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

const (
	// https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/deployment/util/deployment_util.go#L93
	TimedOutReason = "ProgressDeadlineExceeded"
)


// NewApplyOptions defines flags and other configuration parameters for the `apply` command
func NewApplyOptions(yamlBody string) *apply.ApplyOptions {
	applyOptions := &apply.ApplyOptions{
		PrintFlags: genericclioptions.NewPrintFlags("created").WithTypeSetter(scheme.Scheme),

		IOStreams: genericiooptions.IOStreams{
			In:     strings.NewReader(yamlBody),
			Out:    log.Writer(),
			ErrOut: log.Writer(),
		},

		Overwrite:    true,
		OpenAPIPatch: true,
		Recorder:     genericclioptions.NoopRecorder{},

		VisitedUids:       sets.New[apiMachineryTypes.UID](),
		VisitedNamespaces: sets.New[string](),
	}
	return applyOptions
}


type RestClientStatus int

const (
	RestClientOk = iota
	RestClientGenericError
	RestClientInvalidTypeError
)

type RestClientResult struct {
	ResourceInterface dynamic.ResourceInterface
	Error             error
	Status            RestClientStatus
}

func RestClientResultSuccess(resourceInterface dynamic.ResourceInterface) *RestClientResult {
	return &RestClientResult{
		ResourceInterface: resourceInterface,
		Error:             nil,
		Status:            RestClientOk,
	}
}

func RestClientResultFromErr(err error) *RestClientResult {
	return &RestClientResult{
		ResourceInterface: nil,
		Error:             err,
		Status:            RestClientGenericError,
	}
}

func RestClientResultFromInvalidTypeErr(err error) *RestClientResult {
	return &RestClientResult{
		ResourceInterface: nil,
		Error:             err,
		Status:            RestClientInvalidTypeError,
	}
}

// GetRestClientFromUnstructured creates a dynamic k8s client based on the provided manifest
func GetRestClientFromUnstructured(manifest *yaml.Manifest, provider *KubeProvider) *RestClientResult {

	doGetRestClientFromUnstructured := func(manifest *yaml.Manifest, provider *KubeProvider) *RestClientResult {
		// Use the k8s Discovery service to find all valid APIs for this cluster
		discoveryClient, _ := provider.ToDiscoveryClient()
		var resources []*meta_v1.APIResourceList
		var err error
		_, resources, err = discoveryClient.ServerGroupsAndResources()

		// There is a partial failure mode here where not all groups are returned `GroupDiscoveryFailedError`
		// we'll try and continue in this condition as it's likely something we don't need
		// and if it is the `checkAPIResourceIsPresent` check will fail and stop the process
		if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
			return RestClientResultFromErr(err)
		}

		// Validate that the APIVersion provided in the YAML is valid for this cluster
		apiResource, exists := checkAPIResourceIsPresent(resources, *manifest.Raw)
		if !exists {
			// api not found, invalidate the cache and try again
			// this handles the case when a CRD is being created by another kubectl_manifest resource run
			discoveryClient.Invalidate()
			_, resources, err = discoveryClient.ServerGroupsAndResources()

			if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
				return RestClientResultFromErr(err)
			}

			// check for resource again
			apiResource, exists = checkAPIResourceIsPresent(resources, *manifest.Raw)
			if !exists {
				return RestClientResultFromInvalidTypeErr(fmt.Errorf("resource [%s/%s] isn't valid for cluster, check the APIVersion and Kind fields are valid", manifest.Raw.GroupVersionKind().GroupVersion().String(), manifest.GetKind()))
			}
		}

		resourceStruct := k8sschema.GroupVersionResource{
			Group:    apiResource.Group,
			Version:  apiResource.Version,
			Resource: apiResource.Name,
		}
		// For core services (ServiceAccount, Service etc) the group is incorrectly parsed.
		// "v1" should be empty group and "v1" for version
		if resourceStruct.Group == "v1" && resourceStruct.Version == "" {
			resourceStruct.Group = ""
			resourceStruct.Version = "v1"
		}
		// get dynamic client based on the found resource struct
		client := dynamic.NewForConfigOrDie(&provider.RestConfig).Resource(resourceStruct)

		// if the resource is namespaced and doesn't have a namespace defined, set it to default
		if apiResource.Namespaced {
			if !manifest.HasNamespace() {
				manifest.SetNamespace("default")
			}
			return RestClientResultSuccess(client.Namespace(manifest.GetNamespace()))
		}

		return RestClientResultSuccess(client)
	}

	timeout := time.NewTimer(60 * time.Second)
	defer timeout.Stop()
	select {
	case res := <-discoveryWithTimeout(func() *RestClientResult {
		return doGetRestClientFromUnstructured(manifest, provider)
	}):
		return res
	case <-timeout.C:
		log.Printf("[ERROR] %v timed out fetching resources from discovery client", manifest)
		return RestClientResultFromErr(fmt.Errorf("%v timed out fetching resources from discovery client", manifest))
	}
}

// discoveryWithTimeout runs produce in a goroutine and returns a buffered
// channel of capacity 1 so the producer can always deliver its result and
// exit, even if the caller has already taken the timeout branch. Without the
// buffer the producer pins the discovery client and cached HTTP responses
// until the process exits, leaking one goroutine per timed-out apply.
func discoveryWithTimeout(produce func() *RestClientResult) <-chan *RestClientResult {
	ch := make(chan *RestClientResult, 1)
	go func() {
		ch <- produce()
	}()
	return ch
}

// checkAPIResourceIsPresent Loops through a list of available APIResources and
// checks there is a resource for the APIVersion and Kind defined in the 'resource'
// if found it returns true and the APIResource which matched
func checkAPIResourceIsPresent(available []*meta_v1.APIResourceList, resource meta_v1_unstruct.Unstructured) (*meta_v1.APIResource, bool) {
	resourceGroupVersionKind := resource.GroupVersionKind()
	for _, rList := range available {
		if rList == nil {
			continue
		}
		group := rList.GroupVersion
		for _, r := range rList.APIResources {
			if group == resourceGroupVersionKind.GroupVersion().String() && r.Kind == resource.GetKind() {
				r.Group = resourceGroupVersionKind.Group
				r.Version = resourceGroupVersionKind.Version
				r.Kind = resourceGroupVersionKind.Kind
				return &r, true
			}
		}
	}
	log.Printf("[ERROR] Could not find a valid ApiResource for this manifest %s/%s/%s", resourceGroupVersionKind.Group, resourceGroupVersionKind.Version, resourceGroupVersionKind.Kind)
	return nil, false
}

func WaitForDelete(ctx context.Context, restClient *RestClientResult, name string, timeout time.Duration) error {
	timeoutSeconds := int64(timeout.Seconds())

	rawResponse, err := restClient.ResourceInterface.Get(ctx, name, meta_v1.GetOptions{})
	resourceGone := errors.IsGone(err) || errors.IsNotFound(err)
	if err != nil && !resourceGone {
		return err
	}

	if !resourceGone {
		resourceVersion, _, err := unstructured.NestedString(rawResponse.Object, "metadata", "resourceVersion")
		if err != nil {
			return err
		}

		watcher, err := restClient.ResourceInterface.Watch(
			ctx,
			meta_v1.ListOptions{
				Watch:           true,
				TimeoutSeconds:  &timeoutSeconds,
				FieldSelector:   fields.OneTermEqualSelector("metadata.name", name).String(),
				ResourceVersion: resourceVersion,
			})
		if err != nil {
			return err
		}

		defer watcher.Stop()

		deleted := false
		for !deleted {
			select {
			case event := <-watcher.ResultChan():
				if event.Type == watch.Deleted {
					deleted = true
				}

			case <-ctx.Done():
				return fmt.Errorf("%s failed to delete resource", name)
			}
		}
	}

	return nil
}

// deploymentRolloutComplete reports whether the Deployment has finished
// its current rollout. Mirrors kubectl's `rollout status deployment`
// progression semantics. Same as the existing watch logic — extracted to
// a pure predicate so we can probe both before and after opening the
// Watch, closing the Get-then-Watch race that left wait_for_rollout
// hanging until the operation timeout. See issue #226.
func deploymentRolloutComplete(deployment *apps_v1.Deployment) bool {
	if deployment.Generation > deployment.Status.ObservedGeneration {
		return false
	}
	// Preserve the existing provider behaviour: a Progressing=False with
	// reason=ProgressDeadlineExceeded is treated as "still waiting", not
	// as a permanent failure. Callers rely on this to keep waiting while
	// the controller retries.
	if condition := getDeploymentCondition(deployment.Status, apps_v1.DeploymentProgressing); condition != nil && condition.Reason == TimedOutReason {
		return false
	}
	if deployment.Spec.Replicas != nil && deployment.Status.UpdatedReplicas < *deployment.Spec.Replicas {
		return false
	}
	if deployment.Status.Replicas > deployment.Status.UpdatedReplicas {
		return false
	}
	if deployment.Status.AvailableReplicas < deployment.Status.UpdatedReplicas {
		return false
	}
	return true
}

func WaitForDeploymentRollout(ctx context.Context, provider *KubeProvider, ns string, name string, timeout time.Duration) error {
	deploymentClient := provider.MainClientset.AppsV1().Deployments(ns)

	// Probe current state and capture ResourceVersion before opening the
	// Watch. A Watch opened with no ResourceVersion only delivers future
	// events; if the controller settled the rollout between this Get and
	// the Watch open, the watcher would sit idle until the operation
	// timeout. See issue #226.
	resourceVersion := ""
	if current, err := deploymentClient.Get(ctx, name, meta_v1.GetOptions{}); err == nil {
		if deploymentRolloutComplete(current) {
			return nil
		}
		resourceVersion = current.ResourceVersion
	}

	timeoutSeconds := int64(timeout.Seconds())
	watcher, err := deploymentClient.Watch(ctx, meta_v1.ListOptions{
		Watch:           true,
		TimeoutSeconds:  &timeoutSeconds,
		FieldSelector:   fields.OneTermEqualSelector("metadata.name", name).String(),
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return err
	}
	defer watcher.Stop()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				if current, gerr := deploymentClient.Get(ctx, name, meta_v1.GetOptions{}); gerr == nil && deploymentRolloutComplete(current) {
					return nil
				}
				return fmt.Errorf("%s watch channel closed before Deployment rollout completed", name)
			}
			if event.Type != watch.Modified {
				continue
			}
			deployment, ok := event.Object.(*apps_v1.Deployment)
			if !ok {
				return fmt.Errorf("%s could not cast to Deployment", name)
			}
			if deploymentRolloutComplete(deployment) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("%s failed to rollout Deployment", name)
		}
	}
}

func getDeploymentCondition(status apps_v1.DeploymentStatus, condType apps_v1.DeploymentConditionType) *apps_v1.DeploymentCondition {
	// Borrowed from: https://github.com/kubernetes/kubectl/blob/c4be63c54b7188502c1a63bb884a0b05fac51ebd/pkg/util/deployment/deployment.go#L60
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			return &c
		}
	}
	return nil
}

// daemonSetRolloutComplete reports whether the DaemonSet has finished its
// current rollout. Mirrors kubectl's `rollout status daemonset` logic.
// A DaemonSet whose `nodeSelector` matches no nodes settles with
// DesiredNumberScheduled = 0; this predicate returns true for that case
// (0 >= 0) and the caller exits without waiting on further events.
func daemonSetRolloutComplete(daemon *apps_v1.DaemonSet) bool {
	if daemon.Spec.UpdateStrategy.Type != apps_v1.RollingUpdateDaemonSetStrategyType {
		return true
	}
	if daemon.Generation > daemon.Status.ObservedGeneration {
		return false
	}
	if daemon.Status.UpdatedNumberScheduled < daemon.Status.DesiredNumberScheduled {
		return false
	}
	if daemon.Status.NumberAvailable < daemon.Status.DesiredNumberScheduled {
		return false
	}
	return true
}

func WaitForDaemonSetRollout(ctx context.Context, provider *KubeProvider, ns string, name string, timeout time.Duration) error {
	daemonClient := provider.MainClientset.AppsV1().DaemonSets(ns)

	// Probe the current state before opening a watch. The DaemonSet
	// controller may have already settled the status by the time we get
	// here (notably for a DaemonSet whose nodeSelector matches no nodes —
	// DesiredNumberScheduled = 0). A `Watch` opened after that point only
	// delivers future events; the watcher would sit idle until the
	// Terraform create-timeout and surface as a rollout failure. See
	// https://github.com/alekc/terraform-provider-kubectl/issues/228.
	//
	// We also capture the ResourceVersion to seed the Watch — that way
	// any reconcile event the controller emits between this Get and the
	// Watch opening is still delivered, closing the read-then-watch race.
	resourceVersion := ""
	if current, err := daemonClient.Get(ctx, name, meta_v1.GetOptions{}); err == nil {
		if daemonSetRolloutComplete(current) {
			return nil
		}
		resourceVersion = current.ResourceVersion
	}

	timeoutSeconds := int64(timeout.Seconds())
	watcher, err := daemonClient.Watch(ctx, meta_v1.ListOptions{
		Watch:           true,
		TimeoutSeconds:  &timeoutSeconds,
		FieldSelector:   fields.OneTermEqualSelector("metadata.name", name).String(),
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return err
	}
	defer watcher.Stop()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				// Watcher closed (server-side timeout or apiserver
				// disconnect) before we observed completion. Re-probe
				// directly — the rollout may have finished silently.
				if current, gerr := daemonClient.Get(ctx, name, meta_v1.GetOptions{}); gerr == nil && daemonSetRolloutComplete(current) {
					return nil
				}
				return fmt.Errorf("%s watch channel closed before DaemonSet rollout completed", name)
			}
			if event.Type != watch.Modified {
				continue
			}
			daemon, ok := event.Object.(*apps_v1.DaemonSet)
			if !ok {
				return fmt.Errorf("%s could not cast to DaemonSet", name)
			}
			if daemonSetRolloutComplete(daemon) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("%s failed to rollout DaemonSet", name)
		}
	}
}

// statefulSetRolloutComplete reports whether the StatefulSet has
// finished its current rollout. Mirrors kubectl's `rollout status
// statefulset` semantics, extracted from the original watch loop so we
// can probe state before opening the Watch. See issue #226.
func statefulSetRolloutComplete(sts *apps_v1.StatefulSet) bool {
	if sts.Spec.UpdateStrategy.Type != apps_v1.RollingUpdateStatefulSetStrategyType {
		return true
	}
	if sts.Status.ObservedGeneration == 0 || sts.Generation > sts.Status.ObservedGeneration {
		return false
	}
	if sts.Spec.Replicas != nil && sts.Status.ReadyReplicas < *sts.Spec.Replicas {
		return false
	}
	if sts.Spec.UpdateStrategy.RollingUpdate != nil {
		if sts.Spec.Replicas != nil && sts.Spec.UpdateStrategy.RollingUpdate.Partition != nil {
			if sts.Status.UpdatedReplicas < (*sts.Spec.Replicas - *sts.Spec.UpdateStrategy.RollingUpdate.Partition) {
				return false
			}
		}
		return true
	}
	if sts.Status.UpdateRevision != sts.Status.CurrentRevision {
		return false
	}
	return true
}

func WaitForStatefulSetRollout(ctx context.Context, provider *KubeProvider, ns string, name string, timeout time.Duration) error {
	stsClient := provider.MainClientset.AppsV1().StatefulSets(ns)

	resourceVersion := ""
	if current, err := stsClient.Get(ctx, name, meta_v1.GetOptions{}); err == nil {
		if statefulSetRolloutComplete(current) {
			return nil
		}
		resourceVersion = current.ResourceVersion
	}

	timeoutSeconds := int64(timeout.Seconds())
	watcher, err := stsClient.Watch(ctx, meta_v1.ListOptions{
		Watch:           true,
		TimeoutSeconds:  &timeoutSeconds,
		FieldSelector:   fields.OneTermEqualSelector("metadata.name", name).String(),
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return err
	}
	defer watcher.Stop()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				if current, gerr := stsClient.Get(ctx, name, meta_v1.GetOptions{}); gerr == nil && statefulSetRolloutComplete(current) {
					return nil
				}
				return fmt.Errorf("%s watch channel closed before StatefulSet rollout completed", name)
			}
			if event.Type != watch.Modified {
				continue
			}
			sts, ok := event.Object.(*apps_v1.StatefulSet)
			if !ok {
				return fmt.Errorf("%s could not cast to StatefulSet", name)
			}
			if statefulSetRolloutComplete(sts) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("%s failed to rollout StatefulSet", name)
		}
	}
}

func WaitForApiService(ctx context.Context, provider *KubeProvider, name string, timeout time.Duration) error {
	timeoutSeconds := int64(timeout.Seconds())

	watcher, err := provider.AggregatorClientset.ApiregistrationV1().APIServices().Watch(ctx, meta_v1.ListOptions{Watch: true, TimeoutSeconds: &timeoutSeconds, FieldSelector: fields.OneTermEqualSelector("metadata.name", name).String()})
	if err != nil {
		return err
	}

	defer watcher.Stop()

	done := false
	for !done {
		select {
		case event := <-watcher.ResultChan():
			if event.Type == watch.Modified {
				apiService, ok := event.Object.(*apiregistration.APIService)
				if !ok {
					return fmt.Errorf("%s could not cast to APIService", name)
				}

				for i := range apiService.Status.Conditions {
					if apiService.Status.Conditions[i].Type == apiregistration.Available {
						done = true
						continue
					}
				}
			}

		case <-ctx.Done():
			return fmt.Errorf("%s failed to wait for APIService", name)
		}
	}

	return nil
}

func WaitForConditions(ctx context.Context, restClient *RestClientResult, waitFields []types.WaitForField, waitConditions []types.WaitForStatusCondition, name string, timeout time.Duration) error {
	timeoutSeconds := int64(timeout.Seconds())

	watcher, err := restClient.ResourceInterface.Watch(
		ctx,
		meta_v1.ListOptions{
			Watch:          true,
			TimeoutSeconds: &timeoutSeconds,
			FieldSelector:  fields.OneTermEqualSelector("metadata.name", name).String(),
		},
	)
	if err != nil {
		return err
	}
	defer watcher.Stop()

	done := false
	for !done {
		select {
		case event := <-watcher.ResultChan():
			log.Printf("[TRACE] Received event type %s for %s", event.Type, name)
			if event.Type == watch.Modified || event.Type == watch.Added {
				rawResponse, ok := event.Object.(*meta_v1_unstruct.Unstructured)
				if !ok {
					return fmt.Errorf("%s could not cast resource to unstructured", name)
				}

				totalConditions := len(waitConditions) + len(waitFields)
				totalMatches := 0

				yamlJson, err := rawResponse.MarshalJSON()
				if err != nil {
					return err
				}

				gq := gojsonq.New().FromString(string(yamlJson))

				for _, c := range waitConditions {
					// Find the conditions by status and type
					count := gq.Reset().From("status.conditions").
						Where("type", "=", c.Type).
						Where("status", "=", c.Status).Count()
					if count == 0 {
						log.Printf("[TRACE] Condition %s with status %s not found in %s", c.Type, c.Status, name)
						continue
					}
					log.Printf("[TRACE] Condition %s with status %s found in %s", c.Type, c.Status, name)
					totalMatches++
				}

				for _, c := range waitFields {
					// Find the key
					v := gq.Reset().Find(c.Key)
					if v == nil {
						log.Printf("[TRACE] Key %s not found in %s", c.Key, name)
						continue
					}

					// For the sake of comparison we will convert everything to a string
					stringVal := fmt.Sprintf("%v", v)
					switch c.ValueType {
					case "regex":
						matched, err := regexp.Match(c.Value, []byte(stringVal))
						if err != nil {
							return err
						}

						if !matched {
							log.Printf("[TRACE] Value %s does not match regex %s in %s (key %s)", stringVal, c.Value, name, c.Key)
							continue
						}

						log.Printf("[TRACE] Value %s matches regex %s in %s (key %s)", stringVal, c.Value, name, c.Key)
						totalMatches++

					case "eq", "":
						if stringVal != c.Value {
							log.Printf("[TRACE] Value %s does not match %s in %s (key %s)", stringVal, c.Value, name, c.Key)
							continue
						}
						log.Printf("[TRACE] Value %s matches %s in %s (key %s)", stringVal, c.Value, name, c.Key)
						totalMatches++
					}
				}
				if totalMatches == totalConditions {
					log.Printf("[TRACE] All conditions met for %s", name)
					done = true
					continue
				}
				log.Printf("[TRACE] %d/%d conditions met for %s. Waiting for next ", totalMatches, totalConditions, name)
			}

		case <-ctx.Done():
			return fmt.Errorf("%s failed to wait for resource", name)
		}
	}

	return nil
}

// Takes the result of flatmap.Expand for an array of strings
// and returns a []*string
func expandStringList(configured []interface{}) []string {
	vs := make([]string, 0, len(configured))
	for _, v := range configured {
		val, ok := v.(string)
		if ok && val != "" {
			vs = append(vs, val)
		}
	}
	return vs
}

func GetFingerprint(s string) string {
	fingerprint := sha256.New()
	fingerprint.Write([]byte(s))
	return fmt.Sprintf("%x", fingerprint.Sum(nil))
}

func GetLiveManifestFields(ignoredFields []string, userProvided *yaml.Manifest, liveManifest *yaml.Manifest) string {

	// there is a special user case for secrets.
	// If they are defined as manifests with StringData, it will always provide a non-empty plan
	// so we will do a small lifehack here
	if userProvided.GetKind() == "Secret" && userProvided.GetAPIVersion() == "v1" {
		if stringData, found := userProvided.Raw.Object["stringData"]; found {
			// there is an edge case where stringData might be nil and not a map[string]interface{}
			// in this case we will just ignore it
			if stringData, ok := stringData.(map[string]interface{}); ok {
				// move all stringdata values to the data
				for k, v := range stringData {
					encodedString := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%v", v)))
					meta_v1_unstruct.SetNestedField(userProvided.Raw.Object, encodedString, "data", k)
				}
				// and unset the stringData entirely
				meta_v1_unstruct.RemoveNestedField(userProvided.Raw.Object, "stringData")
			}
		}
	}

	flattenedUser := flatten.Flatten(userProvided.Raw.Object)
	flattenedLive := flatten.Flatten(liveManifest.Raw.Object)

	// remove any fields from the user provided set or control fields that we want to ignore
	fieldsToTrim := append(kubernetesControlFields, ignoredFields...)
	for _, field := range fieldsToTrim {
		delete(flattenedUser, field)

		// check for any nested fields to ignore
		for k, _ := range flattenedUser {
			if strings.HasPrefix(k, field+".") {
				delete(flattenedUser, k)
			}
		}
	}

	// update the user provided flattened string with the live versions of the keys
	// this implicitly excludes anything that the user didn't provide as it was added by kubernetes runtime (annotations/mutations etc)
	var userKeys []string
	for userKey, userValue := range flattenedUser {
		normalizedUserValue := strings.TrimSpace(userValue)

		// only include the value if it exists in the live version
		// that is, don't add to the userKeys array unless the key still exists in the live manifest
		if _, exists := flattenedLive[userKey]; exists {
			userKeys = append(userKeys, userKey)
			normalizedLiveValue := strings.TrimSpace(flattenedLive[userKey])
			if normalizedUserValue != normalizedLiveValue {
				log.Printf("[TRACE] yaml drift detected in %s for %s, was: %s now: %s", userProvided.GetSelfLink(), userKey, normalizedUserValue, normalizedLiveValue)
			}
			flattenedUser[userKey] = GetFingerprint(normalizedLiveValue)
		} else {
			if normalizedUserValue != "" {
				log.Printf("[TRACE] yaml drift detected in %s for %s, was %s now blank", userProvided.GetSelfLink(), userKey, normalizedUserValue)
			}
		}
	}

	sort.Strings(userKeys)
	var returnedValues []string
	for _, k := range userKeys {
		returnedValues = append(returnedValues, fmt.Sprintf("%s=%s", k, flattenedUser[k]))
	}

	return strings.Join(returnedValues, "\n")
}

var kubernetesControlFields = []string{
	"status",
	"metadata.finalizers",
	"metadata.initializers",
	"metadata.ownerReferences",
	"metadata.creationTimestamp",
	"metadata.generation",
	"metadata.resourceVersion",
	"metadata.uid",
	"metadata.annotations.kubectl.kubernetes.io/last-applied-configuration",
	"metadata.managedFields",
}
