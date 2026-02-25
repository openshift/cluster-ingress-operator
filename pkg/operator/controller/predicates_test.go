package controller_test

import (
	"testing"

	testr "github.com/go-logr/logr/testr"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var fakeObjects = []client.Object{
	&gatewayapiv1.GatewayClass{
		ObjectMeta: v1.ObjectMeta{
			Name: "valid",
		},
		Spec: gatewayapiv1.GatewayClassSpec{
			ControllerName: "openshift.io/gateway-controller/v1",
		},
	},
	&gatewayapiv1.GatewayClass{
		ObjectMeta: v1.ObjectMeta{
			Name: "invalid",
		},
		Spec: gatewayapiv1.GatewayClassSpec{
			ControllerName: "openshift.io/gateway-controller/not-valid",
		},
	},
}

func TestGatewayHasOurController(t *testing.T) {
	logger := testr.NewWithOptions(t, testr.Options{
		LogTimestamp: true,
	})
	scheme := runtime.NewScheme()
	if err := gatewayapiv1.Install(scheme); err != nil {
		t.Fatalf("error creating scheme for fake client: %s", err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fakeObjects...).Build()
	tests := []struct {
		name     string
		resource client.Object
		checkRev bool
		want     bool
	}{
		{
			name: "not a gateway should return false",
			want: false,
			resource: &gatewayapiv1.GatewayClass{
				ObjectMeta: v1.ObjectMeta{
					Name: "xpto",
				},
			},
		},
		{
			name: "a gateway with invalid class should return false",
			want: false,
			resource: &gatewayapiv1.Gateway{
				ObjectMeta: v1.ObjectMeta{
					Name:      "xpto",
					Namespace: "openshift-ingress",
				},
				Spec: gatewayapiv1.GatewaySpec{
					GatewayClassName: "invalid",
				},
			},
		},
		{
			name: "a gateway with non-existing class should return false",
			want: false,
			resource: &gatewayapiv1.Gateway{
				ObjectMeta: v1.ObjectMeta{
					Name:      "xpto",
					Namespace: "openshift-ingress",
				},
				Spec: gatewayapiv1.GatewaySpec{
					GatewayClassName: "notavailable",
				},
			},
		},
		{
			name:     "a gateway with openshift rev labels and enforcing rev validation should return false",
			want:     false,
			checkRev: true,
			resource: &gatewayapiv1.Gateway{
				ObjectMeta: v1.ObjectMeta{
					Name:      "xpto",
					Namespace: "openshift-ingress",
					Labels: map[string]string{
						"istio.io/rev": "openshift-gateway",
					},
				},
				Spec: gatewayapiv1.GatewaySpec{
					GatewayClassName: "valid",
				},
			},
		},
		{
			name:     "a gateway with openshift rev labels but NOT enforcing rev validation should return true",
			want:     true,
			checkRev: false,
			resource: &gatewayapiv1.Gateway{
				ObjectMeta: v1.ObjectMeta{
					Name:      "xpto",
					Namespace: "openshift-ingress",
					Labels: map[string]string{
						"istio.io/rev": "openshift-gateway",
					},
				},
				Spec: gatewayapiv1.GatewaySpec{
					GatewayClassName: "valid",
				},
			},
		},
		{
			name:     "a gateway without openshift rev labels and enforcing rev validation should return true",
			want:     true,
			checkRev: true,
			resource: &gatewayapiv1.Gateway{
				ObjectMeta: v1.ObjectMeta{
					Name:      "xpto",
					Namespace: "openshift-ingress",
					Labels: map[string]string{
						"istio.io/rev": "bla",
					},
				},
				Spec: gatewayapiv1.GatewaySpec{
					GatewayClassName: "valid",
				},
			},
		},
		{
			name: "a gateway with valid class should return true",
			want: true,
			resource: &gatewayapiv1.Gateway{
				ObjectMeta: v1.ObjectMeta{
					Name:      "xpto",
					Namespace: "openshift-ingress",
				},
				Spec: gatewayapiv1.GatewaySpec{
					GatewayClassName: "valid",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Default is to not check the rev label, so we don't set the testFn for now
			// unless our test explicitly request for it
			testFn := controller.GatewayHasOurController(logger, fakeClient, tt.checkRev)
			got := testFn(tt.resource)
			if got != tt.want {
				t.Errorf("GatewayHasOurController() = %v, want %v", got, tt.want)
			}
		})
	}
}
