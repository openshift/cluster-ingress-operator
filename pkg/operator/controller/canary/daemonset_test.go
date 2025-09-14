package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_desiredCanaryDaemonSet(t *testing.T) {
	// canaryImageName is the ingress-operator image
	canaryImageName := "openshift/origin-cluster-ingress-operator:latest"
	certSecretName := "test_secret_name"
	daemonset := desiredCanaryDaemonSet(canaryImageName, certSecretName)

	expectedDaemonSetName := controller.CanaryDaemonSetName()

	if !cmp.Equal(daemonset.Name, expectedDaemonSetName.Name) {
		t.Errorf("expected daemonset name to be %s, but got %s", expectedDaemonSetName.Name, daemonset.Name)
	}

	if !cmp.Equal(daemonset.Namespace, expectedDaemonSetName.Namespace) {
		t.Errorf("expected daemonset namespace to be %s, but got %s", expectedDaemonSetName.Namespace, daemonset.Namespace)
	}

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	if !cmp.Equal(daemonset.Labels, expectedLabels) {
		t.Errorf("expected daemonset labels to be %q, but got %q", expectedLabels, daemonset.Labels)
	}

	labelSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			controller.CanaryDaemonSetLabel: canaryControllerName,
		},
	}

	if !cmp.Equal(daemonset.Spec.Selector, labelSelector) {
		t.Errorf("expected daemonset selector to be %q, but got %q", labelSelector, daemonset.Spec.Selector)
	}

	if !cmp.Equal(daemonset.Spec.Template.Labels, labelSelector.MatchLabels) {
		t.Errorf("expected daemonset template labels to be %q, but got %q", labelSelector.MatchLabels, daemonset.Spec.Template.Labels)
	}

	containers := daemonset.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Errorf("expected daemonset to have 1 container, but found %d", len(containers))
	}

	if !cmp.Equal(containers[0].Image, canaryImageName) {
		t.Errorf("expected daemonset container image to be %q, but got %q", canaryImageName, containers[0].Image)
	}

	nodeSelector := daemonset.Spec.Template.Spec.NodeSelector
	expectedNodeSelector := map[string]string{
		"kubernetes.io/os": "linux",
	}
	if !cmp.Equal(nodeSelector, expectedNodeSelector) {
		t.Errorf("expected daemonset node selector to be %q, but got %q", expectedNodeSelector, nodeSelector)
	}

	priorityClass := daemonset.Spec.Template.Spec.PriorityClassName
	expectedPriorityClass := "system-cluster-critical"
	if !cmp.Equal(priorityClass, expectedPriorityClass) {
		t.Errorf("expected daemonset priority class to be %q, but got %q", expectedPriorityClass, priorityClass)
	}

	tolerations := daemonset.Spec.Template.Spec.Tolerations
	expectedTolerations := []corev1.Toleration{
		{
			Key:      "node-role.kubernetes.io/infra",
			Operator: "Exists",
		},
	}
	if !cmp.Equal(tolerations, expectedTolerations) {
		t.Errorf("expected daemonset tolerations to be %v, but got %v", expectedTolerations, tolerations)
	}

	volumes := daemonset.Spec.Template.Spec.Volumes
	secretMode := int32(0420)
	expectedVolumes := []corev1.Volume{
		{
			Name: "cert",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  certSecretName,
					DefaultMode: &secretMode,
				},
			},
		},
	}
	if !cmp.Equal(volumes, expectedVolumes) {
		t.Errorf("expected daemonset volumes to be %v, but got %v", expectedVolumes, volumes)
	}
}

func Test_canaryDaemonsetChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*appsv1.DaemonSet)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *appsv1.DaemonSet) {},
			expect:      false,
		},
		{
			description: "if pod template node selector changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.NodeSelector = map[string]string{
					"foo":  "bar",
					"test": "selector",
				}
			},
			expect: true,
		},
		{
			description: "if pod template tolerations changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Tolerations = []corev1.Toleration{
					{
						Key:      "test",
						Operator: "nil",
						Value:    "foo",
						Effect:   "bar",
					},
				}
			},
			expect: true,
		},
		{
			description: "if pod template toleration effect changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Tolerations[0].Effect = "NoExecute"
			},
			expect: true,
		},
		{
			description: "if canary server image changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Image = "foo.io/test:latest"
			},
			expect: true,
		},
		{
			description: "if canary command changes (removed)",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Command = []string{}
			},
			expect: true,
		},
		{
			description: "if canary command changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Command = []string{"foo"}
			},
			expect: true,
		},
		{
			description: "if canary server container name changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Name = "bar"
			},
			expect: true,
		},
		{
			description: "if canary daemonset priority class changed",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.PriorityClassName = "my-priority-class"
			},
			expect: true,
		},
		{
			description: "if canary daemonset pod security context changed",
			mutate: func(ds *appsv1.DaemonSet) {
				v := false
				sc := &corev1.PodSecurityContext{
					RunAsNonRoot: &v,
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
				}
				ds.Spec.Template.Spec.SecurityContext = sc
			},
			expect: true,
		},
		{
			description: "if canary daemonset container security context changed",
			mutate: func(ds *appsv1.DaemonSet) {
				v := false
				sc := &corev1.SecurityContext{
					AllowPrivilegeEscalation: &v,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{},
					},
				}
				ds.Spec.Template.Spec.Containers[0].SecurityContext = sc
			},
			expect: true,
		},
		{
			description: "if canary daemonset environment changed",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Env = append(ds.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "foo", Value: "bar"})
			},
			expect: true,
		},
		{
			description: "if canary daemonset volume mount changed",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].VolumeMounts = append(ds.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "foo", MountPath: "/bar"})
			},
			expect: true,
		},
		{
			description: "if canary daemonset volume changed",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Volumes = append(ds.Spec.Template.Spec.Volumes, corev1.Volume{
					Name: "foo",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "bar",
						},
					},
				})
			},
			expect: true,
		},
		{
			description: "if canary daemonset ports changed",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Ports = append(ds.Spec.Template.Spec.Containers[0].Ports, corev1.ContainerPort{Name: "foo", ContainerPort: 1234})
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original := desiredCanaryDaemonSet("", "foobar")
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := canaryDaemonSetChanged(original, mutated); changed != tc.expect {
				t.Errorf("expect canaryDaemonSetChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if updatedChanged, _ := canaryDaemonSetChanged(original, updated); !updatedChanged {
					t.Error("canaryDaemonSetChanged reported changes but did not make any update")
				}
				if changedAgain, _ := canaryDaemonSetChanged(mutated, updated); changedAgain {
					t.Error("canaryDaemonSetChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
