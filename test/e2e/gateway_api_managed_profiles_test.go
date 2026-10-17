//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// testGatewayAPIManagedProfileClusterIP verifies ClusterIP traffic and DNS exclusion for the managed class.
func testGatewayAPIManagedProfileClusterIP(t *testing.T) {
	// Wait for the managed class and its associated defaults.
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

	// Exercise admission without changing or deleting a protected class.
	protectedClass := &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: names.SimpleNameGenerator.GenerateName("openshift-admission-test-")},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
	}
	updated := managedProfile.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations["ingress.operator.openshift.io/admission-test"] = "true"
	for _, operation := range []struct {
		name    string
		request func() error
	}{
		{"protected-name creation", func() error { return kclient.Create(t.Context(), protectedClass, client.DryRunAll) }},
		{"managed-class update", func() error {
			return kclient.Patch(t.Context(), updated, client.MergeFrom(managedProfile), client.DryRunAll)
		}},
		{"managed-class deletion", func() error {
			return kclient.Delete(t.Context(), managedProfile, client.Preconditions{UID: &managedProfile.UID}, client.DryRunAll)
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.request()
			require.True(t, apierrors.IsForbidden(err), "expected policy rejection, got %v", err)
			require.ErrorContains(t, err, "openshift-ingress-operator-gatewayclasses")
			require.ErrorContains(t, err, "Only the Ingress Operator may create, modify, or delete managed OpenShift GatewayClasses")
		})
	}

	// Isolate the backend and client; cleanup removes only this test's resources.
	namespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("managed-profile-clusterip-"))
	hostname := "echo.clusterip.example.com"
	gateway, err := createGateway(t, managedProfile, namespace.Name, operatorcontroller.DefaultOperandNamespace, "clusterip.example.com")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := kclient.Delete(context.Background(), gateway); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete test Gateway %s: %v", client.ObjectKeyFromObject(gateway), err)
		}
	})

	// The programmed gateway must expose only ClusterIP Services.
	_, err = assertGatewaySuccessful(t, gateway.Namespace, gateway.Name)
	require.NoError(t, err)
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

	// Attach a ready echo backend through an accepted HTTPRoute.
	_, err = createHttpRoute(t, namespace.Name, "echo", gateway.Namespace, hostname, "echo", gateway)
	require.NoError(t, err)
	backend := buildEchoReplicaSet("echo", namespace.Name)
	require.NoError(t, waitForReplicaSetReady(t, kclient, backend, DefaultRetryTimeout))
	err = wait.PollUntilContextTimeout(t.Context(), time.Second, DefaultRetryTimeout, true, func(context.Context) (bool, error) {
		if _, err := assertHttpRouteSuccessful(t, namespace.Name, "echo", gateway); err != nil {
			t.Logf("waiting for HTTPRoute acceptance and resolved references: %v", err)
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)

	// Curl the gateway's cluster-local Service from a pod, not the backend directly.
	clientPod := buildExecPod("client", namespace.Name, backend.Spec.Template.Spec.Containers[0].Image)
	require.NoError(t, createWithRetryOnError(t, t.Context(), clientPod, DefaultRetryTimeout))
	require.NoError(t, waitForPodReady(t, kclient, clientPod, DefaultRetryTimeout))
	for _, service := range services.Items {
		url := "http://" + service.Name + "." + service.Namespace + ".svc/managed-profile"
		command := []string{"curl", "--silent", "--show-error", "--fail", "--noproxy", "*", "--connect-timeout", "2", "--max-time", "5", "--header", "Host: " + hostname, url}
		var stdout, stderr bytes.Buffer
		err = wait.PollUntilContextTimeout(t.Context(), time.Second, DefaultRetryTimeout, true, func(context.Context) (bool, error) {
			stdout.Reset()
			stderr.Reset()
			if err := podExec(t, *clientPod, &stdout, &stderr, command); err != nil {
				t.Logf("gateway request failed: %v: %s", err, stderr.String())
				return false, nil
			}
			return strings.Contains(stdout.String(), "GET /managed-profile HTTP/1.1") && strings.Contains(stdout.String(), hostname), nil
		})
		require.NoError(t, err, "gateway Service %s must return the echo backend response; stdout: %s; stderr: %s", service.Name, stdout.String(), stderr.String())
	}

	// Check Gateway-associated DNS records stay absent after programming and traffic.
	dnsObservationStart := time.Now()
	err = wait.PollUntilContextTimeout(t.Context(), time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var records iov1.DNSRecordList
		if err := kclient.List(ctx, &records, client.InNamespace(gateway.Namespace), client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gateway.Name}); err != nil {
			return false, err
		}
		if len(records.Items) > 0 {
			return false, fmt.Errorf("unexpected DNSRecords for Gateway %s: %+v", gateway.Name, records.Items)
		}
		return time.Since(dnsObservationStart) >= time.Minute, nil
	})
	require.NoError(t, err, "Gateway-associated DNSRecords must stay absent after programming and traffic")
}
