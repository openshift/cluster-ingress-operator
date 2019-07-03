// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"

	iov1 "github.com/openshift/cluster-ingress-operator/pkg/api/v1"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
)

// TestCreateIngressControllerThenSecret creates an ingresscontroller that
// references a secret that does not exist, then creates the secret and verifies
// that the operator updates the "router-certs" global secret.
func TestCreateIngressControllerThenSecret(t *testing.T) {
	name := types.NamespacedName{Namespace: operatorNamespace, Name: names.SimpleNameGenerator.GenerateName("test-")}
	ic := newPrivateController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.DefaultCertificate = &corev1.LocalObjectReference{
		Name: name.Name,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	conditions := []operatorv1.OperatorCondition{
		{Type: iov1.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
	}
	err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	// Create the secret.
	secret, err := createDefaultCertTestSecret(kclient, name.Name)
	if err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), secret); err != nil {
			t.Errorf("failed to delete secret: %v", err)
		}
	}()

	// Wait for the "router-certs" secret to be updated.
	err = wait.PollImmediate(1*time.Second, 60*time.Second, func() (bool, error) {
		globalSecret := &corev1.Secret{}
		if err := kclient.Get(context.TODO(), ingresscontroller.RouterCertsGlobalSecretName(), globalSecret); err != nil {
			return false, nil
		}
		if _, ok := globalSecret.Data[ic.Spec.Domain]; !ok {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe updated global secret: %v", err)
	}
}

// TestCreateSecretThenIngressController creates a secret, then creates an
// ingresscontroller that references the secret and verifies that the operator
// updates the "router-certs" global secret.
func TestCreateSecretThenIngressController(t *testing.T) {
	name := types.NamespacedName{Namespace: operatorNamespace, Name: names.SimpleNameGenerator.GenerateName("test-")}

	// Create the secret.
	secret, err := createDefaultCertTestSecret(kclient, name.Name)
	if err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), secret); err != nil {
			t.Errorf("failed to delete secret: %v", err)
		}
	}()

	// Create the ingresscontroller.
	ic := newPrivateController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.DefaultCertificate = &corev1.LocalObjectReference{
		Name: name.Name,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the "router-certs" secret to be updated.
	err = wait.PollImmediate(1*time.Second, 60*time.Second, func() (bool, error) {
		globalSecret := &corev1.Secret{}
		if err := kclient.Get(context.TODO(), ingresscontroller.RouterCertsGlobalSecretName(), globalSecret); err != nil {
			return false, nil
		}
		if _, ok := globalSecret.Data[ic.Spec.Domain]; !ok {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe updated global secret: %v", err)
	}
}
