package listenerset_status

import (
	"context"
	"strings"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ctrltestutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestReconcile(t *testing.T) {
	gc := &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-default",
		},
		Spec: gatewayapiv1.GatewayClassSpec{
			ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName,
		},
	}
	unmanagedGC := &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-class",
		},
		Spec: gatewayapiv1.GatewayClassSpec{
			ControllerName: "example.com/other-controller",
		},
	}
	gw := &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "openshift-ingress",
		},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: "openshift-default",
			Listeners: []gatewayapiv1.Listener{
				{
					Name:     "http",
					Port:     80,
					Protocol: gatewayapiv1.HTTPProtocolType,
				},
			},
		},
	}
	ls := &gatewayapiv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ls",
			Namespace: "openshift-ingress",
		},
		Spec: gatewayapiv1.ListenerSetSpec{
			ParentRef: gatewayapiv1.ParentGatewayReference{
				Name: "test-gw",
			},
			Listeners: []gatewayapiv1.ListenerEntry{
				{
					Name:     "extra",
					Port:     8443,
					Protocol: gatewayapiv1.HTTPSProtocolType,
				},
			},
		},
	}
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: ls.Namespace,
			Name:      ls.Name,
		},
	}

	tests := []struct {
		name            string
		objects         []runtime.Object
		expectedMetric  string
		expectedCount   int
		expectCondition bool
		// removeObject, if set, is called after the first reconcile
		// to delete an object before a second reconcile that should
		// clean up the metric.
		removeObject func(t *testing.T, r *reconciler)
	}{
		{
			name:    "metric and condition set when ListenerSet targets managed Gateway",
			objects: []runtime.Object{gc, gw, ls},
			expectedMetric: `
# HELP ingress_operator_listenerset_on_managed_gateway Set to 1 when a ListenerSet targets an OpenShift-managed Gateway. ListenerSets are not yet supported and may cause unexpected traffic behavior on upgrade.
# TYPE ingress_operator_listenerset_on_managed_gateway gauge
ingress_operator_listenerset_on_managed_gateway{listenerset_name="test-ls",listenerset_namespace="openshift-ingress"} 1
`,
			expectedCount:   1,
			expectCondition: true,
		},
		{
			name:          "metric cleared when ListenerSet not found",
			objects:       []runtime.Object{gc, gw},
			expectedCount: 0,
		},
		{
			name:          "metric cleared when Gateway not found",
			objects:       []runtime.Object{gc, ls},
			expectedCount: 0,
		},
		{
			name:          "metric cleared when GatewayClass not found",
			objects:       []runtime.Object{gw, ls},
			expectedCount: 0,
		},
		{
			name:          "metric cleared when GatewayClass has different controller",
			objects:       []runtime.Object{unmanagedGC, gwWithClass("other-class"), ls},
			expectedCount: 0,
		},
		{
			name:          "metric cleaned up after ListenerSet deletion",
			objects:       []runtime.Object{gc, gw, ls},
			expectedCount: 0,
			removeObject: func(t *testing.T, r *reconciler) {
				if err := r.client.Delete(context.Background(), ls.DeepCopy()); err != nil {
					t.Fatalf("failed to delete ListenerSet: %v", err)
				}
			},
		},
		{
			name:          "metric cleaned up after Gateway deletion",
			objects:       []runtime.Object{gc, gw, ls},
			expectedCount: 0,
			removeObject: func(t *testing.T, r *reconciler) {
				if err := r.client.Delete(context.Background(), gw.DeepCopy()); err != nil {
					t.Fatalf("failed to delete Gateway: %v", err)
				}
			},
		},
		{
			name:          "metric cleaned up after GatewayClass deletion",
			objects:       []runtime.Object{gc, gw, ls},
			expectedCount: 0,
			removeObject: func(t *testing.T, r *reconciler) {
				if err := r.client.Delete(context.Background(), gc.DeepCopy()); err != nil {
					t.Fatalf("failed to delete GatewayClass: %v", err)
				}
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := gatewayapiv1.Install(scheme); err != nil {
		t.Fatalf("failed to install gateway API scheme: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			listenerSetOnManagedGatewayMetric.Reset()

			clientObjects := make([]runtime.Object, len(tc.objects))
			copy(clientObjects, tc.objects)

			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(clientObjects...).
				WithStatusSubresource(&gatewayapiv1.ListenerSet{}).
				Build()
			informer := informertest.FakeInformers{Scheme: scheme}
			fakeCache := ctrltestutil.FakeCache{Informers: &informer, Reader: cl}

			r := &reconciler{
				client: cl,
				cache:  fakeCache,
			}

			_, err := r.Reconcile(context.Background(), request)
			assert.NoError(t, err)

			if tc.removeObject != nil {
				assert.Equal(t, 1, promtestutil.CollectAndCount(listenerSetOnManagedGatewayMetric), "metric should be set before deletion")
				tc.removeObject(t, r)
				_, err = r.Reconcile(context.Background(), request)
				assert.NoError(t, err)
			}

			count := promtestutil.CollectAndCount(listenerSetOnManagedGatewayMetric)
			assert.Equal(t, tc.expectedCount, count, "unexpected metric count")

			if tc.expectedMetric != "" {
				err := promtestutil.CollectAndCompare(listenerSetOnManagedGatewayMetric, strings.NewReader(tc.expectedMetric))
				assert.NoError(t, err, "metric mismatch")
			}

			if tc.expectCondition {
				updatedLS := &gatewayapiv1.ListenerSet{}
				err := cl.Get(context.Background(), request.NamespacedName, updatedLS)
				assert.NoError(t, err)
				cond := apimeta.FindStatusCondition(updatedLS.Status.Conditions, string(gatewayapiv1.ListenerSetConditionAccepted))
				if assert.NotNil(t, cond, "Accepted condition should be set") {
					assert.Equal(t, metav1.ConditionFalse, cond.Status)
					assert.Equal(t, ReasonUnsupportedByController, cond.Reason)
				}
			}
		})
	}
}

func gwWithClass(className string) *gatewayapiv1.Gateway {
	return &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "openshift-ingress",
		},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(className),
			Listeners: []gatewayapiv1.Listener{
				{
					Name:     "http",
					Port:     80,
					Protocol: gatewayapiv1.HTTPProtocolType,
				},
			},
		},
	}
}
