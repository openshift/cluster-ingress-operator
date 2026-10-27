package udproute_status

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func udpHTTPClient(t *testing.T, initial *unstructured.Unstructured) client.Client {
	t.Helper()
	var mutex sync.Mutex
	current := initial.DeepCopy()
	current.SetResourceVersion("1")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		response.Header().Set("Content-Type", "application/json")
		check := func(err error) bool {
			if !assert.NoError(t, err) {
				http.Error(response, "HTTP fixture failure", http.StatusInternalServerError)
				return false
			}
			return true
		}
		expectedPath := "/apis/gateway.networking.k8s.io/v1/namespaces/" + current.GetNamespace() + "/udproutes/" + current.GetName()
		if request.URL.Path != expectedPath && request.URL.Path != expectedPath+"/status" {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Method == http.MethodGet {
			check(json.NewEncoder(response).Encode(current))
			return
		}
		if !assert.True(t, strings.HasSuffix(request.URL.Path, "/status"), "writes must target only UDPRoute status") {
			http.Error(response, "unexpected write path", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(request.Body)
		if !check(err) {
			return
		}
		var submitted map[string]interface{}
		if !check(json.Unmarshal(body, &submitted)) {
			return
		}
		version, found, err := unstructured.NestedString(submitted, "metadata", "resourceVersion")
		if !check(err) {
			return
		}
		if !assert.True(t, found, "every status update/patch must carry an optimistic resourceVersion") {
			http.Error(response, "missing resourceVersion", http.StatusBadRequest)
			return
		}
		if version != current.GetResourceVersion() {
			response.WriteHeader(http.StatusConflict)
			check(json.NewEncoder(response).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusFailure, Reason: metav1.StatusReasonConflict, Message: "object was modified", Code: http.StatusConflict}))
			return
		}
		switch request.Method {
		case http.MethodPut:
			current.Object["status"] = submitted["status"]
		case http.MethodPatch:
			if !assert.Equal(t, "application/merge-patch+json", request.Header.Get("Content-Type")) {
				http.Error(response, "unexpected content type", http.StatusBadRequest)
				return
			}
			patchClient := fake.NewClientBuilder().WithObjects(current.DeepCopy()).Build()
			patched := current.DeepCopy()
			if !check(patchClient.Patch(request.Context(), patched, client.RawPatch(types.MergePatchType, body))) {
				return
			}
			current = patched
		default:
			t.Errorf("unexpected status method %s", request.Method)
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		numericVersion, err := strconv.Atoi(version)
		if !check(err) {
			return
		}
		current.SetResourceVersion(fmt.Sprint(numericVersion + 1))
		check(json.NewEncoder(response).Encode(current))
	}))
	t.Cleanup(server.Close)
	gvk := initial.GroupVersionKind()
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
	mapper.Add(gvk, meta.RESTScopeNamespace)
	cl, err := client.New(&rest.Config{Host: server.URL}, client.Options{Mapper: mapper})
	require.NoError(t, err)
	return cl
}
