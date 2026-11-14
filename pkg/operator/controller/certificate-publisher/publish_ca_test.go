package certificatepublisher

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

// TestEnsureDefaultIngressCertConfigMapMetadata verifies that
// ensureDefaultIngressCertConfigMap sets the operator-managed TLS metadata
// annotations when creating the configmap, adds them to a pre-existing
// configmap that lacks them while preserving unrelated annotations, and does
// not issue a spurious update when only an unrelated annotation differs.
func TestEnsureDefaultIngressCertConfigMapMetadata(t *testing.T) {
	name := controller.DefaultIngressCertConfigMapName()

	tests := []struct {
		name                string
		existing            *corev1.ConfigMap
		caBundle            string
		wantUnrelated       bool
		wantResourceVersion string
	}{
		{
			name:     "create",
			caBundle: "new-ca-bundle",
		},
		{
			name: "update adds annotations and preserves unrelated",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name.Name,
					Namespace:   name.Namespace,
					Annotations: map[string]string{"example.com/unrelated": "preserved"},
				},
				Data: map[string]string{"ca-bundle.crt": "old-ca-bundle"},
			},
			caBundle:      "new-ca-bundle",
			wantUnrelated: true,
		},
		{
			name: "no spurious update when unrelated annotation changes",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            name.Name,
					Namespace:       name.Namespace,
					ResourceVersion: "999",
					Annotations: map[string]string{
						annotations.OpenShiftComponent:   controller.RouterTLSOwningComponent,
						annotations.OpenShiftDescription: defaultIngressCertificateDescription,
						"example.com/unrelated":          "changed-by-another-actor",
					},
				},
				Data: map[string]string{"ca-bundle.crt": "same-ca-bundle"},
			},
			caBundle:            "same-ca-bundle",
			wantUnrelated:       true,
			wantResourceVersion: "999",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientBuilder := fake.NewClientBuilder()
			if test.existing != nil {
				clientBuilder = clientBuilder.WithObjects(test.existing)
			}
			r := &reconciler{
				client:   clientBuilder.Build(),
				recorder: record.NewFakeRecorder(10),
			}

			assert.NoError(t, r.ensureDefaultIngressCertConfigMap(test.caBundle))

			actual := &corev1.ConfigMap{}
			assert.NoError(t, r.client.Get(context.Background(), name, actual))
			assert.Equal(t, controller.RouterTLSOwningComponent, actual.Annotations[annotations.OpenShiftComponent])
			assert.Equal(t, defaultIngressCertificateDescription, actual.Annotations[annotations.OpenShiftDescription])
			assert.Equal(t, test.caBundle, actual.Data["ca-bundle.crt"])

			if test.wantUnrelated {
				_, ok := actual.Annotations["example.com/unrelated"]
				assert.True(t, ok, "expected unrelated annotation to be preserved")
			}

			if test.wantResourceVersion != "" {
				// The fake client bumps ResourceVersion on every Update; an
				// unchanged ResourceVersion proves that changing an unrelated
				// annotation did not trigger a spurious update, because only the
				// managed annotations are compared.
				assert.Equal(t, test.wantResourceVersion, actual.ResourceVersion, "expected no spurious update")
			}
		})
	}
}
