//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	udproutestatuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/udproute-status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	condutils "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func testUDPRouteNotAccepted(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crdKey := client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}
	if err := kclient.Get(t.Context(), crdKey, crd); err != nil {
		if errors.IsNotFound(err) {
			t.Skipf("UDPRoute CRD %s is not installed on this cluster; skipping UDPRoute status and metric assertions", crdKey.Name)
		}
		t.Fatalf("failed to get UDPRoute CRD %s: %v", crdKey.Name, err)
	}
	udpVersion := ""
	for _, wanted := range []string{"v1", "v1alpha2"} {
		for _, version := range crd.Spec.Versions {
			if udpVersion == "" && version.Name == wanted && version.Served {
				udpVersion = wanted
			}
		}
	}
	require.NotEmpty(t, udpVersion, "UDPRoute CRD must serve a supported version")

	managedGatewayName := names.SimpleNameGenerator.GenerateName("test-udp-gw-")
	managedGateway := &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: managedGatewayName, Namespace: operatorcontroller.DefaultOperandNamespace},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: operatorcontroller.OpenShiftDefaultGatewayClassName,
			Listeners:        []gatewayapiv1.Listener{{Name: "http", Port: 80, Protocol: gatewayapiv1.HTTPProtocolType}},
		},
	}
	require.NoError(t, kclient.Create(t.Context(), managedGateway), "failed to create managed Gateway %s/%s", managedGateway.Namespace, managedGateway.Name)
	t.Cleanup(func() {
		if err := kclient.Delete(context.Background(), managedGateway); err != nil && !errors.IsNotFound(err) {
			t.Logf("failed to delete managed Gateway: %v", err)
		}
	})

	unmanagedClass := &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: names.SimpleNameGenerator.GenerateName("test-udp-unmanaged-")},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: "example.com/udp-test-controller"},
	}
	require.NoError(t, kclient.Create(t.Context(), unmanagedClass), "failed to create unmanaged GatewayClass %s", unmanagedClass.Name)
	t.Cleanup(func() {
		if err := kclient.Delete(context.Background(), unmanagedClass); err != nil && !errors.IsNotFound(err) {
			t.Logf("failed to delete unmanaged GatewayClass: %v", err)
		}
	})
	unmanagedGateway := &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: names.SimpleNameGenerator.GenerateName("test-udp-unmanaged-gw-"), Namespace: operatorcontroller.DefaultOperandNamespace},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(unmanagedClass.Name),
			Listeners:        []gatewayapiv1.Listener{{Name: "http", Port: 80, Protocol: gatewayapiv1.HTTPProtocolType}},
		},
	}
	require.NoError(t, kclient.Create(t.Context(), unmanagedGateway), "failed to create unmanaged Gateway %s/%s", unmanagedGateway.Namespace, unmanagedGateway.Name)
	t.Cleanup(func() {
		if err := kclient.Delete(context.Background(), unmanagedGateway); err != nil && !errors.IsNotFound(err) {
			t.Logf("failed to delete unmanaged Gateway: %v", err)
		}
	})

	createUDPRoute := func(name string) *unstructured.Unstructured {
		route := udpRouteTestObject(udpVersion)
		route.SetName(name)
		route.SetNamespace(operatorcontroller.DefaultOperandNamespace)
		route.Object["spec"] = map[string]interface{}{"parentRefs": []interface{}{map[string]interface{}{"name": managedGatewayName}}, "rules": []interface{}{map[string]interface{}{"backendRefs": []interface{}{map[string]interface{}{"name": "unused-backend", "port": int64(53)}}}}}
		require.NoError(t, kclient.Create(t.Context(), route), "failed to create UDPRoute %s/%s", route.GetNamespace(), route.GetName())
		t.Cleanup(func() {
			if err := kclient.Delete(context.Background(), route); err != nil && !errors.IsNotFound(err) {
				t.Logf("failed to delete UDPRoute: %v", err)
			}
		})
		return route
	}

	route1 := createUDPRoute(names.SimpleNameGenerator.GenerateName("test-udproute-"))
	route2 := createUDPRoute(names.SimpleNameGenerator.GenerateName("test-udproute-"))

	for _, route := range []*unstructured.Unstructured{route1, route2} {
		routeKey := client.ObjectKeyFromObject(route)
		assert.EventuallyWithT(t, func(collect *assert.CollectT) {
			current := udpRouteTestObject(udpVersion)
			if err := kclient.Get(t.Context(), routeKey, current); err != nil {
				assert.NoError(collect, err, "failed to get UDPRoute %s", routeKey)
				return
			}
			matches, err := udpRouteHasRejectedManagedParent(current)
			if !assert.NoError(collect, err, "failed to inspect UDPRoute %s status", routeKey) {
				return
			}
			assert.True(collect, matches, "owned status must match the full admitted parent and current generation, without any owned Accepted=True")
		}, 2*time.Minute, 2*time.Second, "UDPRoute %s did not get an exact owned Accepted=False status or had an owned Accepted=True", route.GetName())
	}

	prometheusClient := createPrometheusClient(t)
	metricQuery := func(route *unstructured.Unstructured) string {
		return fmt.Sprintf(`ingress_operator_udproute_on_managed_gateway{udproute_name="%s",udproute_namespace="%s"}`, route.GetName(), route.GetNamespace())
	}
	assertMetricValue(t, prometheusClient, metricQuery(route1), 1, "Prometheus metric was not set for UDPRoute 1")
	assertMetricValue(t, prometheusClient, metricQuery(route2), 1, "Prometheus metric was not set for UDPRoute 2")

	require.NoError(t, kclient.Delete(t.Context(), route1), "failed to delete UDPRoute %s/%s", route1.GetNamespace(), route1.GetName())
	assertMetricGone(t, prometheusClient, metricQuery(route1), "Prometheus metric was not cleaned up after UDPRoute deletion")

	current := udpRouteTestObject(udpVersion)
	require.NoError(t, kclient.Get(t.Context(), client.ObjectKeyFromObject(route2), current), "failed to get UDPRoute %s/%s before retargeting", route2.GetNamespace(), route2.GetName())
	require.NoError(t, unstructured.SetNestedSlice(current.Object, []interface{}{map[string]interface{}{"name": unmanagedGateway.Name}}, "spec", "parentRefs"), "failed to retarget UDPRoute %s/%s", route2.GetNamespace(), route2.GetName())
	require.NoError(t, kclient.Update(t.Context(), current), "failed to update retargeted UDPRoute %s/%s", route2.GetNamespace(), route2.GetName())
	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		if err := kclient.Get(t.Context(), client.ObjectKeyFromObject(route2), current); err != nil {
			assert.NoError(collect, err, "failed to get retargeted UDPRoute %s", client.ObjectKeyFromObject(route2))
			return
		}
		parents, err := udpRouteTestParents(current)
		if !assert.NoError(collect, err, "failed to inspect retargeted UDPRoute %s status", client.ObjectKeyFromObject(route2)) {
			return
		}
		for _, parentStatus := range parents {
			assert.NotEqual(collect, gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName), parentStatus.ControllerName, "stale CIO-owned parent status must be removed after retargeting")
		}
	}, 2*time.Minute, 2*time.Second, "stale CIO-owned UDPRoute parent status was not removed after retargeting")
	assertMetricGone(t, prometheusClient, metricQuery(route2), "Prometheus metric was not cleaned up after UDPRoute retargeting")
}

func admittedUDPRouteParentRefs(route *unstructured.Unstructured) ([]gatewayapiv1.ParentReference, error) {
	values, _, err := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if err != nil {
		return nil, err
	}
	parents := make([]gatewayapiv1.ParentReference, len(values))
	for index, value := range values {
		parent, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid admitted UDPRoute parentRef %T", value)
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(parent, &parents[index]); err != nil {
			return nil, err
		}
	}
	return parents, nil
}

func Example_admittedUDPRouteParentRefs() {
	group, kind := gatewayapiv1.Group(gatewayapiv1.GroupName), gatewayapiv1.Kind("Gateway")
	namespace, section, port := gatewayapiv1.Namespace("cross-namespace"), gatewayapiv1.SectionName("udp"), gatewayapiv1.PortNumber(53)
	want := gatewayapiv1.ParentReference{Group: &group, Kind: &kind, Name: "gateway", Namespace: &namespace, SectionName: &section, Port: &port}
	route := udpRouteTestObject("v1")
	route.Object["spec"] = map[string]interface{}{"parentRefs": []interface{}{map[string]interface{}{"group": gatewayapiv1.GroupName, "kind": "Gateway", "name": "gateway", "namespace": "cross-namespace", "sectionName": "udp", "port": int64(53)}}}
	parents, err := admittedUDPRouteParentRefs(route)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(parents) == 1 && reflect.DeepEqual(want, parents[0]))
	// Output: true
}

func udpRouteTestObject(version string) *unstructured.Unstructured {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: gatewayapiv1.GroupName, Version: version, Kind: "UDPRoute"})
	return route
}

func udpRouteTestParents(route *unstructured.Unstructured) ([]gatewayapiv1.RouteParentStatus, error) {
	values, _, err := unstructured.NestedSlice(route.Object, "status", "parents")
	if err != nil {
		return nil, fmt.Errorf("invalid UDPRoute status parents: %w", err)
	}
	parents := make([]gatewayapiv1.RouteParentStatus, len(values))
	for index, value := range values {
		parent, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid UDPRoute status parent %d: %T", index, value)
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(parent, &parents[index]); err != nil {
			return nil, fmt.Errorf("invalid UDPRoute status parent %d: %w", index, err)
		}
	}
	return parents, nil
}

func udpRouteHasRejectedManagedParent(route *unstructured.Unstructured) (bool, error) {
	refs, err := admittedUDPRouteParentRefs(route)
	if err != nil {
		return false, err
	}
	if len(refs) != 1 {
		return false, fmt.Errorf("expected exactly one admitted UDPRoute parent, got %d", len(refs))
	}
	parents, err := udpRouteTestParents(route)
	if err != nil {
		return false, err
	}
	foundExpected := false
	for _, parent := range parents {
		if parent.ControllerName != gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName) {
			continue
		}
		condition := condutils.FindStatusCondition(parent.Conditions, string(gatewayapiv1.RouteConditionAccepted))
		if condition != nil && condition.Status == metav1.ConditionTrue {
			return false, nil
		}
		if reflect.DeepEqual(parent.ParentRef, refs[0]) {
			foundExpected = condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == udproutestatuscontroller.ReasonUnsupportedByController && condition.ObservedGeneration == route.GetGeneration()
		}
	}
	return foundExpected, nil
}

func udpRouteStatusAssertionFixture() *unstructured.Unstructured {
	route := udpRouteTestObject("v1")
	route.SetGeneration(7)
	ref := map[string]interface{}{"group": gatewayapiv1.GroupName, "kind": "Gateway", "namespace": "cross-namespace", "name": "gateway", "sectionName": "udp", "port": int64(53)}
	route.Object["spec"] = map[string]interface{}{"parentRefs": []interface{}{runtime.DeepCopyJSONValue(ref)}}
	route.Object["status"] = map[string]interface{}{"parents": []interface{}{map[string]interface{}{
		"parentRef": runtime.DeepCopyJSONValue(ref), "controllerName": operatorcontroller.OpenShiftGatewayClassControllerName,
		"conditions": []interface{}{map[string]interface{}{"type": "Accepted", "status": "False", "reason": udproutestatuscontroller.ReasonUnsupportedByController, "observedGeneration": int64(7)}},
	}}}
	return route
}

func Example_udpRouteHasRejectedManagedParent() {
	cases := []struct {
		name   string
		mutate func(map[string]interface{})
		want   bool
	}{
		{name: "full admitted identity", want: true},
		{name: "wrong group", mutate: func(parent map[string]interface{}) {
			parent["parentRef"].(map[string]interface{})["group"] = "example.com"
		}},
		{name: "wrong kind", mutate: func(parent map[string]interface{}) { parent["parentRef"].(map[string]interface{})["kind"] = "Service" }},
		{name: "wrong namespace", mutate: func(parent map[string]interface{}) {
			parent["parentRef"].(map[string]interface{})["namespace"] = "other"
		}},
		{name: "wrong name", mutate: func(parent map[string]interface{}) { parent["parentRef"].(map[string]interface{})["name"] = "other" }},
		{name: "wrong section", mutate: func(parent map[string]interface{}) {
			parent["parentRef"].(map[string]interface{})["sectionName"] = "other"
		}},
		{name: "wrong port", mutate: func(parent map[string]interface{}) { parent["parentRef"].(map[string]interface{})["port"] = int64(54) }},
		{name: "wrong controller", mutate: func(parent map[string]interface{}) { parent["controllerName"] = "example.com/foreign" }},
		{name: "stale generation", mutate: func(parent map[string]interface{}) {
			parent["conditions"].([]interface{})[0].(map[string]interface{})["observedGeneration"] = int64(6)
		}},
		{name: "wrong reason", mutate: func(parent map[string]interface{}) {
			parent["conditions"].([]interface{})[0].(map[string]interface{})["reason"] = "Other"
		}},
		{name: "unknown acceptance", mutate: func(parent map[string]interface{}) {
			parent["conditions"].([]interface{})[0].(map[string]interface{})["status"] = "Unknown"
		}},
		{name: "owned accepted true", mutate: func(parent map[string]interface{}) {
			parent["conditions"].([]interface{})[0].(map[string]interface{})["status"] = "True"
		}},
		{name: "missing acceptance", mutate: func(parent map[string]interface{}) { delete(parent, "conditions") }},
	}
	for _, test := range cases {
		route := udpRouteStatusAssertionFixture()
		parent := route.Object["status"].(map[string]interface{})["parents"].([]interface{})[0].(map[string]interface{})
		if test.mutate != nil {
			test.mutate(parent)
		}
		before := route.DeepCopy()
		got, err := udpRouteHasRejectedManagedParent(route)
		if err != nil || got != test.want || !reflect.DeepEqual(before, route) {
			panic(fmt.Sprintf("%s: got %t, want %t, error %v, input changed %t", test.name, got, test.want, err, !reflect.DeepEqual(before, route)))
		}
	}
	for _, controller := range []string{operatorcontroller.OpenShiftGatewayClassControllerName, "example.com/foreign"} {
		route := udpRouteStatusAssertionFixture()
		status := route.Object["status"].(map[string]interface{})
		parents := status["parents"].([]interface{})
		extra := runtime.DeepCopyJSONValue(parents[0]).(map[string]interface{})
		extra["controllerName"] = controller
		extra["parentRef"].(map[string]interface{})["name"] = "other"
		extra["conditions"].([]interface{})[0].(map[string]interface{})["status"] = "True"
		extra["futureForeignField"] = map[string]interface{}{"retained": true}
		status["parents"] = append(parents, extra)
		before := route.DeepCopy()
		got, err := udpRouteHasRejectedManagedParent(route)
		want := controller != operatorcontroller.OpenShiftGatewayClassControllerName
		if err != nil || got != want || !reflect.DeepEqual(before, route) {
			panic(fmt.Sprintf("additional Accepted=True for %s: got %t, want %t, error %v", controller, got, want, err))
		}
	}
	fmt.Printf("verified %d exact status cases without changing input\n", len(cases)+2)
	// Output: verified 15 exact status cases without changing input
}

func Example_udpRouteTestParents() {
	malformed := []interface{}{
		"not a slice",
		map[string]interface{}{"not": "a slice"},
		[]interface{}{"not a parent map"},
		[]interface{}{nil},
		[]interface{}{map[string]interface{}{"parentRef": "not a reference"}},
		[]interface{}{map[string]interface{}{"controllerName": int64(1)}},
		[]interface{}{map[string]interface{}{"conditions": "not a slice"}},
		[]interface{}{map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"observedGeneration": "not an integer"}}}},
	}
	for index, parents := range malformed {
		route := udpRouteTestObject("v1")
		route.Object["status"] = map[string]interface{}{"parents": parents}
		if _, err := udpRouteTestParents(route); err == nil {
			panic(fmt.Sprintf("malformed case %d did not return a parse error", index))
		}
	}
	route := udpRouteTestObject("v1")
	parents, err := udpRouteTestParents(route)
	if err != nil || parents == nil || len(parents) != 0 {
		panic(fmt.Sprintf("absent parents must return an allocated empty slice, got %v, %v", parents, err))
	}
	fmt.Printf("verified %d malformed cases and allocated absent parents without panic\n", len(malformed))
	// Output: verified 8 malformed cases and allocated absent parents without panic
}
