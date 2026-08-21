package ingress

import (
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func Test_clearRouteStatus_DoesNotMutateOriginal(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := routev1.Install(scheme); err != nil {
		t.Fatalf("failed to add route scheme: %v", err)
	}

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Status: routev1.RouteStatus{
			Ingress: []routev1.RouteIngress{
				{
					RouterName: "router-a",
					Conditions: []routev1.RouteIngressCondition{
						{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue},
					},
				},
				{
					RouterName: "router-b",
					Conditions: []routev1.RouteIngressCondition{
						{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue},
					},
				},
				{
					RouterName: "router-c",
					Conditions: []routev1.RouteIngressCondition{
						{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue},
					},
				},
			},
		},
	}

	// Snapshot the original Ingress slice for comparison.
	snapshot := route.DeepCopy()

	// Build a reconciler with a fake client that has the route.
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(route).
		WithObjects(route).
		Build()
	rec := &reconciler{client: cl}

	// Call the real clearRouteStatus targeting "router-b".
	cleared, err := rec.clearRouteStatus(route, "router-b")
	assert.NoError(t, err)
	assert.True(t, cleared, "expected clearRouteStatus to clear router-b")

	// The original route's Status.Ingress slice must be unchanged.
	assert.Equal(t, snapshot.Status.Ingress, route.Status.Ingress,
		"original route's Status.Ingress was mutated by clearRouteStatus")
	assert.Len(t, route.Status.Ingress, 3,
		"original route should still have 3 ingress entries")
}
