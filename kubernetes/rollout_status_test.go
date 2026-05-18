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
