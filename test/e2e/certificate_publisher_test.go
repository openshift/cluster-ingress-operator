// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
)

// TestCreateIngressControllerThenSecret creates an ingresscontroller that
// references a secret that does not exist, then creates the secret and verifies
// that the operator updates the "router-certs" global secret.
func TestCreateIngressControllerThenSecret(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	name := names.SimpleNameGenerator.GenerateName("test-")

	// Create the ingresscontroller.
	var one int32 = 1
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: operatorv1.IngressControllerSpec{
			DefaultCertificate: &corev1.LocalObjectReference{
				Name: name,
			},
			Domain: name,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			},
			Replicas: &one,
		},
	}
	if err := cl.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create the ingresscontroller: %v", err)
	}
	defer func() {
		if err := cl.Delete(context.TODO(), ic); err != nil {
			t.Fatalf("failed to delete the ingresscontroller: %v", err)
		}
	}()

	// Wait for the ingresscontroller to be reconciled.
	err = wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}, ic); err != nil {
			return false, nil
		}
		if len(ic.Status.Domain) == 0 {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe reconciliation of ingresscontroller: %v", err)
	}

	// Create the secret.
	secret, err := createDefaultCertTestSecret(cl, name)
	if err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}
	defer func() {
		if err := cl.Delete(context.TODO(), secret); err != nil {
			t.Errorf("failed to delete secret: %v", err)
		}
	}()

	// Wait for the "router-certs" secret to be updated.
	err = wait.PollImmediate(1*time.Second, 60*time.Second, func() (bool, error) {
		globalSecret := &corev1.Secret{}
		if err := cl.Get(context.TODO(), ingresscontroller.RouterCertsGlobalSecretName(), globalSecret); err != nil {
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
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	name := names.SimpleNameGenerator.GenerateName("test-")

	// Create the secret.
	secret, err := createDefaultCertTestSecret(cl, name)
	if err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}
	defer func() {
		if err := cl.Delete(context.TODO(), secret); err != nil {
			t.Errorf("failed to delete secret: %v", err)
		}
	}()

	// Create the ingresscontroller.
	var one int32 = 1
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: operatorv1.IngressControllerSpec{
			DefaultCertificate: &corev1.LocalObjectReference{
				Name: name,
			},
			Domain: name,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			},
			Replicas: &one,
		},
	}
	if err := cl.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create the ingresscontroller: %v", err)
	}
	defer func() {
		if err := cl.Delete(context.TODO(), ic); err != nil {
			t.Fatalf("failed to delete the ingresscontroller: %v", err)
		}
	}()

	// Wait for the "router-certs" secret to be updated.
	err = wait.PollImmediate(1*time.Second, 60*time.Second, func() (bool, error) {
		globalSecret := &corev1.Secret{}
		if err := cl.Get(context.TODO(), ingresscontroller.RouterCertsGlobalSecretName(), globalSecret); err != nil {
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
