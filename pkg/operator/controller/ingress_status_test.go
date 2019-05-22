package controller

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	toolscache "k8s.io/client-go/tools/cache"
)

func pendingLBService(owner string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "router-" + owner,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: owner,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
}

func provisionedLBservice(owner string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "router-" + owner,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: owner,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{Hostname: "lb.cloudprovider.example.com"},
				},
			},
		},
	}
}

func clusterIPservice(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "router-" + name + "internal",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func cond(t string, status operatorv1.ConditionStatus, reason string) operatorv1.OperatorCondition {
	return operatorv1.OperatorCondition{
		Type:   t,
		Status: status,
		Reason: reason,
	}
}

func indexerContains(indexer toolscache.Indexer, name, value string) bool {
	objects, err := indexer.ByIndex(name, value)
	if err != nil {
		return false
	}
	return len(objects) > 0
}

func makeIndexers(funcs map[string]client.IndexerFunc) toolscache.Indexers {
	indexers := map[string]toolscache.IndexFunc{}
	for name := range funcs {
		fn := funcs[name]
		indexers[name] = func(obj interface{}) ([]string, error) {
			return fn(obj.(runtime.Object)), nil
		}
	}
	return indexers
}

func TestComputeLoadBalancerStatus(t *testing.T) {
	tests := []struct {
		desc       string
		controller string
		strat      operatorv1.EndpointPublishingStrategyType
		services   []*corev1.Service
		expect     []operatorv1.OperatorCondition
	}{
		{
			desc:       "provisioned/ready",
			controller: "default",
			strat:      operatorv1.LoadBalancerServiceStrategyType,
			services: []*corev1.Service{
				provisionedLBservice("secondary"),
				provisionedLBservice("default"),
				clusterIPservice("default"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "HasLoadBalancerEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "LoadBalancerProvisioned"),
			},
		},
		{
			desc:       "pending/unready",
			controller: "default",
			strat:      operatorv1.LoadBalancerServiceStrategyType,
			services: []*corev1.Service{
				provisionedLBservice("secondary"),
				pendingLBService("default"),
				clusterIPservice("default"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "HasLoadBalancerEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "LoadBalancerPending"),
			},
		},
		{
			desc:       "unmanaged",
			controller: "default",
			strat:      operatorv1.HostNetworkStrategyType,
			services: []*corev1.Service{
				provisionedLBservice("secondary"),
				clusterIPservice("default"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionFalse, "UnsupportedEndpointPublishingStrategy"),
			},
		},
		{
			desc:       "missing",
			controller: "default",
			strat:      operatorv1.LoadBalancerServiceStrategyType,
			services: []*corev1.Service{
				provisionedLBservice("secondary"),
				clusterIPservice("default"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "HasLoadBalancerEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "LoadBalancerNotFound"),
			},
		},
	}

	for _, test := range tests {
		t.Logf("evaluating test %s", test.desc)
		controllers := toolscache.NewIndexer(toolscache.MetaNamespaceKeyFunc, makeIndexers(controllerIndexers))
		services := toolscache.NewIndexer(toolscache.MetaNamespaceKeyFunc, makeIndexers(serviceIndexers))
		status := &ingressStatusCache{
			contains: func(kind runtime.Object, name, value string) bool {
				switch kind.(type) {
				case *corev1.ServiceList:
					return indexerContains(services, name, value)
				case *operatorv1.IngressControllerList:
					return indexerContains(controllers, name, value)
				default:
					return false
				}
			},
		}
		controller := &operatorv1.IngressController{
			ObjectMeta: metav1.ObjectMeta{
				Name: test.controller,
			},
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: test.strat,
				},
			},
		}
		controllers.Add(controller)
		for i := range test.services {
			services.Add(test.services[i])
		}

		actual := status.computeLoadBalancerStatus(controller)
		conditionsCmpOpts := []cmp.Option{
			cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Message"),
			cmpopts.EquateEmpty(),
			cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
		}
		if !cmp.Equal(actual, test.expect, conditionsCmpOpts...) {
			t.Fatalf("expected:\n%#v\ngot:\n%#v", test.expect, actual)
		}
	}
}

func TestComputeIngressStatusConditions(t *testing.T) {
	testCases := []struct {
		description     string
		availRepl, repl int32
		condType        string
		condStatus      operatorv1.ConditionStatus
	}{
		{"0/2 deployment replicas available", 0, 2, operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionFalse},
		{"1/2 deployment replicas available", 1, 2, operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue},
		{"2/2 deployment replicas available", 2, 2, operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue},
	}

	for i, tc := range testCases {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ingress-controller-%d", i+1),
			},
			Status: appsv1.DeploymentStatus{
				Replicas:          tc.repl,
				AvailableReplicas: tc.availRepl,
			},
		}

		expected := []operatorv1.OperatorCondition{
			{
				Type:   tc.condType,
				Status: tc.condStatus,
			},
		}
		actual := computeIngressStatusConditions([]operatorv1.OperatorCondition{}, deploy)
		conditionsCmpOpts := []cmp.Option{
			cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Reason", "Message"),
			cmpopts.EquateEmpty(),
			cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
		}
		if !cmp.Equal(actual, expected, conditionsCmpOpts...) {
			t.Fatalf("%q: expected %#v, got %#v", tc.description, expected, actual)
		}
	}
}

func TestIngressStatusesEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        operatorv1.IngressControllerStatus
	}{
		{
			description: "nil and non-nil slices are equal",
			expected:    true,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{},
			},
		},
		{
			description: "empty slices should be equal",
			expected:    true,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{},
			},
		},
		{
			description: "condition LastTransitionTime should not be ignored",
			expected:    false,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               operatorv1.IngressControllerAvailableConditionType,
						Status:             operatorv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(0, 0),
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               operatorv1.IngressControllerAvailableConditionType,
						Status:             operatorv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(1, 0),
					},
				},
			},
		},
		{
			description: "check condition reason differs",
			expected:    false,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionFalse,
						Reason: "foo",
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionFalse,
						Reason: "bar",
					},
				},
			},
		},
		{
			description: "condition status differs",
			expected:    false,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionTrue,
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionFalse,
					},
				},
			},
		},
		{
			description: "check duplicate with single condition",
			expected:    false,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:    operatorv1.IngressControllerAvailableConditionType,
						Message: "foo",
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:    operatorv1.IngressControllerAvailableConditionType,
						Message: "foo",
					},
					{
						Type:    operatorv1.IngressControllerAvailableConditionType,
						Message: "foo",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		if actual := ingressStatusesEqual(tc.a, tc.b); actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description, tc.expected, actual)
		}
	}
}
