package gatewayclass

import (
	"testing"

	"github.com/stretchr/testify/assert"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
)

func Test_crdExists(t *testing.T) {
	scheme := runtime.NewScheme()
	apiextensionsv1.AddToScheme(scheme)

	tests := []struct {
		name            string
		crdName         string
		existingObjects []runtime.Object
		expectExists    bool
		expectError     bool
	}{
		{
			name:    "CRD exists",
			crdName: "inferencepools.inference.networking.k8s.io",
			existingObjects: []runtime.Object{
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.k8s.io",
					},
				},
			},
			expectExists: true,
			expectError:  false,
		},
		{
			name:            "CRD does not exist",
			crdName:         "inferencepools.inference.networking.k8s.io",
			existingObjects: []runtime.Object{},
			expectExists:    false,
			expectError:     false,
		},
		{
			name:    "Experimental CRD exists",
			crdName: "inferencepools.inference.networking.x-k8s.io",
			existingObjects: []runtime.Object{
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.x-k8s.io",
					},
				},
			},
			expectExists: true,
			expectError:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				Build()
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
			reconciler := &reconciler{
				cache: cache,
			}
			exists, err := reconciler.crdExists(t.Context(), tc.crdName)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.expectExists, exists)
		})
	}
}

func Test_inferencepoolCrdExists(t *testing.T) {
	scheme := runtime.NewScheme()
	apiextensionsv1.AddToScheme(scheme)

	tests := []struct {
		name            string
		existingObjects []runtime.Object
		expectExists    bool
	}{
		{
			name:            "No InferencePool CRD",
			existingObjects: []runtime.Object{},
			expectExists:    false,
		},
		{
			name: "Stable InferencePool CRD exists",
			existingObjects: []runtime.Object{
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.k8s.io",
					},
				},
			},
			expectExists: true,
		},
		{
			name: "Experimental InferencePool CRD exists",
			existingObjects: []runtime.Object{
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.x-k8s.io",
					},
				},
			},
			expectExists: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				Build()
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
			reconciler := &reconciler{
				cache: cache,
			}
			exists, err := reconciler.inferencepoolCrdExists(t.Context())
			assert.NoError(t, err)
			assert.Equal(t, tc.expectExists, exists)
		})
	}
}

