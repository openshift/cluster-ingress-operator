package status

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// startupIngressController models actual persisted conditions while the default
// router is waiting for its first node and managed infrastructure is ready.
func startupIngressController() operatorv1.IngressController {
	return operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{Name: manifests.DefaultIngressControllerName},
		Status: operatorv1.IngressControllerStatus{Conditions: []operatorv1.OperatorCondition{
			{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionFalse, Reason: ingress.ReasonAwaitingNodes},
			{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
			{Type: operatorv1.OperatorStatusTypeDegraded, Status: operatorv1.ConditionFalse},
			{Type: ingress.IngressControllerDeploymentAvailableConditionType, Status: operatorv1.ConditionFalse},
			{Type: ingress.IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionTrue, Reason: ingress.ReasonDeploymentRollingOut},
			{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
			{Type: ingress.IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionFalse},
			{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		}},
	}
}

// Test_ingressControllersForClusterOperator verifies the CVO startup exception
// without changing the resource conditions consumed by router readiness waits.
func Test_ingressControllersForClusterOperator(t *testing.T) {
	for _, tc := range []struct {
		name        string
		topology    configv1.TopologyMode
		nodes       *corev1.NodeList
		modify      func(*operatorv1.IngressController)
		available   configv1.ConditionStatus
		progressing configv1.ConditionStatus
	}{
		{name: "initial zero-worker startup", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionTrue, progressing: configv1.ConditionFalse},
		{name: "unknown node list", topology: configv1.ExternalTopologyMode, available: configv1.ConditionFalse, progressing: configv1.ConditionTrue},
		{name: "regular topology", topology: configv1.HighlyAvailableTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionFalse, progressing: configv1.ConditionTrue},
		{name: "first node exists before ingress reconcile", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{Items: []corev1.Node{{}}}, available: configv1.ConditionFalse, progressing: configv1.ConditionTrue},
		{name: "private router dependencies", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionTrue, progressing: configv1.ConditionFalse, modify: func(ic *operatorv1.IngressController) {
			ic.Status.Conditions = ingress.MergeConditions(ic.Status.Conditions,
				operatorv1.OperatorCondition{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
				operatorv1.OperatorCondition{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse})
		}},
		{name: "post-startup node loss", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionFalse, progressing: configv1.ConditionTrue, modify: func(ic *operatorv1.IngressController) {
			ic.Status.Conditions[0].Reason = "IngressControllerUnavailable"
		}},
		{name: "DNS failure during startup", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionFalse, progressing: configv1.ConditionTrue, modify: func(ic *operatorv1.IngressController) {
			ic.Status.Conditions = ingress.MergeConditions(ic.Status.Conditions, operatorv1.OperatorCondition{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionFalse})
		}},
		{name: "load balancer failure during startup", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionFalse, progressing: configv1.ConditionTrue, modify: func(ic *operatorv1.IngressController) {
			ic.Status.Conditions = ingress.MergeConditions(ic.Status.Conditions, operatorv1.OperatorCondition{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionFalse})
		}},
		{name: "unknown DNS management", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionFalse, progressing: configv1.ConditionTrue, modify: func(ic *operatorv1.IngressController) {
			ic.Status.Conditions = ingress.MergeConditions(ic.Status.Conditions, operatorv1.OperatorCondition{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionUnknown})
		}},
		{name: "load balancer progressing remains visible", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionTrue, progressing: configv1.ConditionTrue, modify: func(ic *operatorv1.IngressController) {
			ic.Status.Conditions = ingress.MergeConditions(ic.Status.Conditions, operatorv1.OperatorCondition{Type: ingress.IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionTrue})
		}},
		{name: "missing status does not become available", topology: configv1.ExternalTopologyMode, nodes: &corev1.NodeList{}, available: configv1.ConditionUnknown, progressing: configv1.ConditionTrue, modify: func(ic *operatorv1.IngressController) {
			ic.Status.Conditions = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ic := startupIngressController()
			if tc.modify != nil {
				tc.modify(&ic)
			}
			controllers := []operatorv1.IngressController{ic}
			before := ic.DeepCopy()
			view := ingressControllersForClusterOperator(controllers, tc.topology, tc.nodes)
			require.Empty(t, cmp.Diff(*before, controllers[0]), "must not change cached IngressController conditions")
			available := computeOperatorAvailableCondition(view, tc.topology == configv1.ExternalTopologyMode)
			require.Equal(t, tc.available, available.Status)
			r := &reconciler{config: Config{OperatorReleaseVersion: "5.1", IngressControllerImage: "router:new", CanaryImage: "canary:new"}}
			oldVersions := []configv1.OperandVersion{{Name: OperatorVersionName, Version: "5.0"}, {Name: IngressControllerVersionName, Version: "router:old"}, {Name: CanaryImageVersionName, Version: "canary:old"}}
			allAvailable := checkAllIngressesAvailable(view, tc.topology == configv1.ExternalTopologyMode)
			versions := r.computeOperatorStatusVersions(oldVersions, allAvailable)
			progressing := computeOperatorProgressingCondition(view, allAvailable, oldVersions, versions, "5.1", "router:new", "canary:new", operatorcontroller.TransitionState{})
			require.Equal(t, tc.progressing, progressing.Status)
			if tc.available == configv1.ConditionTrue {
				require.Equal(t, "5.1", versions[0].Version, "zero-worker startup must allow CVO version advancement")
			} else {
				require.Equal(t, oldVersions, versions)
			}
		})
	}
}

// Test_zeroWorkerStartupDoesNotMaskCustomRouter verifies that custom routers
// retain their deployment readiness and continue contributing to CVO progress.
func Test_zeroWorkerStartupDoesNotMaskCustomRouter(t *testing.T) {
	defaultIC := startupIngressController()
	customIC := startupIngressController()
	customIC.Name = "e2e-dcm"
	controllers := []operatorv1.IngressController{defaultIC, customIC}
	view := ingressControllersForClusterOperator(controllers, configv1.ExternalTopologyMode, &corev1.NodeList{})
	require.Equal(t, operatorv1.ConditionTrue, view[0].Status.Conditions[0].Status)
	require.Equal(t, operatorv1.ConditionFalse, view[1].Status.Conditions[0].Status)
	require.Equal(t, operatorv1.ConditionTrue, view[1].Status.Conditions[1].Status)
	require.False(t, checkAllIngressesAvailable(view, true))
	progressing := computeOperatorProgressingCondition(view, false, nil, nil, "", "", "", operatorcontroller.TransitionState{})
	require.Equal(t, configv1.ConditionTrue, progressing.Status)
	require.Contains(t, progressing.Message, "e2e-dcm")
	require.Equal(t, operatorv1.ConditionFalse, controllers[0].Status.Conditions[0].Status)
}

type failingStartupNodeCache struct {
	cache.Cache
	reader client.Reader
	err    error
}

func (c *failingStartupNodeCache) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	if _, ok := list.(*corev1.NodeList); ok {
		return c.err
	}
	return c.reader.List(ctx, list, options...)
}

// Test_zeroWorkerStartupNodeListFailure verifies that an API/cache failure does
// not masquerade as a successful empty list or advance CVO conditions/versions.
func Test_zeroWorkerStartupNodeListFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, operatorv1.AddToScheme(scheme))
	require.NoError(t, configv1.AddToScheme(scheme))
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: operatorcontroller.IngressClusterOperatorName().Name}}
	initializeClusterOperator(co)
	before := co.DeepCopy()
	ic := startupIngressController()
	ic.Namespace = operatorcontroller.DefaultOperatorNamespace
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(co).WithObjects(co, &ic).Build()
	listErr := errors.New("node list unavailable")
	r := &reconciler{
		config: Config{Namespace: ic.Namespace, ControlPlaneTopology: configv1.ExternalTopologyMode, OperatorReleaseVersion: "5.1"},
		client: kubeClient,
		cache:  &failingStartupNodeCache{reader: kubeClient, err: listErr},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{})
	require.ErrorIs(t, err, listErr)
	require.NoError(t, kubeClient.Get(context.Background(), operatorcontroller.IngressClusterOperatorName(), co))
	require.Empty(t, cmp.Diff(before.Status, co.Status))
}
