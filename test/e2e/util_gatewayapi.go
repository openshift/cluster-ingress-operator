//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	maistrav2 "github.com/maistra/istio-operator/pkg/apis/maistra/v2"
	configv1 "github.com/openshift/api/config/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	gwapi "sigs.k8s.io/gateway-api/apis/v1beta1"
)

const (
	allNamespaces     = "All"
	defaultPortNumber = 80

	// openshiftOperatorsNamespace holds the expected OSSM subscription and Istio operator pod.
	openshiftOperatorsNamespace = "openshift-operators"
	// openshiftIstioOperatorDeploymentName holds the expected istio-operator deployment name.
	openshiftIstioOperatorDeploymentName = "istio-operator"
	// openshiftIngressNamespace holds many of the test objects and the Istiod proxy pod.
	openshiftIngressNamespace = "openshift-ingress"
	// openshiftIstiodDeploymentName holds the expected istiod proxy deployment name
	openshiftIstiodDeploymentName = "istiod-openshift-gateway"
	// openshiftSMCPName holds the expected OSSM ServiceMeshControlPlane name
	openshiftSMCPName = "openshift-gateway"
)

// updateIngressOperatorRole updates the ingress-operator cluster role with cluster-admin privilege.
// TODO - Remove this function after https://issues.redhat.com/browse/OSSM-3508 is fixed.
func updateIngressOperatorRole(t *testing.T) error {
	t.Helper()

	// Create the same rolebinding that the `oc adm policy add-cluster-role-to-user` command creates.
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster-admin-e2e",
		},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "ingress-operator", Namespace: "openshift-ingress-operator"}},
	}

	// Add the rolebinding to the ingress-operator user.
	if err := kclient.Create(context.TODO(), crb); err != nil {
		if kerrors.IsAlreadyExists(err) {
			t.Logf("rolebinding already exists")
			return nil
		}
		t.Logf("error adding rolebinding: %v", err)
		return err
	}
	t.Log("rolebinding has been added")
	return nil
}

// assertCrdExists checks if the CRD of the given name exists and returns an error if not.
// Otherwise returns the CRD version.
func assertCrdExists(t *testing.T, crdname string) (string, error) {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	name := types.NamespacedName{"", crdname}
	crdVersion := ""

	err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, crd); err != nil {
			t.Logf("failed to get crd %s: %v", name, err)
			return false, nil
		}
		crdConditions := crd.Status.Conditions
		for _, version := range crd.Spec.Versions {
			if version.Served {
				crdVersion = version.Name
			}
		}
		for _, c := range crdConditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		t.Logf("failed to find crd %s to be Established", name)
		return false, nil
	})
	return crdVersion, err
}

// assertCanCreateGatewayClass  attempts to create a gatewayClass with the given name and controller name.
// Returns false if it cannot create the gatewayClass, otherwise returns true and the created gatewayClass.
func assertCanCreateGatewayClass(t *testing.T, name, controllerName string) (bool, *gwapi.GatewayClass) {
	t.Helper()

	gatewayClass, err := createGatewayClass(t, name, controllerName)
	if err != nil {
		t.Logf("error creating gateway class: %v", err)
		return false, nil
	}
	return true, gatewayClass
}

// assertCanCreateGateway  attempts to create a gateway with the given gatewayClass, name, and namespace.
// Returns false if it cannot create the gateway, otherwise returns true and the created gateway.
func assertCanCreateGateway(t *testing.T, gatewayClass *gwapi.GatewayClass, name, namespace, domain string) (bool, *gwapi.Gateway) {
	t.Helper()

	gateway, err := createGateway(gatewayClass, name, namespace, domain)
	if err != nil {
		t.Logf("error creating gateway: %v", err)
		return false, nil
	}
	return true, gateway
}

// assertCanCreateHttpRoute  attempts to create a gatewayClass with the given namespace, name, parent namespace,
// hostname, backend reference name, and gateway.
// Returns false if it cannot create the httpRoute, otherwise returns true.
func assertCanCreateHttpRoute(t *testing.T, ns, name, parentns, hostname, berefname string, gateway *gwapi.Gateway) (bool, *gwapi.HTTPRoute) {
	t.Helper()

	httproute, err := createHttpRoute(ns, name, parentns, hostname, berefname, gateway)
	if err != nil {
		t.Logf("error creating httpRoute: %v", err)
		return false, nil
	}
	return true, httproute
}

// createHttpRoute checks if the HTTPRoute can be created.
// If it can't an error is returned.
func createHttpRoute(namespace, routename, parentnamespace, hostname, backendrefname string, gateway *gwapi.Gateway) (*gwapi.HTTPRoute, error) {
	// Just in case gateway creation failed, supply a fake gateway name.
	name := "NONE"
	if gateway != nil {
		name = gateway.Name
	}

	// Create the backend (service and pod) needed for the route to have resolvedRefs=true.
	// The http route, service, and pod are cleaned up when the namespace is automatically deleted.
	// buildEchoPod builds a pod that listens on port 8080.
	echoPod := buildEchoPod(backendrefname, namespace)
	if err := kclient.Create(context.TODO(), echoPod); err != nil {
		return nil, fmt.Errorf("failed to create pod %s/%s: %v", namespace, echoPod.Name, err)
	}
	// buildEchoService builds a service that targets port 8080.
	echoService := buildEchoService(echoPod.Name, namespace, echoPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), echoService); err != nil {
		return nil, fmt.Errorf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
	}

	httpRoute := buildHTTPRoute(routename, namespace, name, parentnamespace, hostname, backendrefname)
	if err := kclient.Create(context.TODO(), httpRoute); err != nil {
		if kerrors.IsAlreadyExists(err) {
			name := types.NamespacedName{namespace, routename}
			if err = kclient.Get(context.TODO(), name, httpRoute); err == nil {
				return httpRoute, nil
			}
		} else {
			return nil, errors.New("failed to create http route: " + err.Error())
		}
	}
	return httpRoute, nil
}

// createGateway checks if the Gateway can be created.
// If it can, it is returned.  If it can't an error is returned.
func createGateway(gatewayClass *gwapi.GatewayClass, name, namespace, domain string) (*gwapi.Gateway, error) {
	gateway := buildGateway(name, namespace, gatewayClass.Name, allNamespaces, domain)
	if err := kclient.Create(context.TODO(), gateway); err != nil {
		if kerrors.IsAlreadyExists(err) {
			name := types.NamespacedName{namespace, name}
			if err = kclient.Get(context.TODO(), name, gateway); err == nil {
				return gateway, nil
			}
		} else {
			return nil, errors.New("failed to create gateway: " + err.Error())
		}
	}
	return gateway, nil
}

// createGatewayClass checks if the GatewayClass can be created.
// If it can, it is returned.  If it can't an error is returned.
func createGatewayClass(t *testing.T, name, controllerName string) (*gwapi.GatewayClass, error) {
	t.Helper()

	gatewayClass := buildGatewayClass(name, controllerName)
	if err := kclient.Create(context.TODO(), gatewayClass); err != nil {
		if kerrors.IsAlreadyExists(err) {
			name := types.NamespacedName{"", name}
			if err = kclient.Get(context.TODO(), name, gatewayClass); err == nil {
				t.Logf("gateway class already exists")
				return gatewayClass, nil
			}
		} else {
			return nil, errors.New("failed to create gateway class: " + err.Error())
		}
	}
	return gatewayClass, nil
}

// buildGatewayClass initializes the GatewayClass and returns its address.
func buildGatewayClass(name, controllerName string) *gwapi.GatewayClass {
	return &gwapi.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gwapi.GatewayClassSpec{
			ControllerName: gwapi.GatewayController(controllerName),
		},
	}
}

// buildGateway initializes the Gateway and returns its address.
func buildGateway(name, namespace, gcname, fromNs, domain string) *gwapi.Gateway {
	hostname := gwapi.Hostname("*." + domain)
	fromNamespace := gwapi.FromNamespaces(fromNs)
	// Tell the gateway listener to allow routes from the namespace/s in the fromNamespaces variable, which could be "All".
	allowedRoutes := gwapi.AllowedRoutes{Namespaces: &gwapi.RouteNamespaces{From: &fromNamespace}}
	listener1 := gwapi.Listener{Name: "http", Hostname: &hostname, Port: 80, Protocol: "HTTP", AllowedRoutes: &allowedRoutes}

	return &gwapi.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gwapi.GatewaySpec{
			GatewayClassName: gwapi.ObjectName(gcname),
			Listeners:        []gwapi.Listener{listener1},
		},
	}
}

// buildHTTPRoute initializes the HTTPRoute and returns its address.
func buildHTTPRoute(routename, namespace, parentgateway, parentnamespace, hostname, backendrefname string) *gwapi.HTTPRoute {
	parentns := gwapi.Namespace(parentnamespace)
	parent := gwapi.ParentReference{Name: gwapi.ObjectName(parentgateway), Namespace: &parentns}
	port := gwapi.PortNumber(defaultPortNumber)
	rule := gwapi.HTTPRouteRule{
		BackendRefs: []gwapi.HTTPBackendRef{{
			BackendRef: gwapi.BackendRef{
				BackendObjectReference: gwapi.BackendObjectReference{
					Name: gwapi.ObjectName(backendrefname),
					Port: &port,
				},
			},
		}},
	}

	return &gwapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routename, Namespace: namespace},
		Spec: gwapi.HTTPRouteSpec{
			CommonRouteSpec: gwapi.CommonRouteSpec{ParentRefs: []gwapi.ParentReference{parent}},
			Hostnames:       []gwapi.Hostname{gwapi.Hostname(hostname)},
			Rules:           []gwapi.HTTPRouteRule{rule},
		},
	}
}

// getClusterVersion returns the ClusterVersion if found.  If one is not found, it returns an error.
func getClusterVersion() (*configv1.ClusterVersion, error) {
	clusterVersion := &configv1.ClusterVersion{}
	versionName := types.NamespacedName{"", "version"}
	err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), versionName, clusterVersion); err != nil {
			return false, nil
		}
		return true, nil
	})
	return clusterVersion, err
}

// assertSubscription checks if the Subscription of the given name exists and returns an error if not.
func assertSubscription(t *testing.T, namespace, subName string) error {
	t.Helper()
	subscription := &operatorsv1alpha1.Subscription{}
	nsName := types.NamespacedName{namespace, subName}

	err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), nsName, subscription); err != nil {
			t.Logf("failed to get subscription %s, retrying...", subName)
			return false, nil
		}
		t.Logf("found subscription %s at installed version %s", subscription.Name, subscription.Status.InstalledCSV)
		return true, nil
	})
	return err
}

// assertOSSMOperator checks if the OSSM Istio operator gets successfully installed
// and returns an error if not.
func assertOSSMOperator(t *testing.T) error {
	t.Helper()
	dep := &appsv1.Deployment{}
	ns := types.NamespacedName{Namespace: openshiftOperatorsNamespace, Name: openshiftIstioOperatorDeploymentName}

	// Get the Istio operator deployment.
	err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), ns, dep); err != nil {
			t.Logf("failed to get deployment %v, retrying...", ns)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("error finding deployment %v: %v", ns, err)
	}

	// Get the istio-operator pod.
	podlist, err := getPods(t, kclient, dep)
	if err != nil {
		return fmt.Errorf("error finding pod for deployment %v: %v", ns, err)
	}
	if len(podlist.Items) > 1 {
		return fmt.Errorf("too many pods for deployment %v: %d", ns, len(podlist.Items))
	}
	pod := podlist.Items[0]
	if pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("OSSM operator failure: pod %s is not running, it is %v", pod.Name, pod.Status.Phase)
	}

	t.Logf("found OSSM operator pod %s/%s to be %s", pod.Namespace, pod.Name, pod.Status.Phase)
	return nil
}

// assertIstiodProxy checks if the OSSM Istiod proxy gets successfully installed
// and returns an error if not.
func assertIstiodProxy(t *testing.T) error {
	t.Helper()
	dep := &appsv1.Deployment{}
	ns := types.NamespacedName{Namespace: openshiftIngressNamespace, Name: openshiftIstiodDeploymentName}

	// Get the Istiod proxy deployment.
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), ns, dep); err != nil {
			t.Logf("failed to get deployment %v, retrying...", ns)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("error finding deployment %v: %v", ns, err)
	}

	// Get the istiod pod.
	podlist, err := getPods(t, kclient, dep)
	if err != nil {
		return fmt.Errorf("error finding pod for deployment %v: %v", ns, err)
	}
	if len(podlist.Items) > 1 {
		return fmt.Errorf("too many pods for deployment %v: %d", ns, len(podlist.Items))
	}
	pod := podlist.Items[0]
	if pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("Istiod proxy failure: pod %s is not running, it is %v", pod.Name, pod.Status.Phase)
	}

	t.Logf("found istiod pod %s/%s to be %s", pod.Namespace, pod.Name, pod.Status.Phase)
	return nil
}

// assertGatewayClassSuccessful checks if the gateway class was created and accepted successfully
// and returns an error if not.
func assertGatewayClassSuccessful(t *testing.T, name string) (error, *gwapi.GatewayClass) {
	t.Helper()

	gwc := &gwapi.GatewayClass{}
	nsName := types.NamespacedName{"", name}
	recordedConditionMsg := "not found"

	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), nsName, gwc); err != nil {
			t.Logf("failed to get gateway class %s, retrying...", name)
			return false, nil
		}
		for _, condition := range gwc.Status.Conditions {
			if condition.Type == string(gwapi.GatewayClassReasonAccepted) {
				recordedConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					return true, nil
				}
			}
		}
		t.Logf("found gateway class %s, but it is not yet Accepted. Retrying...", name)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("gateway class %s not %v, last recorded status message: %s", name, gwapi.GatewayClassReasonAccepted, recordedConditionMsg), nil
	}

	t.Logf("gateway class %s successful", name)
	return nil, gwc
}

// assertGatewaySuccessful checks if the gateway was created and accepted successfully
// and returns an error if not.
func assertGatewaySuccessful(t *testing.T, namespace, name string) (error, *gwapi.Gateway) {
	t.Helper()

	gw := &gwapi.Gateway{}
	nsName := types.NamespacedName{namespace, name}
	recordedConditionMsg := "not found"

	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), nsName, gw); err != nil {
			t.Logf("failed to get gateway %s, retrying...", name)
			return false, nil
		}
		for _, condition := range gw.Status.Conditions {
			if condition.Type == string(gwapi.GatewayClassReasonAccepted) { // there is no GatewayReasonAccepted!
				recordedConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					t.Logf("found gateway %s/%s as Accepted", namespace, name)
					return true, nil
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("gateway %s not %v, last recorded status message: %s", name, gwapi.GatewayClassReasonAccepted, recordedConditionMsg), nil
	}

	return nil, gw
}

// assertHttpRouteSuccessful checks if the http route was created and has parent conditions that indicate
// it was accepted successfully.  A parent is usually a gateway.  Returns an error not accepted and/or not resolved.
func assertHttpRouteSuccessful(t *testing.T, namespace, name, openshiftIngressNamespace, hostname string, gateway *gwapi.Gateway) (error, *gwapi.HTTPRoute) {
	t.Helper()

	httproute := &gwapi.HTTPRoute{}
	nsName := types.NamespacedName{namespace, name}

	// Wait 1 minute for parent/s to update
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), nsName, httproute); err != nil {
			t.Logf("failed to get httproute %s/%s, retrying...", namespace, name)
			return false, nil
		}
		numParents := len(httproute.Status.Parents)
		if numParents == 0 {
			t.Logf("httpRoute %s/%s has no parent conditions, retrying...", namespace, name)
			return false, nil
		}
		t.Logf("found httproute %s/%s with %d parent/s", namespace, name, numParents)
		return true, nil
	})
	if err != nil {
		return err, nil
	}

	acceptedConditionMsg := "no accepted parent conditions"
	resolvedRefConditionMsg := "no resolved ref parent conditions"
	accepted := false
	resolvedRefs := false

	// The http route must have at least one parent for which it is successful.
	// TODO - If it must be successful for all parents, this will need to change.
	for _, parent := range httproute.Status.Parents {
		// For each parent conditions should be true for both Accepted and ResolvedRefs
		for _, condition := range parent.Conditions {
			switch condition.Type {
			case string(gwapi.RouteConditionAccepted):
				acceptedConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					accepted = true
				}
			case string(gwapi.RouteConditionResolvedRefs):
				resolvedRefConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					resolvedRefs = true
				}
			}
		}
		// Check the results for each parent.
		switch {
		case !accepted && !resolvedRefs:
			return fmt.Errorf("httpRoute %s/%s, parent %v/%v neither %v nor %v, last recorded status messages: %s, %s", namespace, name, parent.ParentRef.Namespace, parent.ParentRef.Name, gwapi.RouteConditionAccepted, gwapi.RouteConditionResolvedRefs, acceptedConditionMsg, resolvedRefConditionMsg), nil
		case !accepted:
			return fmt.Errorf("httpRoute %s/%s, parent %v/%v not %v, last recorded status message: %s", namespace, name, parent.ParentRef.Namespace, parent.ParentRef.Name, gwapi.RouteConditionAccepted, acceptedConditionMsg), nil
		case !resolvedRefs:
			return fmt.Errorf("httpRoute %s/%s, parent %v/%v not %v, last recorded status message: %s", namespace, name, parent.ParentRef.Namespace, parent.ParentRef.Name, gwapi.RouteConditionResolvedRefs, resolvedRefConditionMsg), nil
		}
	}
	t.Logf("httpRoute %s/%s successful", namespace, name)
	return nil, httproute
}

// assertHttpRouteConnection checks if the http route of the given name replies successfully,
// and returns an error if not
func assertHttpRouteConnection(t *testing.T, name string) error {
	t.Helper()

	// Create the http client to check the header.
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// TODO - We may want to wait and check that the dns name resolves first.

	// Wait for http route to respond, and when it does, check for the status code.
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		statusCode, err := getHttpResponse(client, name)
		if err != nil {
			t.Logf("GET %s failed: %v, retrying...", name, err)
			return false, nil
		}
		if statusCode != http.StatusOK {
			t.Logf("GET %s failed: status %v, expected %v, retrying...", name, statusCode, http.StatusOK)
			return false, nil // retry on 503 as pod/service may not be ready
		}

		t.Logf("request to %s was successful", name)
		return true, nil
	}); err != nil {
		t.Fatalf("error contacting %s's endpoint: %v", name, err)
	}

	return nil
}

func getHttpResponse(client *http.Client, name string) (int, error) {
	// Send the HTTP request.
	response, err := client.Get("http://" + name)
	if err != nil {
		return 0, fmt.Errorf("GET %s failed: %v", name, err)
	}

	// Close response body.
	defer response.Body.Close()

	return response.StatusCode, nil
}

// assertCatalogSource checks if the CatalogSource of the given name exists,
// and returns an error if not.
func assertCatalogSource(t *testing.T, namespace, csName string) error {
	t.Helper()
	catalogSource := &operatorsv1alpha1.CatalogSource{}
	nsName := types.NamespacedName{namespace, csName}

	err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), nsName, catalogSource); err != nil {
			t.Logf("failed to get catalogSource %s: %v, retrying...", csName, err)
			return false, nil
		}
		if catalogSource.Status.GRPCConnectionState != nil && catalogSource.Status.GRPCConnectionState.LastObservedState == "READY" {
			t.Logf("found catalogSource %s with last observed state %s", catalogSource.Name, catalogSource.Status.GRPCConnectionState.LastObservedState)
			return true, nil
		}
		t.Logf("found catalogSource %s but could not determine last observed state, retrying...", catalogSource.Name)
		return false, nil
	})
	return err
}

// assertSMCP checks if the ServiceMeshControlPlane exists,
// and returns an error if not.
func assertSMCP(t *testing.T) error {
	t.Helper()
	smcp := &maistrav2.ServiceMeshControlPlane{}
	nsName := types.NamespacedName{openshiftIngressNamespace, openshiftSMCPName}

	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), nsName, smcp); err != nil {
			t.Logf("failed to get ServiceMeshControlPlane %s/%s, retrying...", nsName.Namespace, nsName.Name)
			return false, nil
		}
		if smcp.Status.Readiness.Components != nil {
			pending := len(smcp.Status.Readiness.Components["pending"]) > 0
			unready := len(smcp.Status.Readiness.Components["unready"]) > 0
			if pending || unready {
				t.Logf("found ServiceMeshControlPlane %s/%s, but it isn't ready. Retrying...", smcp.Namespace, smcp.Name)
				return false, nil
			}
			if len(smcp.Status.Readiness.Components["ready"]) > 0 {
				t.Logf("found ServiceMeshControlPlane %s/%s with ready components: %v", smcp.Namespace, smcp.Name, smcp.Status.Readiness.Components["ready"])
				return true, nil
			}
		}
		t.Logf("found ServiceMeshControlPlane %s/%s but could not determine its readiness. Retrying...", smcp.Namespace, smcp.Name)
		return false, nil
	})
	return err
}

// getDefaultDomain returns the appsDomain from the cluster ingress config.
func getDefaultDomain(t *testing.T) string {
	t.Helper()

	ing := &configv1.Ingress{}
	appsDomain := ""

	err := wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		// Get the ingress config
		if err := kclient.Get(context.TODO(), clusterConfigName, ing); err != nil {
			t.Logf("get ingress config failed: %v, retrying...", err)
			return false, nil
		}
		appsDomain = ing.Spec.Domain
		t.Logf("appsDomain is %s", appsDomain)
		return true, nil
	})
	if err != nil {
		t.Errorf("could not get default appsDomain: %v", err)
	}
	return appsDomain
}

// assert checks if the
// and returns an error if not.
/* Need:
func assertIstioIngressGateway?
func assertDNSRecord
func assertLoadBalancer?

func assert(t *testing.T, name string) error {
	t.Helper()
	return nil
}
*/
