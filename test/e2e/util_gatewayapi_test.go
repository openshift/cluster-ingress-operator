//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	v1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	"github.com/google/go-cmp/cmp"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	allNamespaces     = "All"
	defaultPortNumber = 80

	// openshiftOperatorsNamespace holds the expected OSSM subscription and Istio operator pod.
	openshiftOperatorsNamespace = "openshift-operators"
	// openshiftIstioOperatorDeploymentName holds the expected Service Mesh
	// operator deployment name.
	openshiftIstioOperatorDeploymentName = "servicemesh-operator3"
	// openshiftIstiodDeploymentName holds the expected istiod deployment name
	openshiftIstiodDeploymentName = "istiod-openshift-gateway"
	// openshiftIstioName holds the expected Istio CR name.
	openshiftIstioName = "openshift-gateway"
	// cvoNamespace is the namespace of cluster version operator (CVO).
	cvoNamespace = "openshift-cluster-version"
	// cvoDeploymentName is the name of cluster version operator's deployment.
	cvoDeploymentName = "cluster-version-operator"
)

// assertCRDExists checks if the CRD of the given name exists and returns an
// error if not.  If the CRD does exist, this function returns a slice of
// strings indicating the served versions.
func assertCRDExists(t *testing.T, crdname string) ([]string, error) {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	name := types.NamespacedName{Namespace: "", Name: crdname}
	crdVersions := []string{}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, name, crd); err != nil {
			t.Logf("failed to get crd %s: %v", name, err)
			return false, nil
		}
		crdConditions := crd.Status.Conditions
		for _, version := range crd.Spec.Versions {
			if version.Served {
				crdVersions = append(crdVersions, version.Name)
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
	return crdVersions, err
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
func createHttpRoute(t *testing.T, namespace, routeName, parentNamespace, hostname, backendRefname string, gateway *gatewayapiv1.Gateway) (*gatewayapiv1.HTTPRoute, error) {
	if gateway == nil {
		return nil, errors.New("unable to create httpRoute, no gateway available")
	}

	// Create the backend (service and pod) needed for the route to have resolvedRefs=true.
	// The http route, service, and pod are cleaned up when the namespace is automatically deleted.
	// buildEchoReplicaSet builds a replicaset which creates a pod that listens on port 8080.
	echoRs := buildEchoReplicaSet(backendRefname, namespace)
	if err := createWithRetryOnError(t, context.TODO(), echoRs, 2*time.Minute); err != nil {
		return nil, fmt.Errorf("failed to create replicaset %s/%s: %v", namespace, echoRs.Name, err)
	}
	// buildEchoService builds a service that targets port 8080.
	echoService := buildEchoService(echoRs.Name, namespace, echoRs.Spec.Template.ObjectMeta.Labels)
	if err := createWithRetryOnError(t, context.TODO(), echoService, 2*time.Minute); err != nil {
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

func createGatewayWithListeners(t *testing.T, gatewayClass *gatewayapiv1.GatewayClass, name, namespace string, listeners []testListener) (*gatewayapiv1.Gateway, error) {
	t.Helper()
	var gatewayListeners []gatewayapiv1.Listener
	for _, spec := range listeners {
		var hostname *gatewayapiv1.Hostname = nil
		if spec.hostname != nil {
			h := gatewayapiv1.Hostname(*spec.hostname)
			hostname = &h
		}

		fromNamespace := gatewayapiv1.FromNamespaces(allNamespaces)
		allowedRoutes := gatewayapiv1.AllowedRoutes{
			Namespaces: &gatewayapiv1.RouteNamespaces{From: &fromNamespace},
		}

		l := gatewayapiv1.Listener{
			Name:          gatewayapiv1.SectionName(spec.name),
			Hostname:      hostname,
			Port:          80,
			Protocol:      "HTTP",
			AllowedRoutes: &allowedRoutes,
		}
		gatewayListeners = append(gatewayListeners, l)
	}

	gateway := &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(gatewayClass.Name),
			Listeners:        gatewayListeners,
		},
	}

	if err := createWithRetryOnError(t, context.TODO(), gateway, 2*time.Minute); err != nil {
		return nil, fmt.Errorf("failed to create gateway %s: %v", name, err.Error())
	}

	return gateway, nil
}

// createGatewayClass checks if the GatewayClass can be created.
// If it can, it is returned.  If it can't an error is returned.
func createGatewayClass(t *testing.T, name, controllerName string) (*gatewayapiv1.GatewayClass, error) {
	t.Helper()

	gatewayClass := buildGatewayClass(name, controllerName)
	nsName := types.NamespacedName{Namespace: "", Name: name}
	if err := wait.PollUntilContextTimeout(context.TODO(), 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		if err := kclient.Create(ctx, gatewayClass); err != nil {
			if kerrors.IsAlreadyExists(err) {
				if err := kclient.Get(ctx, nsName, gatewayClass); err != nil {
					t.Logf("gatewayclass %s already exists, but get failed: %v; retrying...", nsName.Name, err)
					return false, nil
				}
				return true, nil
			}
			t.Logf("error creating gatewayclass %s: %v; retrying...", nsName.Name, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, err
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

// assertOSSMOperator checks if the OSSM operator gets successfully installed
// and returns an error if not. It uses configurable parameters such as the expected OSSM version, polling interval, and timeout.
func assertOSSMOperator(t *testing.T) error {
	return assertOSSMOperatorWithConfig(t, "", 1*time.Second, 60*time.Second)
}

// assertOSSMOperatorWithConfig checks if the OSSM operator gets successfully installed
// and returns an error if not. It uses configurable parameters such as
// the expected OSSM version, polling interval, and timeout.
func assertOSSMOperatorWithConfig(t *testing.T, version string, interval, timeout time.Duration) error {
	t.Helper()
	dep := &appsv1.Deployment{}
	ns := types.NamespacedName{Namespace: openshiftOperatorsNamespace, Name: openshiftIstioOperatorDeploymentName}

	// Get the OSSM operator deployment.
	if err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, ns, dep); err != nil {
			t.Logf("failed to get deployment %v, retrying...", ns)
			return false, nil
		}
		if len(version) > 0 {
			if csv, found := dep.Labels["olm.owner"]; found {
				if csv == version {
					t.Logf("Found OSSM deployment %q with expected version %q", ns, version)
				} else {
					t.Logf("OSSM deployment %q expected to have version %q but got %q, retrying...", ns, version, csv)
					return false, nil
				}
			}
		}
		if dep.Status.AvailableReplicas < *dep.Spec.Replicas {
			t.Logf("OSSM deployment %q expected to have %d available replica(s) but got %d, retrying...", ns, *dep.Spec.Replicas, dep.Status.AvailableReplicas)
			return false, nil
		}
		t.Logf("found OSSM operator deployment %q with %d available replica(s)", ns, dep.Status.AvailableReplicas)
		return true, nil
	}); err != nil {
		return fmt.Errorf("error finding deployment %v: %v", ns, err)
	}
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
	if len(podlist.Items) < 1 {
		return fmt.Errorf("Insufficient amount of pods in deployment %v: %d", ns, len(podlist.Items))
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
			t.Logf("Failed to get gatewayclass %s: %v; retrying...", name, err)
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
		t.Logf("Found gatewayclass %s, but it is not yet accepted; retrying...", name)
		return false, nil
	})
	if err != nil {
		t.Logf("[%s] Last observed gatewayclass:\n%s", time.Now().Format(time.DateTime), util.ToYaml(gwc))
		return nil, fmt.Errorf("gatewayclass %s is not %v; last recorded status message: %s", name, gatewayapiv1.GatewayClassConditionStatusAccepted, recordedConditionMsg)
	}

	t.Logf("[%s] Observed that gatewayclass %s has been accepted: %+v", time.Now().Format(time.DateTime), name, gwc.Status)

	return gwc, nil
}

// assertGatewaySuccessful checks if the gateway was created and accepted successfully
// and returns an error if not.
func assertGatewaySuccessful(t *testing.T, namespace, name string) (*gatewayapiv1.Gateway, error) {
	t.Helper()

	gw := &gatewayapiv1.Gateway{}
	nsName := types.NamespacedName{Namespace: namespace, Name: name}
	recordedAcceptedConditionMsg, recordedProgrammedConditionMsg := "", ""

	// Wait for the gateway to be accepted and programmed.
	// Load balancer provisioning can take several minutes on some platforms.
	// Therefore, a timeout of 3 minutes is set to accommodate potential delays.
	err := wait.PollUntilContextTimeout(context.Background(), 3*time.Second, 3*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, gw); err != nil {
			t.Logf("Failed to get gateway %v: %v; retrying...", nsName, err)
			return false, nil
		}
		acceptedConditionFound, programmedConditionFound := false, false
		for _, condition := range gw.Status.Conditions {
			if condition.Type == string(gatewayapiv1.GatewayConditionAccepted) {
				recordedAcceptedConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					t.Logf("[%s] Found gateway %v as Accepted", time.Now().Format(time.DateTime), nsName)
					acceptedConditionFound = true
				}
			}
			// Ensuring the gateway configuration is ready.
			// `AddressNotAssigned` may happen if the gateway service
			// didn't get its load balancer target.
			if condition.Type == string(gatewayapiv1.GatewayConditionProgrammed) {
				recordedProgrammedConditionMsg = condition.Message
				if condition.Status == metav1.ConditionTrue {
					t.Logf("[%s] Found gateway %v as Programmed", time.Now().Format(time.DateTime), nsName)
					programmedConditionFound = true
				}
			}
		}
		if acceptedConditionFound && programmedConditionFound {
			return true, nil
		}
		t.Logf("[%s] Not all expected gateway conditions are found, checking gateway service...", time.Now().Format(time.DateTime))

		// The creation of the gateway service may be delayed.
		// Check the current status of the service to see where we are.
		svc := &corev1.Service{}
		svcNsName := types.NamespacedName{Namespace: namespace, Name: gw.Name + "-" + string(gw.Spec.GatewayClassName)}
		if err := kclient.Get(context, svcNsName, svc); err != nil {
			t.Logf("Failed to get gateway service %v: %v; retrying...", svcNsName, err)
			return false, nil
		}
		t.Logf("[%s] Found gateway service: %+v", time.Now().Format(time.DateTime), svc)
		return false, nil
	})
	if err != nil {
		t.Logf("[%s] Last observed gateway:\n%s", time.Now().Format(time.DateTime), util.ToYaml(gw))
		return nil, fmt.Errorf("gateway %v does not have all expected conditions, last recorded status messages status messages for Accepted: %q, Programmed: %q", nsName, recordedAcceptedConditionMsg, recordedProgrammedConditionMsg)
	}

	t.Logf("[%s] Observed that gateway %v has been accepted: %+v", time.Now().Format(time.DateTime), nsName, gw.Status)

	return gw, nil
}

func waitForGatewayListenerCondition(t *testing.T, gatewayName types.NamespacedName, listenerName string, conditions ...metav1.Condition) error {
	t.Helper()

	gateway := &gatewayapiv1.Gateway{}
	expected := gatewayListenerConditionMap(conditions...)
	current := map[string]string{}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, gatewayName, gateway); err != nil {
			t.Logf("failed to get gateway %s, retrying...", gatewayName.Name)
			return false, nil
		}

		listenerStatus := gatewayapiv1.ListenerStatus{}
		for _, ls := range gateway.Status.Listeners {
			if string(ls.Name) == listenerName {
				listenerStatus = ls
				current = gatewayListenerConditionMap(listenerStatus.Conditions...)
				return conditionsMatchExpected(expected, current), nil
			}
		}
		if &listenerStatus == nil {
			t.Logf("gateway %s's listener %s has not been updated with status, retrying...", gatewayName.Name, listenerName)
			return false, nil
		}
		return false, nil
	})

	if err != nil {
		return fmt.Errorf("Expected conditions: %v do not match\n Current conditions: %v", expected, current)
	}
	return err
}

func gatewayListenerConditionMap(conditions ...metav1.Condition) map[string]string {
	conds := map[string]string{}
	for _, cond := range conditions {
		conds[cond.Type] = string(cond.Status)
	}
	return conds
}

// expectedDnsRecord is used to represent a DNSRecord expectation.
type expectedDnsRecord struct {
	dnsName     string
	gatewayName string
}

type testGateway struct {
	gatewayName string
	namespace   string
	listeners   []testListener
}

type testListener struct {
	name     string
	hostname *string
}

// assertExpectedDNSRecords polls until the DNSRecords in the default operand namespace match the given expectations.
// The expectations parameter is a map where keys are expectations for DNSRecord and values indicate whether a DNSRecord should be present.
func assertExpectedDNSRecords(t *testing.T, expectations map[expectedDnsRecord]bool) error {
	t.Helper()

	var expectationsMet bool

	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 2*time.Minute, false, func(context context.Context) (bool, error) {
		haveExpectNotPresent := false
		// expectationsMet starts true and gets set to false when some expectation is not met.
		expectationsMet = true

		dnsRecords := &v1.DNSRecordList{}
		if err := kclient.List(context, dnsRecords, client.InNamespace(operatorcontroller.DefaultOperandNamespace)); err != nil {
			t.Logf("failed to list DNSRecords: %v, retrying...", err)
			return false, nil
		}

		// Iterate over all expectations.
		for exp, shouldBePresent := range expectations {
			if !shouldBePresent {
				haveExpectNotPresent = true
			}

			// Reset the found and foundReady flags for each expectation.
			found := false
			foundReady := false
			// Look for a DNSRecord that matches the expected gateway and DNS name.
			for _, record := range dnsRecords.Items {
				if record.Labels["gateway.networking.k8s.io/gateway-name"] == exp.gatewayName &&
					record.Spec.DNSName == exp.dnsName {

					if !shouldBePresent {
						expectationsMet = false
						found = true
						t.Logf("DNSRecord %q (%s) found but should not be present.", record.Name, exp.dnsName)
						return false, nil
					}
					found = true
					// DNSRecord found and should be present, check if it is published
					for _, zone := range record.Status.Zones {
						for _, condition := range zone.Conditions {
							if condition.Type == v1.DNSRecordPublishedConditionType && condition.Status == string(metav1.ConditionTrue) {
								t.Logf("Found DNSRecord %q (%s) %s=%s as expected", record.Name, exp.dnsName, condition.Type, condition.Status)
								foundReady = true
							}
						}
					}
					if !foundReady {
						t.Logf("Found DNSRecord %v but could not determine its readiness; retrying...", record.Name)
						expectationsMet = false
						return false, nil
					}
				}
			}

			// If the record is expected but not found, return false to continue polling.
			if shouldBePresent && !found {
				t.Logf("DNSRecord for hostname %q (gateway: %s) is expected to be present but was not found; retrying...", exp.dnsName, exp.gatewayName)
				expectationsMet = false
				return false, nil
			}
			// If the record is not expected but was found, return false to continue polling.
			if !shouldBePresent && found {
				t.Logf("DNSRecord for hostname %q (gateway: %s) is present but was expected to be absent; retrying...", exp.dnsName, exp.gatewayName)
				expectationsMet = false
				return false, nil
			}
		}
		if haveExpectNotPresent {
			t.Logf("Continuing polling to ensure non-expected DNSRecords do not exist...")
		}
		return !haveExpectNotPresent, nil
	})

	if !expectationsMet {
		return fmt.Errorf("failed to observe expected DNSRecords: %v", err)
	}
	return nil
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
			t.Logf("Failed to get httproute %v: %v; retrying...", nsName, err)
			return false, nil
		}
		numParents := len(httproute.Status.Parents)
		if numParents == 0 {
			t.Logf("Found no parents in httproute %v with status %+v; retrying...", nsName, httproute.Status)
			return false, nil
		}
		t.Logf("Found httproute %v with %d parent/s; status: %+v", nsName, numParents, httproute.Status)
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

	t.Logf("Observed that all parents of httproute %v report accepted and resolved; status: %+v", nsName, httproute.Status)

	return httproute, nil
}

// assertHttpRouteConnection checks if the http route of the given name replies successfully,
// and returns an error if not
func assertHttpRouteConnection(t *testing.T, hostname string, gateway *gatewayapiv1.Gateway) error {
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

	t.Logf("Making sure DNSRecord %v for domain %q is ready to use...", dnsRecordName, domain)
	if err := assertDNSRecord(t, dnsRecordName); err != nil {
		return err
	}

	// Wait and check that the dns name resolves first. Takes a long time, so
	// if the hostname is actually an IP address, skip this.
	if net.ParseIP(hostname) == nil {
		t.Logf("Attempting to resolve %s...", hostname)
		if err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, dnsResolutionTimeout, false, func(context context.Context) (bool, error) {
			_, err := net.LookupHost(hostname)
			if err != nil {
				t.Logf("%v waiting for HTTP route name %s to resolve (%v)", time.Now(), hostname, err)
				return false, nil
			}
			return true, nil
		}); err != nil {
			t.Fatalf("Failed to resolve host name %s: %v", hostname, err)
		}
	}

	var (
		statusCode int
		body       string
		headers    http.Header
	)
	t.Logf("Probing %s...", hostname)
	if err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 5*time.Minute, false, func(context context.Context) (bool, error) {
		var err error
		statusCode, headers, body, err = getHTTPResponse(client, hostname)
		if err != nil {
			t.Logf("GET %s failed: %v, retrying...", hostname, err)
			return false, nil
		}
		if statusCode != http.StatusOK {
			t.Logf("GET %s failed: status %v, expected %v, retrying...", hostname, statusCode, http.StatusOK)
			return false, nil // retry on 503 as pod/service may not be ready
		}
		t.Logf("Request to %s was successful", hostname)
		return true, nil

	}); err != nil {
		if statusCode != 0 {
			t.Log("Response headers for most recent request:", headers)
			t.Log("Reponse body for most recent request:", body)
		}
		t.Fatalf("Error connecting to %s: %v", hostname, err)
	}

	return nil
}

func getHTTPResponse(client *http.Client, name string) (int, http.Header, string, error) {
	// Send the HTTP request.
	response, err := client.Get("http://" + name)
	if err != nil {
		return 0, nil, "", fmt.Errorf("GET %s failed: %v", name, err)
	}

	// Close response body.
	defer response.Body.Close()
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return 0, nil, "", fmt.Errorf("failed to read response body: %w", err)
	}

	return response.StatusCode, response.Header, string(body), nil
}

// assertCatalogSource checks if the CatalogSource of the given name exists,
// and returns an error if not.
func assertCatalogSource(t *testing.T, namespace, csName string) error {
	t.Helper()
	catalogSource := &operatorsv1alpha1.CatalogSource{}
	nsName := types.NamespacedName{Namespace: namespace, Name: csName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, catalogSource); err != nil {
			t.Logf("Failed to get CatalogSource %s: %v.  Retrying...", csName, err)
			return false, nil
		}
		if catalogSource.Status.GRPCConnectionState != nil && catalogSource.Status.GRPCConnectionState.LastObservedState == "READY" {
			t.Logf("Found CatalogSource %s with last observed state: %s", catalogSource.Name, catalogSource.Status.GRPCConnectionState.LastObservedState)
			return true, nil
		}
		t.Logf("Found CatalogSource %s but could not determine last observed state.  Retrying...", catalogSource.Name)
		return false, nil
	})
	return err
}

// assertIstio checks if the Istio exists in a ready state,
// and returns an error if not.
func assertIstio(t *testing.T) error {
	return assertIstioWithConfig(t, "")
}

// assertIstio checks if the Istio exists in a ready state,
// and returns an error if not.It uses configurable parameters such as
// the expected version.
func assertIstioWithConfig(t *testing.T, version string) error {
	t.Helper()
	istio := &sailv1.Istio{}
	nsName := types.NamespacedName{Namespace: operatorcontroller.DefaultOperandNamespace, Name: openshiftIstioName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 3*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, istio); err != nil {
			t.Logf("Failed to get Istio %s/%s: %v.  Retrying...", nsName.Namespace, nsName.Name, err)
			return false, nil
		}
		if len(version) > 0 {
			if version == istio.Spec.Version {
				t.Logf("Found Istio %s/%s with expected version %q", istio.Namespace, istio.Name, version)
			} else {
				t.Logf("Istio %s/%s expected to have version %q but got %q, retrying...", istio.Namespace, istio.Name, version, istio.Spec.Version)
				return false, nil
			}
		}
		if istio.Status.GetCondition(sailv1.IstioConditionReady).Status == metav1.ConditionTrue {
			t.Logf("Found Istio %s/%s, and it reports ready", istio.Namespace, istio.Name)
			return true, nil
		}
		t.Logf("Found Istio %s/%s, but it isn't ready.  Retrying...", istio.Namespace, istio.Name)
		return false, nil
	})
	return err
}

// deleteExistingIstio deletes if the Istio exists and returns an error if not.
func deleteExistingIstio(t *testing.T) error {
	t.Helper()
	existingIstio := &sailv1.Istio{}
	newIstio := &sailv1.Istio{}
	nsName := types.NamespacedName{Namespace: operatorcontroller.DefaultOperandNamespace, Name: openshiftIstioName}

	err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, nsName, existingIstio); err != nil {
			t.Logf("Failed to get Istio %s: %v", nsName.Name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("timed out getting Istio %s: %w", nsName.Name, err)
	}
	t.Logf("Deleting Istio %s...", existingIstio.Name)
	err = kclient.Delete(context.Background(), existingIstio)
	if err != nil {
		t.Errorf("Failed to delete Istio %s: %v", nsName.Name, err)
		return err
	}
	err = wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, nsName, newIstio); err != nil {
			if kerrors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("Got unexpected error when trying to get Istio %s after deletion: %v", nsName.Name, err)
			return false, nil
		}
		// Compare the UID to determine whether it is the same object
		// or whether it is a new one with the same name.
		if newIstio.UID != existingIstio.UID {
			t.Logf("Istio %s has been recreated (old UID: %v, new UID: %v)", nsName.Name, existingIstio.UID, newIstio.UID)
			return true, nil
		}
		t.Logf("Istio %s still exists (UID: %v)", nsName.Name, existingIstio.UID)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("Timed out waiting for Istio %s to be deleted: %v", nsName.Name, err)
	}
	t.Logf("Deleted Istio %s", nsName.Name)
	return nil
}

// assertDNSRecord checks to make sure a DNSRecord exists in a ready state,
// and returns an error if not.
func assertDNSRecord(t *testing.T, recordName types.NamespacedName) error {
	t.Helper()
	dnsRecord := &v1.DNSRecord{}

	err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, 10*time.Minute, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, recordName, dnsRecord); err != nil {
			t.Logf("[%s] Failed to get DNSRecord %v: %v; retrying...", time.Now().Format(time.DateTime), recordName, err)
			return false, nil
		}
		// Determine the current state of the DNSRecord.
		reason := "missing published condition"
		if len(dnsRecord.Status.Zones) > 0 {
			for _, zone := range dnsRecord.Status.Zones {
				for _, condition := range zone.Conditions {
					if condition.Type == v1.DNSRecordPublishedConditionType {
						reason = "unexpected published condition value"
						if condition.Status == string(metav1.ConditionTrue) {
							t.Logf("[%s] Found DNSRecord %v %s=%s", time.Now().Format(time.DateTime), recordName, condition.Type, condition.Status)
							return true, nil
						}
					}
				}
			}
		} else {
			reason = "missing zones"
		}
		t.Logf("[%s] Found DNSRecord %v but could not determine its readiness due to %s; retrying...", time.Now().Format(time.DateTime), recordName, reason)
		return false, nil
	})
	return err
}

// assertVAP checks if the ValidatingAdmissionPolicy of the given name exists, and returns an error if not.
func assertVAP(t *testing.T, name string) error {
	t.Helper()
	vap := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	// Re-creation of VAP can take some time especially after CVO is scaled back up.
	// Use a large timeout of 5 minutes to avoid flakes.
	return wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 5*time.Minute, false, func(context context.Context) (bool, error) {
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
	return wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 120*time.Second, false, func(context context.Context) (bool, error) {
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

// bypassVAP temporarily removes the ingress operator's VAP, executes the given functions,
// and then restores the VAP before returning.
// The provided functions are expected to modify Gateway API CRDs, which are
// normally protected from external modifications (any changes made by anything
// other than the ingress operator) by the VAP.
func bypassVAP(t *testing.T, fns ...func(t *testing.T)) {
	vm := newVAPManager(t, gwapiCRDVAPName)
	if err, recoverFn := vm.disable(); err != nil {
		defer recoverFn()
		t.Fatalf("failed to disable vap: %v", err)
	}
	defer vm.enable()

	for _, fn := range fns {
		fn(t)
	}
}

func eventuallyClusterRoleContainsAggregatedPolicies(t *testing.T, destClusterRoleName, srcClusterRoleName string) error {
	t.Helper()

	return wait.PollImmediate(time.Second, timeout, func() (bool, error) {
		var destClusterRole rbacv1.ClusterRole
		if err := kclient.Get(context.Background(), types.NamespacedName{Name: destClusterRoleName}, &destClusterRole); err != nil {
			t.Logf("Failed to get destination ClusterRole %s; retrying...: %v", destClusterRoleName, err)
			return false, nil
		}

		var srcClusterRole rbacv1.ClusterRole
		if err := kclient.Get(context.Background(), types.NamespacedName{Name: srcClusterRoleName}, &srcClusterRole); err != nil {
			t.Logf("Failed to get source ClusterRole %s: %v; retrying...", srcClusterRoleName, err)
			return false, nil
		}

		if len(destClusterRole.Rules) == 0 {
			return false, fmt.Errorf("ClusterRole %s unexpectedly had no PolicyRules set", destClusterRoleName)
		}

		if len(srcClusterRole.Rules) == 0 {
			return false, fmt.Errorf("ClusterRole %s unexpectedly had no PolicyRules set", srcClusterRoleName)
		}

		if containsPolicyRules(destClusterRole.Rules, srcClusterRole.Rules) {
			t.Logf("ClusterRole %s aggregated all rules from %s", destClusterRoleName, srcClusterRoleName)
			return true, nil
		}

		return false, nil
	})
}

func containsPolicyRules(destRules, srcRules []rbacv1.PolicyRule) bool {
	for _, srcRule := range srcRules {
		if !slices.ContainsFunc(destRules, func(destRule rbacv1.PolicyRule) bool {
			return cmp.Equal(destRule, srcRule)
		}) {
			return false
		}
	}

	return true
}

// updateGatewaySpecWithRetry gets a fresh copy
// of the named gateway object, calls mutateSpecFn() where
// callers can modify fields of the spec, and then updates the gateway
// object. If there is any error during get or update, it will log
// and retry
func updateGatewaySpecWithRetry(t *testing.T, name types.NamespacedName, timeout time.Duration, mutateSpecFn func(*gatewayapiv1.GatewaySpec)) error {
	gw := gatewayapiv1.Gateway{}
	return wait.PollUntilContextTimeout(context.TODO(), 1*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, name, &gw); err != nil {
			t.Logf("error getting gateway resource %v: %v, retrying...", name, err)
			return false, nil
		}
		mutateSpecFn(&gw.Spec)
		if err := kclient.Update(ctx, &gw); err != nil {
			t.Logf("error when updating gateway spec %v: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}
