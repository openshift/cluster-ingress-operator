//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestTunableMaxConnectionsValidValues(t *testing.T) {
	t.Parallel()
	updateMaxConnections := func(t *testing.T, client client.Client, timeout time.Duration, maxConnections int32, name types.NamespacedName) error {
		return wait.PollImmediate(time.Second, timeout, func() (bool, error) {
			ic := operatorv1.IngressController{}
			if err := client.Get(context.TODO(), name, &ic); err != nil {
				t.Logf("Get %q failed: %v, retrying...", name, err)
				return false, nil
			}
			ic.Spec.TuningOptions.MaxConnections = maxConnections
			if err := client.Update(context.TODO(), &ic); err != nil {
				t.Logf("Update %q failed: %v, retrying...", name, err)
				return false, nil
			}
			return true, nil
		})
	}

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "maxconnections-valid-values"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	t.Logf("waiting for ingresscontroller %v", icName)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := getIngressController(t, kclient, icName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}

	if err := waitForDeploymentEnvVar(t, kclient, deployment, time.Minute, ingress.RouterMaxConnectionsEnvName, ""); err != nil {
		t.Fatalf("expected router deployment to have %s unset: %v", ingress.RouterMaxConnectionsEnvName, err)
	}

	for _, testCase := range []struct {
		description    string
		maxConnections int32
		expectedEnvVar string
	}{
		{"set maxconn 12345", 12345, "12345"},
		{"set maxconn auto", -1, "auto"},
		{"set maxconn to default", 0, ""},
		{"set maxconn to 2000000", 2000000, "2000000"},
	} {
		t.Log(testCase.description)
		if err := updateMaxConnections(t, kclient, time.Minute, testCase.maxConnections, icName); err != nil {
			t.Fatalf("failed to update ingresscontroller with maxConnections=%v: %v", testCase.maxConnections, err)
		}
		if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
			t.Fatalf("failed to observe expected conditions: %v", err)
		}
		if err := waitForDeploymentEnvVar(t, kclient, deployment, time.Minute, ingress.RouterMaxConnectionsEnvName, testCase.expectedEnvVar); err != nil {
			t.Fatalf("router deployment not updated with %s=%v: %v", ingress.RouterMaxConnectionsEnvName, testCase.expectedEnvVar, err)
		}
	}
}

// TestTunableMaxConnectionsInvalidValues tests that invalid values
// cannot be set for tuningOptions.maxConnections.
//
// Valid values are 0, -1, and 2000-2000000, so we test outside of those value
// and expect validation failures when we attempt to set them.
func TestTunableMaxConnectionsInvalidValues(t *testing.T) {
	t.Parallel()
	updateMaxConnections := func(t *testing.T, client client.Client, maxConnections int32, name types.NamespacedName) error {
		ic := operatorv1.IngressController{}
		if err := client.Get(context.TODO(), name, &ic); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return err
		}
		ic.Spec.TuningOptions.MaxConnections = maxConnections
		return client.Update(context.TODO(), &ic)
	}

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "maxconnections-invalid-values"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	t.Logf("waiting for ingresscontroller %v", icName)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := getIngressController(t, kclient, icName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}

	if err := waitForDeploymentEnvVar(t, kclient, deployment, time.Minute, ingress.RouterMaxConnectionsEnvName, ""); err != nil {
		t.Fatalf("expected router deployment to have %s unset: %v", ingress.RouterMaxConnectionsEnvName, err)
	}

	for _, testCase := range []struct {
		description    string
		maxConnections int32
	}{
		{"set maxconn -2", -2},
		{"set maxconn 1999", 1999},
		{"set maxconn 2000001", 2000001},
	} {
		t.Log(testCase.description)
		const expectedErr string = `"spec.tuningOptions" must validate at least one schema (anyOf), spec.tuningOptions.maxConnections`
		err := updateMaxConnections(t, kclient, testCase.maxConnections, icName)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), expectedErr) {
			t.Fatalf("expected error message %q, got %v", expectedErr, err)
		}
	}
}
