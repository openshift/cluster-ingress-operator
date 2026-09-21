package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Test_ensureDefaultIngressController verifies the default controller's TLS
// profile when it is created and when it already exists.
func Test_ensureDefaultIngressController(t *testing.T) {
	defaultName := types.NamespacedName{
		Namespace: "openshift-ingress-operator",
		Name:      "default",
	}
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.Install(scheme), "install config API scheme")
	require.NoError(t, operatorv1.AddToScheme(scheme), "install operator API scheme")

	tests := []struct {
		name                       string
		existing                   *operatorv1.IngressController
		changeProfileAfterCreation bool
		wantType                   configv1.TLSProfileType
	}{
		{
			name:     "creates the default controller with the modern TLS profile",
			wantType: configv1.TLSProfileModernType,
		},
		{
			name: "does not mutate an existing default controller",
			existing: &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: defaultName.Namespace,
					Name:      defaultName.Name,
				},
				Spec: operatorv1.IngressControllerSpec{
					TLSSecurityProfile: &configv1.TLSSecurityProfile{
						Type: configv1.TLSProfileIntermediateType,
					},
				},
			},
			wantType: configv1.TLSProfileIntermediateType,
		},
		{
			name:                       "does not erase a changed default controller TLS profile",
			changeProfileAfterCreation: true,
			wantType:                   configv1.TLSProfileIntermediateType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.existing != nil {
				clientBuilder = clientBuilder.WithObjects(tt.existing)
			}
			o := &Operator{
				client:    clientBuilder.Build(),
				namespace: defaultName.Namespace,
			}

			require.NoError(t, o.ensureDefaultIngressController(&configv1.Infrastructure{}, &configv1.Ingress{}), "ensure default ingress controller")

			actual := &operatorv1.IngressController{}
			require.NoError(t, o.client.Get(context.Background(), defaultName, actual), "get default ingress controller")
			if tt.changeProfileAfterCreation {
				actual.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
					Type: configv1.TLSProfileIntermediateType,
				}
				require.NoError(t, o.client.Update(context.Background(), actual), "change default ingress controller TLS security profile")
				require.NoError(t, o.ensureDefaultIngressController(&configv1.Infrastructure{}, &configv1.Ingress{}), "ensure default ingress controller after changing TLS security profile")
				require.NoError(t, o.client.Get(context.Background(), defaultName, actual), "get default ingress controller after changing TLS security profile")
			}
			if assert.NotNil(t, actual.Spec.TLSSecurityProfile, "default ingress controller must have a TLS security profile") {
				assert.Equal(t, tt.wantType, actual.Spec.TLSSecurityProfile.Type, "TLS security profile type")
			}
		})
	}
}
