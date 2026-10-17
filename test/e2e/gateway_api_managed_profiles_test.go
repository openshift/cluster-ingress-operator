//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// testGatewayAPIManagedProfileClusterIP verifies that selecting the managed class provisions only a ClusterIP Service.
func testGatewayAPIManagedProfileClusterIP(t *testing.T) {
	managedProfile, err := assertGatewayClassSuccessful(t, operatorcontroller.OpenShiftClusterIPGatewayClassName)
	require.NoError(t, err)
	require.Equal(t, "true", managedProfile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
	require.Eventually(t, func() bool {
		if err := kclient.Get(t.Context(), client.ObjectKeyFromObject(managedProfile), managedProfile); err != nil {
			return false
		}
		return meta.IsStatusConditionTrue(managedProfile.Status.Conditions, "Customized")
	}, time.Minute, time.Second, "managed ClusterIP defaults must be associated")
	var defaults corev1.ConfigMap
	require.NoError(t, kclient.Get(t.Context(), client.ObjectKey{Namespace: operatorcontroller.DefaultOperandNamespace, Name: "istio-" + operatorcontroller.IstioName("").Name + "-gatewayclass-" + managedProfile.Name}, &defaults))
	require.Equal(t, managedProfile.Name, defaults.Labels[operatorcontroller.GatewayClassDefaultsLabel])

	gateway, err := createGateway(t, managedProfile, names.SimpleNameGenerator.GenerateName("managed-profile-clusterip-"), operatorcontroller.DefaultOperandNamespace, "clusterip.example.com")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := kclient.Delete(context.Background(), gateway); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete test Gateway %s: %v", client.ObjectKeyFromObject(gateway), err)
		}
	})

	var services corev1.ServiceList
	err = wait.PollUntilContextTimeout(t.Context(), time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := kclient.List(ctx, &services, client.InNamespace(gateway.Namespace), client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gateway.Name}); err != nil {
			return false, err
		}
		return len(services.Items) > 0, nil
	})
	require.NoError(t, err, "Gateway must provision a Service")
	for _, service := range services.Items {
		require.Equal(t, corev1.ServiceTypeClusterIP, service.Spec.Type)
		require.NotEmpty(t, service.Spec.ClusterIP)
		require.NotEqual(t, corev1.ClusterIPNone, service.Spec.ClusterIP)
		require.Nil(t, service.Spec.LoadBalancerClass)
		require.Empty(t, service.Spec.LoadBalancerIP)
		require.Empty(t, service.Spec.ExternalTrafficPolicy)
		require.Empty(t, service.Status.LoadBalancer.Ingress)
		for _, port := range service.Spec.Ports {
			require.Zero(t, port.NodePort)
		}
	}
}
