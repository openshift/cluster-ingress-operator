package crd

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/assert"
)

func Test_simpleSubController(t *testing.T) {

	crd := func(name, group, kind string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: group,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Kind: kind,
				},
			},
		}
	}

	fakeStartCtrl := func(ctrl controller.Controller) func() (controller.Controller, error) {
		return func() (controller.Controller, error) {
			// fake start it. Just checking that this function was called
			ctrl.Start(context.Background())
			return ctrl, nil
		}
	}

	scheme := runtime.NewScheme()
	apiextensionsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(crd("test-1", "istio.io", "VirtualService")).
		Build()
	cl := &testutil.FakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
	ctrl := &testutil.FakeController{t, false, nil}
	reconciler := &reconciler{
		client: cl,
		config: Config{
			Mappings: map[metav1.GroupKind]ControllerFunc{
				{Group: "istio.io", Kind: "VirtualService"}: fakeStartCtrl(ctrl),
			},
		},
	}
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "test-1",
		},
	}
	_, err := reconciler.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.True(t, ctrl.Started, "ControllerFunc should have been called")
}
