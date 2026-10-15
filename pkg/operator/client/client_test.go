package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestIngressSchemeUsesV1(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		kind   string
		object runtime.Object
	}{
		{kind: "Ingress", object: &operatorv1.Ingress{}},
		{kind: "IngressList", object: &operatorv1.IngressList{}},
	} {
		t.Run(testCase.kind, func(t *testing.T) {
			t.Parallel()
			kinds, unversioned, err := GetScheme().ObjectKinds(testCase.object)
			require.NoError(t, err)
			require.False(t, unversioned)
			require.Equal(t, []schema.GroupVersionKind{{Group: "operator.openshift.io", Version: "v1", Kind: testCase.kind}}, kinds)
		})
	}
}

func TestIngressClientUsesV1(t *testing.T) {
	t.Parallel()
	groupVersion := "operator.openshift.io/v1"
	ingress := &operatorv1.Ingress{
		TypeMeta:   metav1.TypeMeta{APIVersion: groupVersion, Kind: "Ingress"},
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Generation: 1},
		Spec: operatorv1.IngressSpec{GatewayAPI: operatorv1.GatewayAPIIngressConfig{
			ManagementMode: operatorv1.GatewayAPIManagementModeUnmanaged,
		}},
	}
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		var response interface{}
		switch request.URL.Path {
		case "/api":
			response = &metav1.APIVersions{Versions: []string{"v1"}}
		case "/api/v1":
			response = &metav1.APIResourceList{GroupVersion: "v1"}
		case "/apis":
			response = &metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name:             "operator.openshift.io",
				Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: groupVersion, Version: "v1"}},
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: groupVersion, Version: "v1"},
			}}}
		case "/apis/operator.openshift.io/v1":
			response = &metav1.APIResourceList{GroupVersion: groupVersion, APIResources: []metav1.APIResource{{
				Name: "ingresses", SingularName: "ingress", Kind: "Ingress", Namespaced: false,
				Verbs: metav1.Verbs{"get", "patch"},
			}}}
		case "/apis/operator.openshift.io/v1/ingresses/cluster":
			requests <- request.Method + " " + request.URL.Path
			response = ingress
		case "/apis/operator.openshift.io/v1/ingresses/cluster/status":
			requests <- request.Method + " " + request.URL.Path
			var patch operatorv1.Ingress
			if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
				t.Errorf("decode status patch: %v", err)
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			if patch.Status.ObservedGeneration != 1 {
				t.Errorf("status patch observedGeneration = %d, want 1", patch.Status.ObservedGeneration)
			}
			response = ingress
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	kubeClient, err := NewClient(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	current := &operatorv1.Ingress{}
	require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, current))
	require.Equal(t, operatorv1.GatewayAPIManagementModeUnmanaged, current.Spec.GatewayAPI.ManagementMode)
	updated := current.DeepCopy()
	updated.Status.ObservedGeneration = current.Generation
	require.NoError(t, kubeClient.Status().Patch(context.Background(), updated, client.MergeFrom(current)))
	require.Equal(t, "GET /apis/operator.openshift.io/v1/ingresses/cluster", <-requests)
	require.Equal(t, "PATCH /apis/operator.openshift.io/v1/ingresses/cluster/status", <-requests)
}
