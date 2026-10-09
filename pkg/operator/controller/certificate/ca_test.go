package certificate

import (
	"context"
	"testing"

	"github.com/openshift/api/annotations"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestDesiredRouterCASecretMetadata verifies that desiredRouterCASecret sets the
// operator-managed TLS metadata annotations and stores the provided CA
// certificate and key.
func TestDesiredRouterCASecretMetadata(t *testing.T) {
	secret, err := desiredRouterCASecret("test-namespace", []byte(cert), []byte(key))
	assert.NoError(t, err)

	wantAnnotations := map[string]string{
		annotations.OpenShiftComponent:   controller.RouterTLSOwningComponent,
		annotations.OpenShiftDescription: routerCADescription,
	}
	assert.Equal(t, wantAnnotations, secret.Annotations)
	assert.Equal(t, []byte(cert), secret.Data["tls.crt"])
	assert.Equal(t, []byte(key), secret.Data["tls.key"])
}

// Test_ensureRouterCASecret_reconcilesAnnotations verifies that
// ensureRouterCASecret adds the operator-managed TLS metadata annotations to a
// pre-existing router CA secret (for example, one created by a release that did
// not set them) while preserving unrelated annotations and the existing
// certificate data.
func Test_ensureRouterCASecret_reconcilesAnnotations(t *testing.T) {
	const namespace = "test-namespace"

	name := controller.RouterCASecretName(namespace)

	// existing simulates a router CA secret that predates the TLS metadata
	// annotations.  It carries an unrelated annotation that must be preserved
	// by the reconcile.
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name.Name,
			Namespace:   name.Namespace,
			Annotations: map[string]string{"example.com/unrelated": "preserved"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte(cert),
			"tls.key": []byte(key),
		},
	}

	r := &reconciler{
		client:            fake.NewClientBuilder().WithObjects(existing).Build(),
		recorder:          record.NewFakeRecorder(10),
		operatorNamespace: namespace,
	}

	returned, err := r.ensureRouterCASecret(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, returned)

	actual := &corev1.Secret{}
	assert.NoError(t, r.client.Get(context.Background(), name, actual))
	assert.Equal(t, controller.RouterTLSOwningComponent, actual.Annotations[annotations.OpenShiftComponent])
	assert.Equal(t, routerCADescription, actual.Annotations[annotations.OpenShiftDescription])
	assert.Equal(t, "preserved", actual.Annotations["example.com/unrelated"], "expected unrelated annotation to be preserved")

	// The existing certificate data must be preserved; reconciling the
	// annotations must not regenerate or overwrite the CA certificate.
	assert.Equal(t, []byte(cert), actual.Data["tls.crt"], "expected certificate data to be preserved")
	assert.Equal(t, []byte(key), actual.Data["tls.key"], "expected key data to be preserved")
}

// Test_ensureRouterCASecret_noSpuriousUpdate verifies that ensureRouterCASecret
// does not issue an update when the managed TLS metadata annotations are already
// present on the existing router CA secret.
func Test_ensureRouterCASecret_noSpuriousUpdate(t *testing.T) {
	const namespace = "test-namespace"

	name := controller.RouterCASecretName(namespace)

	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name.Name,
			Namespace:       name.Namespace,
			ResourceVersion: "999",
			Annotations: map[string]string{
				annotations.OpenShiftComponent:   controller.RouterTLSOwningComponent,
				annotations.OpenShiftDescription: routerCADescription,
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte(cert),
			"tls.key": []byte(key),
		},
	}

	r := &reconciler{
		client:            fake.NewClientBuilder().WithObjects(existing).Build(),
		recorder:          record.NewFakeRecorder(10),
		operatorNamespace: namespace,
	}

	returned, err := r.ensureRouterCASecret(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, returned)

	actual := &corev1.Secret{}
	assert.NoError(t, r.client.Get(context.Background(), name, actual))
	// The fake client bumps ResourceVersion on every Update; an unchanged
	// ResourceVersion proves that no spurious update was issued when the
	// annotations already match.
	assert.Equal(t, "999", actual.ResourceVersion, "expected no update when annotations already match")
}
