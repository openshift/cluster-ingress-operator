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
	caSecret := certSecret("ca", "x")
	caSecret.Namespace = "openshift-ingress-operator"

	secrets := []corev1.Secret{
		certSecret("a", "a"),
		certSecret("b", "b"),
		certSecret("c", "c"),
		certSecret("d", "d"),
	}

	ingresses := []operatorv1.IngressController{
		newIngressControllerWithCert("a", "a"),
		newIngressControllerWithCert("c", "c"),
	}

	actual, err := desiredRouterCAConfigMap(ingresses, &caSecret, secrets)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	expected := &corev1.ConfigMap{
		Data: map[string]string{
			"ca-bundle.crt": "xac",
		},
	}

	opts := []cmp.Option{
		cmpopts.EquateEmpty(),
	}
	if !cmp.Equal(actual.Data, expected.Data, opts...) {
		t.Fatalf("expected:\n%#v\ngot:\n%#v", expected.Data, actual.Data)
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
