//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	maistrav2 "github.com/maistra/istio-operator/pkg/apis/maistra/v2"
	v1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	allNamespaces     = "All"
	defaultPortNumber = 80

	// openshiftOperatorsNamespace holds the expected OSSM subscription and Istio operator pod.
	openshiftOperatorsNamespace = "openshift-operators"
	// openshiftIstioOperatorDeploymentName holds the expected istio-operator deployment name.
	openshiftIstioOperatorDeploymentName = "istio-operator"
	// openshiftIstiodDeploymentName holds the expected istiod deployment name
	openshiftIstiodDeploymentName = "istiod-openshift-gateway"
	// openshiftSMCPName holds the expected OSSM ServiceMeshControlPlane name
	openshiftSMCPName = "openshift-gateway"
	// cvoNamespace is the namespace of cluster version operator (CVO).
	cvoNamespace = "openshift-cluster-version"
	// cvoDeploymentName is the name of cluster version operator's deployment.
	cvoDeploymentName = "cluster-version-operator"
)

// updateIngressOperatorRole updates the ingress-operator cluster role with cluster-admin privilege.
// TODO - Remove this function after https://issues.redhat.com/browse/OSSM-3508 is fixed.
func updateIngressOperatorRole(t *testing.T) error {
	t.Helper()

	// Create the same rolebinding that the `oc adm policy add-cluster-role-to-user` command creates.
	// Caller must remove this after setting it.
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster-admin-e2e",
		},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "ingress-operator", Namespace: operatorcontroller.DefaultOperatorNamespace}},
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
	name := types.NamespacedName{Namespace: "", Name: crdname}
	crdVersion := ""

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, name, crd); err != nil {
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

// deleteExistingCRD deletes if the CRD of the given name exists and returns an error if not.
func deleteExistingCRD(t *testing.T, crdName string) error {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	newCRD := &apiextensionsv1.CustomResourceDefinition{}
	name := types.NamespacedName{Namespace: "", Name: crdName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, name, crd); err != nil {
			t.Logf("failed to get crd %s: %v", name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Errorf("failed to get crd %s: %v", name, err)
		return err
	}
	// deleting CRD.
	err = kclient.Delete(context.Background(), crd)
	if err != nil {
		t.Errorf("failed to delete crd %s: %v", name, err)
		return err
	}
	err = wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, name, newCRD); err != nil {
			if kerrors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("failed to delete gatewayAPI CRD %s: %v", crdName, err)
			return false, nil
		}
		// if new CRD got recreated while the poll ensures the CRD is deleted.
		if newCRD != nil && newCRD.UID != crd.UID {
			return true, nil
		}
		t.Logf("crd %s still exists", crdName)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("timed out waiting for gatewayAPI CRD %s to be deleted: %v", crdName, err)
	}
	t.Logf("deleted crd %s", crdName)
	return nil
}

// deleteExistingVAP deletes if the ValidatingAdmissionPolicy of the given name exists and returns an error if not.
func deleteExistingVAP(t *testing.T, vapName string) error {
	t.Helper()

	vap := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	newVAP := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	name := types.NamespacedName{Name: vapName}

	// Retrieve the object to be deleted.
	if err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, name, vap); err != nil {
			t.Logf("failed to get vap %q: %v, retrying ...", vapName, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to get vap %q: %w", vapName, err)
	}

	if err := kclient.Delete(context.Background(), vap); err != nil {
		return fmt.Errorf("failed to delete vap %q: %w", vapName, err)
	}

	// Verify VAP was not recreated.
	if err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, name, newVAP); err != nil {
			if kerrors.IsNotFound(err) {
				// VAP does not exist as expected.
				return true, nil
			}
			t.Logf("failed to get vap %q: %v, retrying ...", vapName, err)
			return false, nil
		}
		// Check if new VAP got recreated.
		if newVAP != nil && newVAP.UID != vap.UID {
			return true, fmt.Errorf("vap %q got recreated", vapName)
		}
		t.Logf("vap %q still exists, retrying ...", vapName)
		return false, nil
	}); err != nil {
		return fmt.Errorf("failed to verify deletion of vap %q: %v", vapName, err)
	}

	t.Logf("deleted vap %q", vapName)
	return nil
}

// createHttpRoute checks if the HTTPRoute can be created.
// If it can't an error is returned.
func createHttpRoute(namespace, routeName, parentNamespace, hostname, backendRefname string, gateway *gatewayapiv1.Gateway) (*gatewayapiv1.HTTPRoute, error) {
	if gateway == nil {
		return nil, errors.New("unable to create httpRoute, no gateway available")
	}

	// Create the backend (service and pod) needed for the route to have resolvedRefs=true.
	// The http route, service, and pod are cleaned up when the namespace is automatically deleted.
	// buildEchoPod builds a pod that listens on port 8080.
	echoPod := buildEchoPod(backendRefname, namespace)
	if err := kclient.Create(context.TODO(), echoPod); err != nil {
		return nil, fmt.Errorf("failed to create pod %s/%s: %v", namespace, echoPod.Name, err)
	}
	// buildEchoService builds a service that targets port 8080.
	echoService := buildEchoService(echoPod.Name, namespace, echoPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), echoService); err != nil {
		return nil, fmt.Errorf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
	}

	httpRoute := buildHTTPRoute(routeName, namespace, gateway.Name, parentNamespace, hostname, backendRefname)
	if err := kclient.Create(context.TODO(), httpRoute); err != nil {
		if kerrors.IsAlreadyExists(err) {
			name := types.NamespacedName{Namespace: namespace, Name: routeName}
			if err = kclient.Get(context.TODO(), name, httpRoute); err == nil {
				return httpRoute, nil
			} else {
				return nil, fmt.Errorf("failed to access existing http route: %v", err.Error())
			}
		} else {
			return nil, fmt.Errorf("failed to create http route: %v", err.Error())
		}
	}
	return httpRoute, nil
}

// createGateway checks if the Gateway can be created.
// If it can, it is returned.  If it can't an error is returned.
func createGateway(gatewayClass *gatewayapiv1.GatewayClass, name, namespace, domain string) (*gatewayapiv1.Gateway, error) {
	gateway := buildGateway(name, namespace, gatewayClass.Name, allNamespaces, domain)
	if err := kclient.Create(context.TODO(), gateway); err != nil {
		if kerrors.IsAlreadyExists(err) {
			name := types.NamespacedName{Namespace: namespace, Name: name}
			if err = kclient.Get(context.TODO(), name, gateway); err != nil {
				return nil, fmt.Errorf("failed to get the existing gateway: %v", err.Error())
			}
		} else {
			return nil, fmt.Errorf("failed to create gateway: %v", err.Error())
		}
	}
	return gateway, nil
}

// createGatewayClass checks if the GatewayClass can be created.
// If it can, it is returned.  If it can't an error is returned.
func createGatewayClass(name, controllerName string) (*gatewayapiv1.GatewayClass, error) {
	gatewayClass := buildGatewayClass(name, controllerName)
	if err := kclient.Create(context.TODO(), gatewayClass); err != nil {
		if kerrors.IsAlreadyExists(err) {
			name := types.NamespacedName{Namespace: "", Name: name}
			if err = kclient.Get(context.TODO(), name, gatewayClass); err == nil {
				return gatewayClass, nil
			}
		} else {
			return nil, fmt.Errorf("failed to create gateway class: %v", err.Error())
		}
	}
	return gatewayClass, nil
}

// createCRD creates the CRD with the given name or retrieves it if already exists.
func createCRD(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := buildGWAPICRDFromName(name)
	if err := kclient.Create(context.Background(), crd); err != nil {
		if kerrors.IsAlreadyExists(err) {
			if err := kclient.Get(context.Background(), types.NamespacedName{Name: name}, crd); err != nil {
				return nil, fmt.Errorf("failed to get crd %q: %w", name, err)
			}
			return crd, nil
		}
		return nil, fmt.Errorf("failed to create crd %q: %w", name, err)
	}
	return crd, nil
}

// buildGatewayClass initializes the GatewayClass and returns its address.
func buildGatewayClass(name, controllerName string) *gatewayapiv1.GatewayClass {
	return &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gatewayapiv1.GatewayClassSpec{
			ControllerName: gatewayapiv1.GatewayController(controllerName),
		},
	}
}

// buildGateway initializes the Gateway and returns its address.
func buildGateway(name, namespace, gcname, fromNs, domain string) *gatewayapiv1.Gateway {
	hostname := gatewayapiv1.Hostname("*." + domain)
	fromNamespace := gatewayapiv1.FromNamespaces(fromNs)
	// Tell the gateway listener to allow routes from the namespace/s in the fromNamespaces variable, which could be "All".
	allowedRoutes := gatewayapiv1.AllowedRoutes{Namespaces: &gatewayapiv1.RouteNamespaces{From: &fromNamespace}}
	listener1 := gatewayapiv1.Listener{Name: "http", Hostname: &hostname, Port: 80, Protocol: "HTTP", AllowedRoutes: &allowedRoutes}

	return &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(gcname),
			Listeners:        []gatewayapiv1.Listener{listener1},
		},
	}
}

// buildHTTPRoute initializes the HTTPRoute and returns its address.
func buildHTTPRoute(routeName, namespace, parentgateway, parentNamespace, hostname, backendRefname string) *gatewayapiv1.HTTPRoute {
	parentns := gatewayapiv1.Namespace(parentNamespace)
	parent := gatewayapiv1.ParentReference{Name: gatewayapiv1.ObjectName(parentgateway), Namespace: &parentns}
	port := gatewayapiv1.PortNumber(defaultPortNumber)
	rule := gatewayapiv1.HTTPRouteRule{
		BackendRefs: []gatewayapiv1.HTTPBackendRef{{
			BackendRef: gatewayapiv1.BackendRef{
				BackendObjectReference: gatewayapiv1.BackendObjectReference{
					Name: gatewayapiv1.ObjectName(backendRefname),
					Port: &port,
				},
			},
		}},
	}

	return &gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName, Namespace: namespace},
		Spec: gatewayapiv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayapiv1.CommonRouteSpec{ParentRefs: []gatewayapiv1.ParentReference{parent}},
			Hostnames:       []gatewayapiv1.Hostname{gatewayapiv1.Hostname(hostname)},
			Rules:           []gatewayapiv1.HTTPRouteRule{rule},
		},
	}
}

// buildGWAPICRDFromName initializes the GatewayAPI CRD deducing most of its required fields from the given name.
func buildGWAPICRDFromName(name string) *apiextensionsv1.CustomResourceDefinition {
	var (
		plural   = strings.Split(name, ".")[0]
		group, _ = strings.CutPrefix(name, plural+".")
		scope    = apiextensionsv1.NamespaceScoped
		// removing trailing "s"
		singular = plural[0 : len(plural)-1]
		versions = []map[string]bool{{"v1": true /*storage version*/}, {"v1beta1": false}}
		kind     string
	)

	switch plural {
	case "gatewayclasses":
		singular = "gatewayclass"
		kind = "GatewayClass"
		scope = apiextensionsv1.ClusterScoped
	case "gateways":
		kind = "Gateway"
	case "httproutes":
		kind = "HTTPRoute"
	case "referencegrants":
		kind = "ReferenceGrant"
		versions = []map[string]bool{{"v1beta1": true}}
	case "listenersets":
		kind = "ListenerSet"
		versions = []map[string]bool{{"v1alpha1": true}}
	case "grpcroutes":
		kind = "GRPCRoute"
		versions = []map[string]bool{{"v1": true}}
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: plural + "." + group,
			Annotations: map[string]string{
				"api-approved.kubernetes.io": "https://github.com/kubernetes-sigs/gateway-api/pull/2466",
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: singular,
				Plural:   plural,
				Kind:     kind,
			},
			Scope: scope,
		},
	}

	for _, v := range versions {
		for name, storage := range v {
			crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{
				Name:    name,
				Storage: storage,
				Served:  true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
					},
				},
			})
		}
	}

	return crd
}

// assertSubscription checks if the Subscription of the given name exists and returns an error if not.
func assertSubscription(t *testing.T, namespace, subName string) error {
	t.Helper()
	subscription := &operatorsv1alpha1.Subscription{}
	nsName := types.NamespacedName{Namespace: namespace, Name: subName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, subscription); err != nil {
			t.Logf("failed to get subscription %s, retrying...", subName)
			return false, nil
		}
		t.Logf("found subscription %s at installed version %s", subscription.Name, subscription.Status.InstalledCSV)
		return true, nil
	})
	return err
}

// deleteExistingSubscription deletes if the subscription of the given name exists and returns an error if not.
func deleteExistingSubscription(t *testing.T, namespace, subName string) error {
	t.Helper()
	existingSubscription := &operatorsv1alpha1.Subscription{}
	newSubscription := &operatorsv1alpha1.Subscription{}
	nsName := types.NamespacedName{Namespace: namespace, Name: subName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, existingSubscription); err != nil {
			t.Logf("failed to get Subscription %s: %v", nsName.Name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("timed out getting subscription %s: %w", nsName.Name, err)
	}
	// deleting Subscription.
	err = kclient.Delete(context.Background(), existingSubscription)
	if err != nil {
		return err
	}
	err = wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, nsName, newSubscription); err != nil {
			if kerrors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("failed to get deleted subscription %s: %v", nsName.Name, err)
			return false, nil
		}
		// if new Subscription got recreated while the poll ensures the Subscription is deleted.
		if newSubscription != nil && newSubscription.UID != existingSubscription.UID {
			return true, nil
		}
		t.Logf("Subscription %s still exists", nsName.Name)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("timed out waiting for Subscription %s to be deleted: %v", nsName.Name, err)
	}
	t.Logf("deleted Subscription %s", nsName.Name)
	return nil

}

// assertOSSMOperator checks if the OSSM Istio operator gets successfully installed
// and returns an error if not.
func assertOSSMOperator(t *testing.T) error {
	t.Helper()
	dep := &appsv1.Deployment{}
	ns := types.NamespacedName{Namespace: openshiftOperatorsNamespace, Name: openshiftIstioOperatorDeploymentName}

	// Get the Istio operator deployment.
	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, ns, dep); err != nil {
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

// assertIstiodControlPlane checks if the OSSM Istiod control plane gets successfully installed
// and returns an error if not.
func assertIstiodControlPlane(t *testing.T) error {
	t.Helper()
	dep := &appsv1.Deployment{}
	ns := types.NamespacedName{Namespace: operatorcontroller.DefaultOperandNamespace, Name: openshiftIstiodDeploymentName}

	// Get the Istiod deployment.
	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, ns, dep); err != nil {
			t.Logf("failed to get deployment %v, retrying...", ns)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("error finding deployment %v: %v", ns, err)
	}

	// Get the Istiod pod.
	podlist, err := getPods(t, kclient, dep)
	if err != nil {
		return fmt.Errorf("error finding pod for deployment %v: %v", ns, err)
	}
	if len(podlist.Items) > 1 {
		return fmt.Errorf("too many pods for deployment %v: %d", ns, len(podlist.Items))
	}
	pod := podlist.Items[0]
	if pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("Istiod failure: pod %s is not running, it is %v", pod.Name, pod.Status.Phase)
	}

	t.Logf("found istiod pod %s/%s to be %s", pod.Namespace, pod.Name, pod.Status.Phase)
	return nil
}

// assertGatewayClassSuccessful checks if the gateway class was created and accepted successfully
// and returns an error if not.
func assertGatewayClassSuccessful(t *testing.T, name string) (*gatewayapiv1.GatewayClass, error) {
	t.Helper()

	gwc := &gatewayapiv1.GatewayClass{}
	nsName := types.NamespacedName{Namespace: "", Name: name}
	recordedConditionMsg := "not found"

	// Wait up to 2 minutes for the gateway class to be Accepted.
	err := wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 2*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, gwc); err != nil {
			t.Logf("failed to get gateway class %s, retrying...", name)
			return false, nil
		}
		for _, condition := range gwc.Status.Conditions {
			if condition.Type == string(gatewayapiv1.GatewayClassConditionStatusAccepted) {
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
		return nil, fmt.Errorf("gateway class %s not %v, last recorded status message: %s", name, gatewayapiv1.GatewayClassConditionStatusAccepted, recordedConditionMsg)
	}

	t.Logf("gateway class %s successful", name)
	return gwc, nil
}

// assertGatewaySuccessful checks if the gateway was created and accepted successfully
// and returns an error if not.
func assertGatewaySuccessful(t *testing.T, namespace, name string) (*gatewayapiv1.Gateway, error) {
	t.Helper()

	gw := &gatewayapiv1.Gateway{}
	nsName := types.NamespacedName{Namespace: namespace, Name: name}
	recordedConditionMsg := "not found"

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, gw); err != nil {
			t.Logf("failed to get gateway %s, retrying...", name)
			return false, nil
		}
		for _, condition := range gw.Status.Conditions {
			if condition.Type == string(gatewayapiv1.GatewayClassConditionStatusAccepted) { // TODO: Use GatewayConditionAccepted when updating to v1.
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
		return nil, fmt.Errorf("gateway %s not %v, last recorded status message: %s", name, gatewayapiv1.GatewayClassConditionStatusAccepted, recordedConditionMsg)
	}

	return gw, nil
}

// assertHttpRouteSuccessful checks if the http route was created and has parent conditions that indicate
// it was accepted successfully.  A parent is usually a gateway.  Returns an error not accepted and/or not resolved.
func assertHttpRouteSuccessful(t *testing.T, namespace, name string, gateway *gatewayapiv1.Gateway) (*gatewayapiv1.HTTPRoute, error) {
	t.Helper()

	if gateway == nil {
		return nil, errors.New("unable to validate httpRoute, no gateway available")
	}
	httproute := &gatewayapiv1.HTTPRoute{}
	nsName := types.NamespacedName{Namespace: namespace, Name: name}

	// Wait 1 minute for parent/s to update
	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, httproute); err != nil {
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
		return nil, err
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
			case string(gatewayapiv1.RouteConditionAccepted):
				acceptedConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					accepted = true
				}
			case string(gatewayapiv1.RouteConditionResolvedRefs):
				resolvedRefConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					resolvedRefs = true
				}
			}
		}
		// Check the results for each parent.
		switch {
		case !accepted && !resolvedRefs:
			return nil, fmt.Errorf("httpRoute %s/%s, parent %v/%v neither %v nor %v, last recorded status messages: %s, %s", namespace, name, parent.ParentRef.Namespace, parent.ParentRef.Name, gatewayapiv1.RouteConditionAccepted, gatewayapiv1.RouteConditionResolvedRefs, acceptedConditionMsg, resolvedRefConditionMsg)
		case !accepted:
			return nil, fmt.Errorf("httpRoute %s/%s, parent %v/%v not %v, last recorded status message: %s", namespace, name, parent.ParentRef.Namespace, parent.ParentRef.Name, gatewayapiv1.RouteConditionAccepted, acceptedConditionMsg)
		case !resolvedRefs:
			return nil, fmt.Errorf("httpRoute %s/%s, parent %v/%v not %v, last recorded status message: %s", namespace, name, parent.ParentRef.Namespace, parent.ParentRef.Name, gatewayapiv1.RouteConditionResolvedRefs, resolvedRefConditionMsg)
		}
	}
	t.Logf("httpRoute %s/%s successful", namespace, name)
	return httproute, nil
}

// assertHttpRouteConnection checks if the http route of the given name replies successfully,
// and returns an error if not
func assertHttpRouteConnection(t *testing.T, hostname string, gateway *gatewayapiv1.Gateway) error {
	t.Helper()
	domain := ""

	// Create the http client to check the header.
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// Get gateway listener hostname to use for dnsRecord.
	if len(gateway.Spec.Listeners) > 0 {
		if gateway.Spec.Listeners[0].Hostname != nil && len(string(*gateway.Spec.Listeners[0].Hostname)) > 0 {
			domain = string(*gateway.Spec.Listeners[0].Hostname)
			if !strings.HasSuffix(domain, ".") {
				domain = domain + "."
			}
		}
	}
	// Obtain the standard formatting of the dnsRecord.
	dnsRecordName := operatorcontroller.GatewayDNSRecordName(gateway, domain)

	// Make sure the DNSRecord is ready to use.
	if err := assertDNSRecord(t, dnsRecordName); err != nil {
		return err
	}

	// Wait and check that the dns name resolves first. Takes a long time, so
	// if the hostname is actually an IP address, skip this.
	if net.ParseIP(hostname) == nil {
		if err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, dnsResolutionTimeout, false, func(context context.Context) (bool, error) {
			_, err := net.LookupHost(hostname)
			if err != nil {
				t.Logf("%v waiting for HTTP route name %s to resolve (%v)", time.Now(), hostname, err)
				return false, nil
			}
			return true, nil
		}); err != nil {
			t.Fatalf("HTTP route name %s was unable to be resolved: %v", hostname, err)
		}
	}

	// Wait for http route to respond, and when it does, check for the status code.
	if err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 5*time.Minute, false, func(context context.Context) (bool, error) {
		statusCode, err := getHttpResponse(client, hostname)
		if err != nil {
			t.Logf("GET %s failed: %v, retrying...", hostname, err)
			return false, nil
		}
		if statusCode != http.StatusOK {
			t.Logf("GET %s failed: status %v, expected %v, retrying...", hostname, statusCode, http.StatusOK)
			return false, nil // retry on 503 as pod/service may not be ready
		}
		t.Logf("request to %s was successful", hostname)
		return true, nil

	}); err != nil {
		t.Fatalf("error contacting %s's endpoint: %v", hostname, err)
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
	nsName := types.NamespacedName{Namespace: namespace, Name: csName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, catalogSource); err != nil {
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

// assertSMCP checks if the ServiceMeshControlPlane exists in a ready state,
// and returns an error if not.
func assertSMCP(t *testing.T) error {
	t.Helper()
	smcp := &maistrav2.ServiceMeshControlPlane{}
	nsName := types.NamespacedName{Namespace: operatorcontroller.DefaultOperandNamespace, Name: openshiftSMCPName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 3*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, smcp); err != nil {
			t.Logf("failed to get ServiceMeshControlPlane %s/%s: %v, retrying...", nsName.Namespace, nsName.Name, err)
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

// deleteExistingSMCP deletes if the SMCP exists and returns an error if not.
func deleteExistingSMCP(t *testing.T) error {
	t.Helper()
	existingSMCP := &maistrav2.ServiceMeshControlPlane{}
	newSMCP := &maistrav2.ServiceMeshControlPlane{}
	nsName := types.NamespacedName{Namespace: operatorcontroller.DefaultOperandNamespace, Name: openshiftSMCPName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, existingSMCP); err != nil {
			t.Logf("failed to get smcp %s: %v", nsName.Name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("timed out getting smcp %s: %w", nsName.Name, err)
	}
	// deleting SMCP.
	err = kclient.Delete(context.Background(), existingSMCP)
	if err != nil {
		t.Errorf("failed to delete smcp %s: %v", nsName.Name, err)
		return err
	}
	err = wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, nsName, newSMCP); err != nil {
			if kerrors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("failed to get deleted SMCP %s: %v", nsName.Name, err)
			return false, nil
		}
		// if new SMCP got recreated while the poll ensures the SMCP is deleted.
		if newSMCP != nil && newSMCP.UID != existingSMCP.UID {
			return true, nil
		}
		t.Logf("smcp %s still exists", nsName.Name)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("timed out waiting for SMCP %s to be deleted: %v", nsName.Name, err)
	}
	t.Logf("deleted smcp %s", nsName.Name)
	return nil
}

// assertDNSRecord checks to make sure a DNSRecord exists in a ready state,
// and returns an error if not.
func assertDNSRecord(t *testing.T, recordName types.NamespacedName) error {
	t.Helper()
	dnsRecord := &v1.DNSRecord{}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, recordName, dnsRecord); err != nil {
			t.Logf("failed to get DNSRecord %s/%s: %v, retrying...", recordName.Namespace, recordName.Name, err)
			return false, nil
		}
		// Determine the current state of the DNSRecord.
		if len(dnsRecord.Status.Zones) > 0 {
			for _, zone := range dnsRecord.Status.Zones {
				for _, condition := range zone.Conditions {
					if condition.Type == v1.DNSRecordPublishedConditionType && condition.Status == string(metav1.ConditionTrue) {
						return true, nil
					}
				}
			}
		}
		t.Logf("found DNSRecord %s/%s but could not determine its readiness. Retrying...", recordName.Namespace, recordName.Name)
		return false, nil
	})
	return err
}

// assertVAP checks if the ValidatingAdmissionPolicy of the given name exists, and returns an error if not.
func assertVAP(t *testing.T, name string) error {
	t.Helper()
	vap := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	return wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, types.NamespacedName{Name: name}, vap); err != nil {
			t.Logf("failed to get vap %q: %v, retrying...", name, err)
			return false, nil
		}
		t.Logf("found vap %q", name)
		return true, nil
	})
}

// scaleDeployment scales the deployment with the given name to the specified number of replicas.
func scaleDeployment(t *testing.T, namespace, name string, replicas int32) error {
	t.Helper()

	nsName := types.NamespacedName{Namespace: namespace, Name: name}
	return wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		depl := &appsv1.Deployment{}
		if err := kclient.Get(context, nsName, depl); err != nil {
			t.Logf("failed to get deployment %q: %v, retrying...", nsName, err)
			return false, nil
		}
		if *depl.Spec.Replicas != replicas {
			depl.Spec.Replicas = &replicas
			if err := kclient.Update(context, depl); err != nil {
				t.Logf("failed to update deployment %q: %v, retrying...", nsName, err)
				return false, nil
			}
			t.Logf("scaled deployment %q to %d replica(s)", nsName, replicas)
		}
		if depl.Status.AvailableReplicas != replicas {
			t.Logf("deployment %q expected to have %d available replica(s) but got %d, retrying...", nsName, replicas, depl.Status.AvailableReplicas)
			return false, nil
		}
		t.Logf("deployment %q has %d available replica(s)", nsName, replicas)
		return true, nil
	})
}

// vapManager helps to disable the VAP resource which is managed by CVO.
type vapManager struct {
	t    *testing.T
	name string
}

// newVAPManager returns a new instance of VAPManager.
func newVAPManager(t *testing.T, vapName string) *vapManager {
	return &vapManager{
		t:    t,
		name: vapName,
	}
}

// disable scales down CVO and removes the VAP resource.
func (m *vapManager) disable() (error, func()) {
	if err := scaleDeployment(m.t, cvoNamespace, cvoDeploymentName, 0); err != nil {
		return fmt.Errorf("failed to scale down cvo: %w", err), func() { /*scale down didn't work, nothing to do*/ }
	}
	if err := deleteExistingVAP(m.t, m.name); err != nil {
		return fmt.Errorf("failed to delete vap %q: %w", m.name, err), func() {
			if err := scaleDeployment(m.t, cvoNamespace, cvoDeploymentName, 1); err != nil {
				m.t.Errorf("failed to scale up cvo: %v", err)
			}
		}
	}
	return nil, nil
}

// Enable scales up CVO and waits until the VAP is recreated.
func (m *vapManager) enable() {
	if err := scaleDeployment(m.t, cvoNamespace, cvoDeploymentName, 1); err != nil {
		m.t.Errorf("failed to scale up cvo: %v", err)
	} else if err := assertVAP(m.t, m.name); err != nil {
		m.t.Errorf("failed to find vap %q: %v", m.name, err)
	}
}

func bypassVAP(t *testing.T, fns ...func(t *testing.T)) {
	vm := newVAPManager(t, gwapiCRDVAPName)
	// Remove the ingress operator's Validating Admission Policy (VAP)
	// which prevents modifications of Gateway API CRDs
	// by anything other than the ingress operator.
	if err, recoverFn := vm.disable(); err != nil {
		defer recoverFn()
		t.Fatalf("failed to disable vap: %v", err)
	}
	// Put back the VAP to ensure that it does not prevent
	// the ingress operator from managing Gateway API CRDs.
	defer vm.enable()

	for _, fn := range fns {
		fn(t)
	}
}
