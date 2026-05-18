package kubernetes

import (
	"testing"

	apps_v1 "k8s.io/api/apps/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestDaemonSetRolloutComplete pins down the rollout-complete predicate so a
// future refactor cannot reintroduce the regression from issue #228, where a
// DaemonSet with a nodeSelector that matches no nodes was treated as
// "still rolling out" instead of immediately complete.
func TestDaemonSetRolloutComplete(t *testing.T) {
	cases := []struct {
		name string
		in   *apps_v1.DaemonSet
		want bool
	}{
		{
			name: "non-rolling-update strategy always complete",
			in: ds(apps_v1.DaemonSetSpec{
				UpdateStrategy: apps_v1.DaemonSetUpdateStrategy{
					Type: apps_v1.OnDeleteDaemonSetStrategyType,
				},
			}, apps_v1.DaemonSetStatus{
				ObservedGeneration: 0, // even with status totally unobserved
			}),
			want: true,
		},
		{
			name: "generation ahead of observedGeneration — not yet observed",
			in: dsRolling(1, apps_v1.DaemonSetStatus{
				ObservedGeneration:     0,
				DesiredNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
				NumberAvailable:        3,
			}),
			want: false,
		},
		{
			name: "no matching nodes — desired=0, observed",
			in: dsRolling(1, apps_v1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 0,
				UpdatedNumberScheduled: 0,
				NumberAvailable:        0,
			}),
			want: true, // issue #228 regression
		},
		{
			name: "rollout still in progress — updated < desired",
			in: dsRolling(1, apps_v1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				UpdatedNumberScheduled: 2,
				NumberAvailable:        2,
			}),
			want: false,
		},
		{
			name: "updated complete but not yet available",
			in: dsRolling(1, apps_v1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
				NumberAvailable:        2,
			}),
			want: false,
		},
		{
			name: "fully rolled out",
			in: dsRolling(1, apps_v1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
				NumberAvailable:        3,
			}),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonSetRolloutComplete(tc.in)
			if got != tc.want {
				t.Fatalf("daemonSetRolloutComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

func ds(spec apps_v1.DaemonSetSpec, status apps_v1.DaemonSetStatus) *apps_v1.DaemonSet {
	return &apps_v1.DaemonSet{
		ObjectMeta: meta_v1.ObjectMeta{Generation: 1},
		Spec:       spec,
		Status:     status,
	}
}

func dsRolling(generation int64, status apps_v1.DaemonSetStatus) *apps_v1.DaemonSet {
	return &apps_v1.DaemonSet{
		ObjectMeta: meta_v1.ObjectMeta{Generation: generation},
		Spec: apps_v1.DaemonSetSpec{
			UpdateStrategy: apps_v1.DaemonSetUpdateStrategy{
				Type: apps_v1.RollingUpdateDaemonSetStrategyType,
			},
		},
		Status: status,
	}
}

// TestDeploymentRolloutComplete pins down the Deployment rollout-complete
// predicate extracted while fixing issue #226 (Get-then-Watch race in
// wait_for_rollout that left updates hanging until the operation timeout).
func TestDeploymentRolloutComplete(t *testing.T) {
	replicas := func(n int32) *int32 { return &n }

	cases := []struct {
		name string
		in   *apps_v1.Deployment
		want bool
	}{
		{
			name: "generation ahead of observedGeneration — not yet observed",
			in: dep(2, apps_v1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           3,
				UpdatedReplicas:    3,
				AvailableReplicas:  3,
			}, replicas(3)),
			want: false,
		},
		{
			name: "progress deadline exceeded — still waiting (not failing) per existing provider semantics",
			in: dep(1, apps_v1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           3,
				UpdatedReplicas:    3,
				AvailableReplicas:  3,
				Conditions: []apps_v1.DeploymentCondition{{
					Type:   apps_v1.DeploymentProgressing,
					Reason: TimedOutReason,
				}},
			}, replicas(3)),
			want: false,
		},
		{
			name: "updated < desired",
			in: dep(1, apps_v1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           3,
				UpdatedReplicas:    2,
				AvailableReplicas:  2,
			}, replicas(3)),
			want: false,
		},
		{
			name: "old replicas still being scaled down",
			in: dep(1, apps_v1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           4,
				UpdatedReplicas:    3,
				AvailableReplicas:  3,
			}, replicas(3)),
			want: false,
		},
		{
			name: "available < updated",
			in: dep(1, apps_v1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           3,
				UpdatedReplicas:    3,
				AvailableReplicas:  2,
			}, replicas(3)),
			want: false,
		},
		{
			name: "fully rolled out",
			in: dep(1, apps_v1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           3,
				UpdatedReplicas:    3,
				AvailableReplicas:  3,
			}, replicas(3)),
			want: true,
		},
		{
			name: "fully rolled out with nil spec.replicas (omits the spec-replicas check)",
			in: dep(1, apps_v1.DeploymentStatus{
				ObservedGeneration: 1,
				Replicas:           1,
				UpdatedReplicas:    1,
				AvailableReplicas:  1,
			}, nil),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deploymentRolloutComplete(tc.in)
			if got != tc.want {
				t.Fatalf("deploymentRolloutComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStatefulSetRolloutComplete pins down the StatefulSet
// rollout-complete predicate extracted alongside the Deployment one.
func TestStatefulSetRolloutComplete(t *testing.T) {
	replicas := func(n int32) *int32 { return &n }
	partition := func(n int32) *int32 { return &n }

	cases := []struct {
		name string
		in   *apps_v1.StatefulSet
		want bool
	}{
		{
			name: "OnDelete strategy always complete",
			in: &apps_v1.StatefulSet{
				ObjectMeta: meta_v1.ObjectMeta{Generation: 1},
				Spec: apps_v1.StatefulSetSpec{
					UpdateStrategy: apps_v1.StatefulSetUpdateStrategy{Type: apps_v1.OnDeleteStatefulSetStrategyType},
				},
				Status: apps_v1.StatefulSetStatus{ObservedGeneration: 0},
			},
			want: true,
		},
		{
			name: "observedGeneration == 0 — controller not yet observed",
			in: stsRolling(1, apps_v1.StatefulSetStatus{
				ObservedGeneration: 0,
				ReadyReplicas:      3,
			}, replicas(3), nil),
			want: false,
		},
		{
			name: "generation ahead of observedGeneration",
			in: stsRolling(2, apps_v1.StatefulSetStatus{
				ObservedGeneration: 1,
				ReadyReplicas:      3,
			}, replicas(3), nil),
			want: false,
		},
		{
			name: "ready < desired",
			in: stsRolling(1, apps_v1.StatefulSetStatus{
				ObservedGeneration: 1,
				ReadyReplicas:      2,
			}, replicas(3), nil),
			want: false,
		},
		{
			name: "partition: updated covers (replicas - partition) → complete",
			in: stsRolling(1, apps_v1.StatefulSetStatus{
				ObservedGeneration: 1,
				ReadyReplicas:      3,
				UpdatedReplicas:    2, // updated 2 pods, replicas=3, partition=1 → 2 >= 3-1 ✓
			}, replicas(3), partition(1)),
			want: true,
		},
		{
			name: "partition: updated below (replicas - partition) → still rolling",
			in: stsRolling(1, apps_v1.StatefulSetStatus{
				ObservedGeneration: 1,
				ReadyReplicas:      3,
				UpdatedReplicas:    1, // 1 < 3-1
			}, replicas(3), partition(1)),
			want: false,
		},
		{
			name: "rolling update with rollingUpdate config but no partition",
			in: stsRolling(1, apps_v1.StatefulSetStatus{
				ObservedGeneration: 1,
				ReadyReplicas:      3,
				UpdatedReplicas:    3,
			}, replicas(3), nil),
			want: true,
		},
		{
			name: "updateRevision != currentRevision when no rollingUpdate config",
			in: &apps_v1.StatefulSet{
				ObjectMeta: meta_v1.ObjectMeta{Generation: 1},
				Spec: apps_v1.StatefulSetSpec{
					Replicas:       replicas(3),
					UpdateStrategy: apps_v1.StatefulSetUpdateStrategy{Type: apps_v1.RollingUpdateStatefulSetStrategyType},
				},
				Status: apps_v1.StatefulSetStatus{
					ObservedGeneration: 1,
					ReadyReplicas:      3,
					UpdateRevision:     "rev-2",
					CurrentRevision:    "rev-1",
				},
			},
			want: false,
		},
		{
			name: "updateRevision == currentRevision when no rollingUpdate config",
			in: &apps_v1.StatefulSet{
				ObjectMeta: meta_v1.ObjectMeta{Generation: 1},
				Spec: apps_v1.StatefulSetSpec{
					Replicas:       replicas(3),
					UpdateStrategy: apps_v1.StatefulSetUpdateStrategy{Type: apps_v1.RollingUpdateStatefulSetStrategyType},
				},
				Status: apps_v1.StatefulSetStatus{
					ObservedGeneration: 1,
					ReadyReplicas:      3,
					UpdateRevision:     "rev-1",
					CurrentRevision:    "rev-1",
				},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statefulSetRolloutComplete(tc.in)
			if got != tc.want {
				t.Fatalf("statefulSetRolloutComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

func dep(generation int64, status apps_v1.DeploymentStatus, specReplicas *int32) *apps_v1.Deployment {
	return &apps_v1.Deployment{
		ObjectMeta: meta_v1.ObjectMeta{Generation: generation},
		Spec:       apps_v1.DeploymentSpec{Replicas: specReplicas},
		Status:     status,
	}
}

func stsRolling(generation int64, status apps_v1.StatefulSetStatus, specReplicas, partition *int32) *apps_v1.StatefulSet {
	spec := apps_v1.StatefulSetSpec{
		Replicas: specReplicas,
		UpdateStrategy: apps_v1.StatefulSetUpdateStrategy{
			Type:          apps_v1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &apps_v1.RollingUpdateStatefulSetStrategy{Partition: partition},
		},
	}
	return &apps_v1.StatefulSet{
		ObjectMeta: meta_v1.ObjectMeta{Generation: generation},
		Spec:       spec,
		Status:     status,
	}
}
