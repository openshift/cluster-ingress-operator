package certificate

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDesiredRouterCAConfigMap(t *testing.T) {
	// This one is special, everything else is in the
	// operand namespace.
	caSecret := certSecret("ca", "x")
	caSecret.Namespace = "openshift-ingress-operator"

	tests := map[string]struct {
		ca        *corev1.Secret
		ingresses []operatorv1.IngressController
		secrets   []corev1.Secret
		expect    map[string]string
	}{
		"with ca, user defined references": {
			ca: &caSecret,
			ingresses: []operatorv1.IngressController{
				newIngressControllerWithCert("default", ""),
				newIngressControllerWithCert("a", "a"),
				newIngressControllerWithCert("c", "c"),
			},
			secrets: []corev1.Secret{
				certSecret("a", "a"),
				certSecret("b", "b"),
				certSecret("c", "c"),
				certSecret("d", "d"),
			},
			expect: map[string]string{"ca-bundle.crt": "xac"},
		},
		"with ca, no ingresscontrollers": {
			ca:        &caSecret,
			ingresses: []operatorv1.IngressController{},
			secrets: []corev1.Secret{
				certSecret("a", "a"),
				certSecret("b", "b"),
				certSecret("c", "c"),
			},
			expect: map[string]string{"ca-bundle.crt": "x"},
		},
		"with ca, no user defined references": {
			ca: &caSecret,
			ingresses: []operatorv1.IngressController{
				newIngressControllerWithCert("default", ""),
			},
			secrets: []corev1.Secret{
				certSecret("a", "a"),
			},
			expect: map[string]string{"ca-bundle.crt": "x"},
		},
		"no ca, no references": {
			ingresses: []operatorv1.IngressController{},
			secrets: []corev1.Secret{
				certSecret("a", "a"),
			},
			expect: map[string]string{"ca-bundle.crt": ""},
		},
	}

	for name, test := range tests {
		t.Logf("testing: %v", name)
		actual, err := desiredRouterCAConfigMap(test.ingresses, test.ca, test.secrets)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		opts := []cmp.Option{
			cmpopts.EquateEmpty(),
		}
		if !cmp.Equal(actual.Data, test.expect, opts...) {
			t.Errorf("expected:\n%#v\ngot:\n%#v", test.expect, actual.Data)
		}
	}
}

func certSecret(name, data string) corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(name),
			Namespace: "openshift-ingress",
			Name:      name,
		},
		Data: map[string][]byte{
			"tls.crt": []byte(data),
		},
	}
}

func newIngressControllerWithCert(name, certSecret string) operatorv1.IngressController {
	repl := int32(1)
	ingress := operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-ingress",
			Name:      name,
		},
		Spec: operatorv1.IngressControllerSpec{
			Domain:   fmt.Sprintf("apps.%s.example.com", name),
			Replicas: &repl,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			},
		},
	}
	if len(certSecret) > 0 {
		ingress.Spec.DefaultCertificate = &corev1.LocalObjectReference{Name: certSecret}
	}
	return ingress
}
