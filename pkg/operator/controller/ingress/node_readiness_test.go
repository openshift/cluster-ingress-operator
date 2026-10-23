package ingress

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Test_nodeMatchesRequiredAffinity verifies Kubernetes node-affinity operators,
// whose serialized values differ from those used by the labels package.
func Test_nodeMatchesRequiredAffinity(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"zone": "east", "capacity": "3"}}}
	for _, tc := range []struct {
		name   string
		key    string
		op     corev1.NodeSelectorOperator
		values []string
		want   bool
	}{
		{"in matches", "zone", corev1.NodeSelectorOpIn, []string{"east"}, true},
		{"in rejects", "zone", corev1.NodeSelectorOpIn, []string{"west"}, false},
		{"not in matches", "zone", corev1.NodeSelectorOpNotIn, []string{"west"}, true},
		{"not in rejects", "zone", corev1.NodeSelectorOpNotIn, []string{"east"}, false},
		{"exists matches", "zone", corev1.NodeSelectorOpExists, nil, true},
		{"exists rejects", "missing", corev1.NodeSelectorOpExists, nil, false},
		{"does not exist matches", "missing", corev1.NodeSelectorOpDoesNotExist, nil, true},
		{"does not exist rejects", "zone", corev1.NodeSelectorOpDoesNotExist, nil, false},
		{"greater than matches", "capacity", corev1.NodeSelectorOpGt, []string{"2"}, true},
		{"greater than rejects", "capacity", corev1.NodeSelectorOpGt, []string{"4"}, false},
		{"less than matches", "capacity", corev1.NodeSelectorOpLt, []string{"4"}, true},
		{"less than rejects", "capacity", corev1.NodeSelectorOpLt, []string{"2"}, false},
		{"invalid operator rejects", "zone", "invalid", []string{"east"}, false},
		{"invalid integer rejects", "capacity", corev1.NodeSelectorOpGt, []string{"invalid"}, false},
		{"empty in values rejects", "zone", corev1.NodeSelectorOpIn, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{Key: tc.key, Operator: tc.op, Values: tc.values}},
				}}}},
			}}}}}
			require.Equal(t, tc.want, nodeMatchesRequiredAffinity(deployment, node))
		})
	}
	t.Run("empty terms do not select nodes", func(t *testing.T) {
		deployment := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}}},
		}}}}}
		require.False(t, nodeMatchesRequiredAffinity(deployment, node))
	})
}

// Test_syncIngressControllerStatus_nodeReadiness exercises the real status sync
// using generated router deployments, including their default NotIn affinity.
// A custom DCM router must not satisfy a readiness wait before its pods exist.
func Test_syncIngressControllerStatus_nodeReadiness(t *testing.T) {
	readyWorker := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"kubernetes.io/os": "linux", "node-role.kubernetes.io/worker": ""}},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
	for _, tc := range []struct {
		name               string
		nodes              []corev1.Node
		waiting            bool
		unsuitable         bool
		publishingStrategy operatorv1.EndpointPublishingStrategyType
		service            *corev1.Service
		dnsConfig          *configv1.DNS
		unavailableDetails string
	}{
		{name: "ready worker satisfies generated affinity", nodes: []corev1.Node{readyWorker}},
		{name: "zero workers preserves unavailable startup marker", waiting: true},
		{name: "custom selector with no matching worker remains unavailable", nodes: []corev1.Node{readyWorker}, unsuitable: true, waiting: true},
		{
			name: "zero workers preserves load balancer failure details", waiting: true,
			publishingStrategy: operatorv1.LoadBalancerServiceStrategyType,
			unavailableDetails: "LoadBalancerReady=False (ServiceNotFound: The LoadBalancer service resource is missing)",
		},
		{
			name: "zero workers preserves DNS failure details", waiting: true,
			publishingStrategy: operatorv1.LoadBalancerServiceStrategyType,
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
				Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{{Hostname: "router.example.test"}},
				}},
			},
			dnsConfig:          &configv1.DNS{Spec: configv1.DNSSpec{PublicZone: &configv1.DNSZone{ID: "test-zone"}}},
			unavailableDetails: "DNSReady=False (RecordNotFound: The wildcard record resource was not found.)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ic, ingressConfig, infraConfig, apiConfig, networkConfig, _, proxyConfig := getRouterDeploymentComponents(t)
			ic.Name = "e2e-dcm"
			ic.Namespace = controller.DefaultOperatorNamespace
			ic.Spec.EndpointPublishingStrategy = &operatorv1.EndpointPublishingStrategy{Type: operatorv1.PrivateStrategyType}
			if tc.publishingStrategy != "" {
				ic.Spec.EndpointPublishingStrategy = &operatorv1.EndpointPublishingStrategy{
					Type:         tc.publishingStrategy,
					LoadBalancer: &operatorv1.LoadBalancerStrategy{Scope: operatorv1.ExternalLoadBalancer},
				}
			}
			ic.Status.EndpointPublishingStrategy = ic.Spec.EndpointPublishingStrategy.DeepCopy()
			ic.Spec.TuningOptions.ConfigurationManagement = operatorv1.IngressControllerConfigurationManagementDynamic
			if tc.unsuitable {
				ic.Spec.NodePlacement = &operatorv1.NodePlacement{NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"dedicated": "router"}}}
			}
			infraConfig.Status.ControlPlaneTopology = configv1.ExternalTopologyMode
			config := Config{IngressControllerImage: ingressControllerImage}
			deployment, err := desiredRouterDeployment(ic, &config, ingressConfig, infraConfig, apiConfig, networkConfig, nil, false, false, nil, proxyConfig)
			require.NoError(t, err)
			require.NotNil(t, deployment.Spec.Template.Spec.Affinity.NodeAffinity, "fixture must exercise production node affinity")
			nodes := &corev1.NodeList{Items: tc.nodes}
			require.Equal(t, tc.waiting, shouldWaitForNodes(ic, deployment, configv1.ExternalTopologyMode, nodes))
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, operatorv1.AddToScheme(scheme))
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ic).WithObjects(ic).Build()
			r := &reconciler{client: kubeClient, config: config}
			dnsConfig := tc.dnsConfig
			if dnsConfig == nil {
				dnsConfig = &configv1.DNS{}
			}
			err, updated := r.syncIngressControllerStatus(ic, deployment, metav1.OwnerReference{}, nil, tc.service, nil, nil, dnsConfig, infraConfig.Status.PlatformStatus, ingressConfig, configv1.ExternalTopologyMode, nodes)
			require.Error(t, err, "an unavailable router must request a retry")
			require.True(t, updated)
			actual := &operatorv1.IngressController{}
			require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}, actual))
			available, found := findOperatorCondition(actual.Status.Conditions, operatorv1.IngressControllerAvailableConditionType)
			require.True(t, found)
			require.Equal(t, operatorv1.ConditionFalse, available.Status)
			require.Equal(t, tc.waiting, available.Reason == ReasonAwaitingNodes)
			if tc.waiting {
				require.Contains(t, available.Message, "IngressController is waiting for a ready node matching the router deployment.")
				require.Contains(t, available.Message, "DeploymentAvailable=Unknown")
			}
			if tc.unavailableDetails != "" {
				require.Contains(t, available.Message, tc.unavailableDetails)
			}
			progressing, found := findOperatorCondition(actual.Status.Conditions, operatorv1.OperatorStatusTypeProgressing)
			require.True(t, found)
			require.Equal(t, operatorv1.ConditionTrue, progressing.Status)
		})
	}
}

// Test_syncIngressControllerStatus_nodeLifecycle verifies that seeing the first
// eligible node ends startup history even before the deployment is ready, and
// that later worker loss cannot reopen the startup exception.
func Test_syncIngressControllerStatus_nodeLifecycle(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, _, proxyConfig := getRouterDeploymentComponents(t)
	ic.Namespace = controller.DefaultOperatorNamespace
	ic.Spec.EndpointPublishingStrategy = &operatorv1.EndpointPublishingStrategy{Type: operatorv1.PrivateStrategyType}
	ic.Status.EndpointPublishingStrategy = ic.Spec.EndpointPublishingStrategy.DeepCopy()
	config := Config{IngressControllerImage: ingressControllerImage}
	deployment, err := desiredRouterDeployment(ic, &config, ingressConfig, infraConfig, apiConfig, networkConfig, nil, false, false, nil, proxyConfig)
	require.NoError(t, err)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, operatorv1.AddToScheme(scheme))
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ic).WithObjects(ic).Build()
	r := &reconciler{client: kubeClient, config: config}
	name := types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}
	step := func(nodes *corev1.NodeList, wantStatus operatorv1.ConditionStatus, wantWaiting bool) {
		t.Helper()
		require.NoError(t, kubeClient.Get(context.Background(), name, ic))
		err, _ := r.syncIngressControllerStatus(ic, deployment, metav1.OwnerReference{}, nil, nil, nil, nil, &configv1.DNS{}, infraConfig.Status.PlatformStatus, ingressConfig, configv1.ExternalTopologyMode, nodes)
		if wantStatus == operatorv1.ConditionFalse {
			require.Error(t, err, "unavailable router must request a retry")
		} else {
			require.NoError(t, err)
		}
		require.NoError(t, kubeClient.Get(context.Background(), name, ic))
		available, found := findOperatorCondition(ic.Status.Conditions, operatorv1.IngressControllerAvailableConditionType)
		require.True(t, found)
		require.Equal(t, wantStatus, available.Status)
		require.Equal(t, wantWaiting, available.Reason == ReasonAwaitingNodes)
	}
	step(&corev1.NodeList{}, operatorv1.ConditionFalse, true)
	nodes := &corev1.NodeList{Items: []corev1.Node{{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"kubernetes.io/os": "linux", "node-role.kubernetes.io/worker": ""}},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}}}
	step(nodes, operatorv1.ConditionFalse, false)
	deployment.Status = appsv1.DeploymentStatus{
		Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
		Conditions: []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: deploymentNewRSAvailableReason},
		},
	}
	step(nodes, operatorv1.ConditionTrue, false)
	deployment.Status = appsv1.DeploymentStatus{}
	step(&corev1.NodeList{}, operatorv1.ConditionFalse, false)
}
