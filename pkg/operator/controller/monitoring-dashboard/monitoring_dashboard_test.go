package monitoringdashboard

import (
	"context"
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ingress-operator-dashboard",
			Namespace: "openshift-config-managed",
			Labels: map[string]string{
				"console.openshift.io/dashboard": "true",
			},
		},
		Data: map[string]string{
			"dashboard.json": dashboardEmbed,
		},
	}
}

// TestDashboardNeedsUpdate checks if the dashboardNeedsUpdate function 
// accurately determines the need for dashboard ConfigMap updates under various scenarios.
func TestDashboardNeedsUpdate(t *testing.T) {
	type testInputs struct {
		current *corev1.ConfigMap
		desired *corev1.ConfigMap
	}
	type testOutputs struct {
		res bool
	}
	testCases := []struct {
		description string
		inputs      testInputs
		output      testOutputs
	}{
		{
			description: "Identical configmaps",
			inputs: testInputs{
				current: newConfigMap(),
				desired: newConfigMap(),
			},
			output: testOutputs{
				res: false,
			},
		},
		{
			description: "Missing dashboard in configmap",
			inputs: testInputs{
				current: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "ingress-controller-dashboard",
						Namespace: "openshift-config-managed",
						Labels: map[string]string{
							"console.openshift.io/dashboard": "true",
						},
					},
					Data: map[string]string{},
				},
				desired: newConfigMap(),
			},
			output: testOutputs{
				res: true,
			},
		},
		{
			description: "Wrong dashboard value",
			inputs: testInputs{
				current: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "ingress-controller-dashboard",
						Namespace: "openshift-config-managed",
						Labels: map[string]string{
							"console.openshift.io/dashboard": "true",
						},
					},
					Data: map[string]string{
						"dashboard.json": "corrupted text",
					},
				},
				desired: newConfigMap(),
			},
			output: testOutputs{
				res: true,
			},
		},
		{
			description: "Second unwanted dashboard",
			inputs: testInputs{
				current: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "ingress-controller-dashboard",
						Namespace: "openshift-config-managed",
						Labels: map[string]string{
							"console.openshift.io/dashboard": "true",
						},
					},
					Data: map[string]string{
						"dashboard.json":  dashboardEmbed,
						"dashboard2.json": dashboardEmbed,
					},
				},
				desired: newConfigMap(),
			},
			output: testOutputs{
				res: true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			expected := tc.output.res
			actual := dashboardNeedsUpdate(tc.inputs.current, tc.inputs.desired)
			if expected != actual {
				t.Errorf("expected %v, got %v", expected, actual)
			}
		})
	}
}

// TestDesiredMonitoringDashboard verifies that the function
// desiredMonitoringDashboard correctly creates a monitoring dashboard
// ConfigMap based on the ControlPlaneTopology value. It ensures no ConfigMap
// is returned for ExternalTopologyMode and checks for a correct ConfigMap in 
// other cases.
func TestDesiredMonitoringDashboard(t *testing.T) {
	type testInputs struct {
		infraStatus configv1.InfrastructureStatus
	}
	type testOutputs struct {
		configMap *corev1.ConfigMap
	}
	testCases := []struct {
		description string
		inputs      testInputs
		output      testOutputs
	}{
		{
			description: "No dashboard if topology is external",
			inputs: testInputs{
				infraStatus: configv1.InfrastructureStatus{
					ControlPlaneTopology: configv1.ExternalTopologyMode,
				},
			},
			output: testOutputs{
				configMap: nil,
			},
		},
		{
			description: "Dashboard expected if topology is not external",
			inputs: testInputs{
				infraStatus: configv1.InfrastructureStatus{
					ControlPlaneTopology: configv1.SingleReplicaTopologyMode,
				},
			},
			output: testOutputs{
				configMap: newConfigMap(),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			expected := tc.output.configMap
			actual := desiredMonitoringDashboard(context.TODO(), tc.inputs.infraStatus)
			if expected == nil && actual != nil {
				t.Errorf("expected %v, got %v", expected, actual)
			} else if expected != nil && actual == nil {
				t.Errorf("expected %v, got %v", expected, actual)
			} else if !reflect.DeepEqual(expected, actual) {
				t.Errorf("expected %v, got %v", expected, actual)
			}
		})
	}
}
