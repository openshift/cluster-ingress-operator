// +build e2e

package e2e

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v2"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/apiserver/pkg/storage/names"

	discocache "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/scale"
)

var (
	defaultAvailableConditions = []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
	}
)

var kclient client.Client
var dnsConfig configv1.DNS
var infraConfig configv1.Infrastructure
var operatorNamespace = manifests.DefaultOperatorNamespace
var defaultName = types.NamespacedName{Namespace: operatorNamespace, Name: manifests.DefaultIngressControllerName}

func TestMain(m *testing.M) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		fmt.Printf("failed to get kube config: %s\n", err)
		os.Exit(1)
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		fmt.Printf("failed to create kube client: %s\n", err)
		os.Exit(1)
	}
	kclient = kubeClient

	if err := kclient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, &dnsConfig); err != nil {
		fmt.Printf("failed to get DNS config: %v\n", err)
		os.Exit(1)
	}
	if err := kclient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, &infraConfig); err != nil {
		fmt.Printf("failed to get infrastructure config: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestOperatorSteadyConditions(t *testing.T) {
	expected := []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue},
	}
	if err := waitForClusterOperatorConditions(kclient, expected...); err != nil {
		t.Errorf("did not get expected available condition: %v", err)
	}
}

func TestDefaultIngressControllerSteadyConditions(t *testing.T) {
	if err := waitForIngressControllerCondition(kclient, 10*time.Second, defaultName, defaultAvailableConditions...); err != nil {
		t.Errorf("did not get expected conditions: %v", err)
	}
}

func TestUserDefinedIngressController(t *testing.T) {
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "test"}
	ing := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	if err := kclient.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ing)

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, defaultAvailableConditions...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

func TestUniqueDomainRejection(t *testing.T) {
	def := &operatorv1.IngressController{}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := kclient.Get(context.TODO(), defaultName, def); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}

	conflictName := types.NamespacedName{Namespace: operatorNamespace, Name: "conflict"}
	conflict := newLoadBalancerController(conflictName, def.Status.Domain)
	if err := kclient.Create(context.TODO(), conflict); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, conflict)

	conditions := []operatorv1.OperatorCondition{
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionFalse},
	}
	err := waitForIngressControllerCondition(kclient, 5*time.Minute, conflictName, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

// TODO: should this be a test of source IP preservation in the conformance suite?
func TestClusterProxyProtocol(t *testing.T) {
	if infraConfig.Status.Platform != configv1.AWSPlatformType {
		t.Skip("test skipped on non-aws platform")
		return
	}

	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get default ingresscontroller deployment: %v", err)
	}

	// Ensure proxy protocol is enabled on the deployment.
	proxyProtocolEnabled := false
	for _, v := range deployment.Spec.Template.Spec.Containers[0].Env {
		if v.Name == "ROUTER_USE_PROXY_PROTOCOL" {
			if val, err := strconv.ParseBool(v.Value); err == nil {
				proxyProtocolEnabled = val
				break
			}
		}
	}
	if !proxyProtocolEnabled {
		t.Fatalf("expected deployment to enable the PROXY protocol")
	}
}

// NOTE: This test will mutate the default ingresscontroller.
//
// TODO: Find a way to do this test without mutating the default ingress?
func TestUpdateDefaultIngressController(t *testing.T) {
	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	defaultIngressCAConfigmap := &corev1.ConfigMap{}
	if err := kclient.Get(context.TODO(), controller.DefaultIngressCertConfigMapName(), defaultIngressCAConfigmap); err != nil {
		t.Fatalf("failed to get CA certificate configmap: %v", err)
	}

	// Verify that the deployment uses the secret name specified in the
	// ingress controller, or the default if none is set, and store the
	// secret name (if any) so we can reset it at the end of the test.
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get default deployment: %v", err)
	}
	originalSecret := ic.Spec.DefaultCertificate.DeepCopy()
	expectedSecretName := controller.RouterOperatorGeneratedDefaultCertificateSecretName(ic, deployment.Namespace).Name
	if originalSecret != nil {
		expectedSecretName = originalSecret.Name
	}
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != expectedSecretName {
		t.Fatalf("expected router deployment certificate secret to be %s, got %s",
			expectedSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	// Update the ingress controller and wait for the deployment to match.
	secret, err := createDefaultCertTestSecret(kclient, names.SimpleNameGenerator.GenerateName("test-"))
	if err != nil {
		t.Fatalf("creating default cert test secret: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), secret); err != nil {
			t.Errorf("failed to delete test secret: %v", err)
		}
	}()

	ic.Spec.DefaultCertificate = &corev1.LocalObjectReference{Name: secret.Name}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update default ingresscontroller: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, deployment); err != nil {
			return false, nil
		}
		if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != secret.Name {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe updated deployment: %v", err)
	}

	// Wait for the default ingress configmap to be updated
	previousDefaultIngressCAConfigmap := defaultIngressCAConfigmap.DeepCopy()
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.DefaultIngressCertConfigMapName(), defaultIngressCAConfigmap); err != nil {
			return false, err
		}
		if defaultIngressCAConfigmap.Data["ca-bundle.crt"] == previousDefaultIngressCAConfigmap.Data["ca-bundle.crt"] {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe update of default ingress CA certificate configmap: %v", err)
	}

	// Reset .spec.defaultCertificate to its original value.
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}
	ic.Spec.DefaultCertificate = originalSecret
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Errorf("failed to reset default ingresscontroller: %v", err)
	}

	// Wait for the default ingress configmap to be updated back to the original
	previousDefaultIngressCAConfigmap = defaultIngressCAConfigmap.DeepCopy()
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.DefaultIngressCertConfigMapName(), defaultIngressCAConfigmap); err != nil {
			return false, err
		}
		if defaultIngressCAConfigmap.Data["ca-bundle.crt"] == previousDefaultIngressCAConfigmap.Data["ca-bundle.crt"] {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Logf("secret content=%v", string(secret.Data["tls.crt"]))
		t.Fatalf("failed to observe update of default ingress CA certificate configmap: %v\noriginal=%v\ncurrent=%v", err, previousDefaultIngressCAConfigmap.Data["ca-bundle.crt"], defaultIngressCAConfigmap.Data["ca-bundle.crt"])
	}
}

// TestIngressControllerScale exercises a simple scale up/down scenario.
func TestIngressControllerScale(t *testing.T) {
	// Get a scale client.
	//
	// TODO: Use controller-runtime once it supports the /scale subresource.
	scaleClient, err := getScaleClient()
	if err != nil {
		t.Fatal(err)
	}

	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get default ingresscontroller deployment: %v", err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		t.Fatalf("router deployment has invalid spec.selector: %v", err)
	}

	oldRsList := &appsv1.ReplicaSetList{}
	if err := kclient.List(context.TODO(), oldRsList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		t.Fatalf("failed to list replicasets for ingresscontroller: %v", err)
	}

	resource := schema.GroupResource{
		Group:    "operator.openshift.io",
		Resource: "ingresscontrollers",
	}

	scale, err := scaleClient.Scales(defaultName.Namespace).Get(context.TODO(), resource, defaultName.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get initial scale of default ingresscontroller: %v", err)
	}

	// Make sure the deployment's selector is reflected in the scale status.
	if scale.Status.Selector != selector.String() {
		t.Fatalf("expected scale status.selector to be %q, got %q", selector.String(), scale.Status.Selector)
	}

	originalReplicas := scale.Spec.Replicas
	newReplicas := originalReplicas + 1

	scale.Spec.Replicas = newReplicas
	updatedScale, err := scaleClient.Scales(defaultName.Namespace).Update(context.TODO(), resource, scale, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to scale ingresscontroller up: %v", err)
	}
	if updatedScale.Spec.Replicas != scale.Spec.Replicas {
		t.Fatalf("expected scaled-up ingresscontroller's spec.replicas to be %d, got %d", scale.Spec.Replicas, updatedScale.Spec.Replicas)
	}

	// Wait for the deployment scale up to be observed.
	if err := waitForAvailableReplicas(kclient, ic, 2*time.Minute, newReplicas); err != nil {
		t.Fatalf("failed waiting deployment %s to scale to %d: %v", defaultName, newReplicas, err)
	}

	// Ensure the ingresscontroller remains available
	if err := waitForIngressControllerCondition(kclient, 2*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Scale back down.
	scale, err = scaleClient.Scales(defaultName.Namespace).Get(context.TODO(), resource, defaultName.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get updated scale of ClusterIngress: %v", err)
	}
	scale.Spec.Replicas = originalReplicas
	updatedScale, err = scaleClient.Scales(defaultName.Namespace).Update(context.TODO(), resource, scale, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to scale ingresscontroller down: %v", err)
	}
	if updatedScale.Spec.Replicas != scale.Spec.Replicas {
		t.Fatalf("expected scaled-down ingresscontroller's spec.replicas to be %d, got %d", scale.Spec.Replicas, updatedScale.Spec.Replicas)
	}

	// Wait for the deployment scale down to be observed.
	if err := waitForAvailableReplicas(kclient, ic, 2*time.Minute, originalReplicas); err != nil {
		t.Fatalf("failed waiting deployment %s to scale to %d: %v", defaultName, originalReplicas, err)
	}

	// Ensure the ingresscontroller remains available
	// TODO: assert that the conditions hold steady for some amount of time?
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure the deployment did not create a new replicaset
	// (see <https://bugzilla.redhat.com/show_bug.cgi?id=1783007>).
	newRsList := &appsv1.ReplicaSetList{}
	if err := kclient.List(context.TODO(), newRsList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		t.Fatalf("failed to list replicasets for ingresscontroller: %v", err)
	}
	oldRsIds := sets.String{}
	for _, rs := range oldRsList.Items {
		oldRsIds.Insert(string(rs.UID))
	}
	newRsIds := sets.String{}
	for _, rs := range newRsList.Items {
		newRsIds.Insert(string(rs.UID))
	}
	if !oldRsIds.IsSuperset(newRsIds) {
		t.Fatalf("scaling the deployment created a new replicaset\nold replicaset list:\n%#v\nnew replicaset list:\n%#v)", oldRsList.Items, newRsList.Items)
	}
}

func getScaleClient() (scale.ScalesGetter, error) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kube config: %v", err)
	}

	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %v", err)
	}

	cachedDiscovery := discocache.NewMemCacheClient(client.Discovery())
	cachedDiscovery.Invalidate()
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscovery)
	restMapper.Reset()
	scaleKindResolver := scale.NewDiscoveryScaleKindResolver(client.Discovery())

	return scale.NewForConfig(kubeConfig, restMapper, dynamic.LegacyAPIPathResolverFunc, scaleKindResolver)
}

// TestDefaultIngressCertificate verifies that the "default-ingress-cert"
// configmap is published and can be used to connect to the router.
func TestDefaultIngressCertificate(t *testing.T) {
	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}

	if ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		t.Skip("test only applicable to load balancer controllers")
		return
	}

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	defaultIngressCAConfigmap := &corev1.ConfigMap{}
	if err := kclient.Get(context.TODO(), controller.DefaultIngressCertConfigMapName(), defaultIngressCAConfigmap); err != nil {
		t.Fatalf("failed to get CA certificate configmap: %v", err)
	}

	var certData []byte
	if val, ok := defaultIngressCAConfigmap.Data["ca-bundle.crt"]; !ok {
		t.Fatalf("%s configmap is missing %q", controller.DefaultIngressCertConfigMapName(), "ca-bundle.crt")
	} else {
		certData = []byte(val)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(certData) {
		t.Fatalf("failed to parse CA certificate")
	}

	wildcardRecordName := controller.WildcardDNSRecordName(ic)
	wildcardRecord := &iov1.DNSRecord{}
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err != nil {
		t.Fatalf("failed to get wildcard dnsrecord %s: %v", wildcardRecordName, err)
	}
	// TODO: handle >0 targets
	host := wildcardRecord.Spec.Targets[0]

	// Make sure we can connect without getting a "certificate signed by
	// unknown authority" or "x509: certificate is valid for [...], not
	// [...]" error.
	serverName := "test." + ic.Status.Domain
	address := net.JoinHostPort(host, "443")
	conn, err := tls.Dial("tcp", address, &tls.Config{
		RootCAs:    certPool,
		ServerName: serverName,
	})
	if err != nil {
		t.Fatalf("failed to connect to router at %s: %v", address, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("failed to close connection: %v", err)
		}
	}()

	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// We do not care about the response as long as we can read it without
	// error.
	if _, err := io.Copy(ioutil.Discard, conn); err != nil && err != io.EOF {
		t.Fatalf("failed to read response from router at %s: %v", address, err)
	}
}

// TestPodDisruptionBudgetExists verifies that a PodDisruptionBudget resource
// exists for the default ingresscontroller.
func TestPodDisruptionBudgetExists(t *testing.T) {
	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	pdb := &policyv1beta1.PodDisruptionBudget{}
	if err := kclient.Get(context.TODO(), controller.RouterPodDisruptionBudgetName(ic), pdb); err != nil {
		t.Fatalf("failed to get default ingresscontroller poddisruptionbudget: %v", err)
	}
}

// TestHostNetworkEndpointPublishingStrategy creates an ingresscontroller with
// the "HostNetwork" endpoint publishing strategy type and verifies that the
// operator creates a router and that the router becomes available.
func TestHostNetworkEndpointPublishingStrategy(t *testing.T) {
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "host"}
	ing := newHostNetworkController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	if err := kclient.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ing)

	conditions := []operatorv1.OperatorCondition{
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

// TestInternalLoadBalancer creates an ingresscontroller with the
// "LoadBalancerService" endpoint publishing strategy type with scope set to
// "Internal" and verifies that the operator creates a load balancer and that
// the load balancer has a private IP address.
func TestInternalLoadBalancer(t *testing.T) {
	platform := infraConfig.Status.Platform

	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AWSPlatformType:      {},
		configv1.AzurePlatformType:    {},
		configv1.GCPPlatformType:      {},
		configv1.IBMCloudPlatformType: {},
	}
	if _, supported := supportedPlatforms[platform]; !supported {
		t.Skip(fmt.Sprintf("test skipped on platform %q", platform))
	}

	annotation := ingresscontroller.InternalLBAnnotations[platform]

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "test"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.InternalLoadBalancer,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	for name, expected := range annotation {
		if actual, ok := lbService.Annotations[name]; !ok {
			t.Fatalf("load balancer has no %q annotation: %v", name, lbService.Annotations)
		} else if actual != expected {
			t.Fatalf("expected %s=%s, found %s=%s", name, expected, name, actual)
		}
	}
}

// TestNodePortServiceEndpointPublishingStrategy creates an ingresscontroller
// with the "NodePortService" endpoint publishing strategy type and verifies
// that the operator creates a router and that the router becomes available.
func TestNodePortServiceEndpointPublishingStrategy(t *testing.T) {
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "nodeport"}
	ing := newNodePortController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	if err := kclient.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ing)

	conditions := []operatorv1.OperatorCondition{
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

// TestTLSSecurityProfile creates an ingresscontroller with no explicit TLS
// profile, then verifies that the operator sets the default "Intermediate" TLS
// profile, then updates the ingresscontroller to use a custom TLS profile, and
// then verifies that the operator reflects the custom profile in its status.
func TestTLSSecurityProfile(t *testing.T) {
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "test"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(name, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", name, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	if ic.Status.TLSProfile == nil {
		t.Fatalf("ingresscontroller status has no security profile")
	}
	intermediateProfileSpec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	if !reflect.DeepEqual(*ic.Status.TLSProfile, *intermediateProfileSpec) {
		expected, _ := yaml.Marshal(intermediateProfileSpec)
		actual, _ := yaml.Marshal(*ic.Status.TLSProfile)
		t.Fatalf("ingresscontroller status has unexpected security profile spec.\nexpected:\n%s\ngot:\n%s", expected, actual)
	}

	customProfileSpec := configv1.TLSProfileSpec{
		Ciphers:       []string{"ECDHE-ECDSA-AES256-GCM-SHA384"},
		MinTLSVersion: configv1.VersionTLS12,
	}
	ic.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: customProfileSpec,
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Errorf("failed to update ingresscontroller %s: %v", name, err)
	}
	err := wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
		}
		if !reflect.DeepEqual(*ic.Status.TLSProfile, customProfileSpec) {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		expected, _ := yaml.Marshal(customProfileSpec)
		actual, _ := yaml.Marshal(*ic.Status.TLSProfile)
		t.Fatalf("ingresscontroller status has unexpected security profile spec.\nexpected:\n%s\ngot:\n%s", expected, actual)
	}
}

func TestRouteAdmissionPolicy(t *testing.T) {
	// Set up an ingresscontroller which only selects routes created by this test
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "routeadmission"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.RouteAdmission = &operatorv1.RouteAdmissionPolicy{
		NamespaceOwnership: operatorv1.StrictNamespaceOwnershipCheck,
	}
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"routeadmissiontest": "",
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Set up a pair of namespaces in which to create routes
	ns1 := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "routeadmissionpolicytest1",
		},
	}
	if err := kclient.Create(context.TODO(), ns1); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), ns1); err != nil {
			t.Fatalf("failed to delete test namespace %v: %v", ns1.Name, err)
		}
	}()
	ns2 := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "routeadmissionpolicytest2",
		},
	}
	if err := kclient.Create(context.TODO(), ns2); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), ns2); err != nil {
			t.Fatalf("failed to delete test namespace %v: %v", ns2.Name, err)
		}
	}()

	// Create conflicting routes in the namespaces
	makeRoute := func(name types.NamespacedName, host, path string) *routev1.Route {
		return &routev1.Route{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: name.Namespace,
				Name:      name.Name,
				Labels: map[string]string{
					"routeadmissiontest": "",
				},
			},
			Spec: routev1.RouteSpec{
				Host: host,
				Path: path,
				To: routev1.RouteTargetReference{
					Kind: "Service",
					Name: "foo",
				},
			},
		}
	}
	route1Name := types.NamespacedName{Namespace: ns1.Name, Name: "route"}
	route1 := makeRoute(route1Name, "routeadmission.test.example.com", "/foo")

	route2Name := types.NamespacedName{Namespace: ns2.Name, Name: "route"}
	route2 := makeRoute(route2Name, "routeadmission.test.example.com", "/bar")

	admittedCondition := routev1.RouteIngressCondition{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}
	rejectedCondition := routev1.RouteIngressCondition{Type: routev1.RouteAdmitted, Status: corev1.ConditionFalse}

	// The first route should be admitted
	if err := kclient.Create(context.TODO(), route1); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	if err := waitForRouteIngressConditions(kclient, route1Name, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// The second route should be rejected because the policy is Strict
	if err := kclient.Create(context.TODO(), route2); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	if err := waitForRouteIngressConditions(kclient, route2Name, ic.Name, rejectedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Update the ingresscontroller to a different route admission policy
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.RouteAdmission.NamespaceOwnership = operatorv1.InterNamespaceAllowedOwnershipCheck
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// The updated ingresscontroller deployment may take a few minutes to
	// roll out, so make sure that it is updated, and then make sure that it
	// has finished rolling out before checking the route.
	deployment := &appsv1.Deployment{}
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
			return false, err
		}
		for _, v := range deployment.Spec.Template.Spec.Containers[0].Env {
			if v.Name == "ROUTER_DISABLE_NAMESPACE_OWNERSHIP_CHECK" {
				return strconv.ParseBool(v.Value)
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe ROUTER_DISABLE_NAMESPACE_OWNERSHIP_CHECK=true: %v", err)
	}
	if err := waitForDeploymentComplete(t, kclient, deployment, 3*time.Minute); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// The second route should eventually be admitted because of the new policy
	if err := waitForRouteIngressConditions(kclient, route2Name, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Test the ingress controller wildcard admission policy. An ingress controller with
	// a nil wildcard policy defaults to WildcardsDisallowed and the default wildcard
	// policy of a route is None. Therefore, the tests above cover defaulting behavior.
	// Create a route that explicitly sets the wildcard policy to None.
	route3Name := types.NamespacedName{Namespace: ns2.Name, Name: "route3"}
	route3 := makeRoute(route3Name, "route3.test.example.com", "/bar")
	route3.Spec.WildcardPolicy = routev1.WildcardPolicyNone

	// The route should be admitted because the default ingresscontroller wildcard
	// policy is WildcardsDisallowed.
	if err := kclient.Create(context.TODO(), route3); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	if err := waitForRouteIngressConditions(kclient, route3Name, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v, status: %v", err, route3)
	}

	// Create a route with a wildcard policy of Subdomain.
	route4Name := types.NamespacedName{Namespace: ns2.Name, Name: "route4"}
	route4 := makeRoute(route4Name, "route4.test.example.com", "/bar")
	route4.Spec.WildcardPolicy = routev1.WildcardPolicySubdomain

	// The route should not be admitted because the ingresscontroller wildcard policy
	// is WildcardsDisallowed by default.
	if err := kclient.Create(context.TODO(), route4); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	if err := waitForRouteIngressConditions(kclient, route4Name, ic.Name, rejectedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Update the ingresscontroller wildcard policy to WildcardsAllowed.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.RouteAdmission.WildcardPolicy = operatorv1.WildcardPolicyAllowed
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
			return false, err
		}
		for _, v := range deployment.Spec.Template.Spec.Containers[0].Env {
			if v.Name == "ROUTER_ALLOW_WILDCARD_ROUTES" {
				return strconv.ParseBool(v.Value)
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe ROUTER_ALLOW_WILDCARD_ROUTES=true: %v", err)
	}
	if err := waitForDeploymentComplete(t, kclient, deployment, 3*time.Minute); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Recreate the route since the failed route will not automatically get
	// readmitted and a route wildcard policy is immutable.
	if err := kclient.Delete(context.TODO(), route4); err != nil {
		t.Fatalf("failed to delete route: %v", err)
	}
	route4 = makeRoute(route4Name, "route4.test.example.com", "/bar")
	route4.Spec.WildcardPolicy = routev1.WildcardPolicySubdomain
	if err := kclient.Create(context.TODO(), route4); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}

	// The route should now be admitted.
	if err := waitForRouteIngressConditions(kclient, route4Name, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
}

func TestSyslogLogging(t *testing.T) {
	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get default ingresscontroller's deployment: %v", err)
	}
	var (
		image      string
		foundImage bool
	)
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "router" {
			image = container.Image
			foundImage = true
		}
	}
	if !foundImage {
		t.Fatal("failed to determine default ingresscontroller deployment's image")
	}

	// Set up rsyslog.
	syslogPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-ingress",
			Name:      "syslog",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "syslog",
					Image: image,
					Command: []string{
						"/sbin/rsyslogd", "-n",
						"-i", "/tmp/rsyslog.pid",
						"-f", "/etc/rsyslog/rsyslog.conf",
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "rsyslog-config",
							MountPath: "/etc/rsyslog",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "rsyslog-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "rsyslog-conf",
							},
						},
					},
				},
			},
		},
	}
	if err := kclient.Create(context.TODO(), syslogPod); err != nil {
		t.Fatalf("failed to create pod for rsyslog: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), syslogPod); err != nil {
			t.Fatalf("failed to delete pod %s: %v", syslogPod.Name, err)
		}
	}()
	syslogConfigmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rsyslog-conf",
			Namespace: "openshift-ingress",
		},
		Data: map[string]string{
			"rsyslog.conf": `$ModLoad imudp
$UDPServerRun 10514
$ModLoad omstdout.so
*.* :omstdout:
`,
		},
	}
	if err := kclient.Create(context.TODO(), syslogConfigmap); err != nil {
		t.Fatalf("failed to create configmap for rsyslog: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), syslogConfigmap); err != nil {
			t.Fatalf("failed to delete configmap %s: %v", syslogConfigmap.Name, err)
		}
	}()

	// Get the rsyslog endpoint.
	var syslogAddress string
	err := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: syslogPod.Namespace, Name: syslogPod.Name}, syslogPod); err != nil {
			return false, nil
		}
		syslogAddress = syslogPod.Status.PodIP
		if len(syslogAddress) == 0 {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe syslog pod IP address: %v", err)
	}

	// Create an ingresscontroller that logs to the endpoint.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "syslog"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic = newPrivateController(icName, domain)
	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type: operatorv1.SyslogLoggingDestinationType,
				Syslog: &operatorv1.SyslogLoggingDestinationParameters{
					Address: syslogAddress,
					Port:    uint32(10514),
				},
			},
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Scan the syslog logs to make sure some requests get logged;
	// the kubelet's health probes should get logged.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		readCloser, err := client.CoreV1().Pods(syslogPod.Namespace).GetLogs(syslogPod.Name, &corev1.PodLogOptions{
			Container: "syslog",
			Follow:    false,
		}).Stream(context.TODO())
		if err != nil {
			t.Errorf("failed to read logs from syslog: %v", err)
			return false, nil
		}
		defer func() {
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close logs reader: %v", err)
			}
		}()
		scanner := bufio.NewScanner(readCloser)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, " HTTP/1.1") {
				t.Logf("found log message for request: %s", line)
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe any request logged in syslog: %v", err)
	}
}

func TestContainerLogging(t *testing.T) {
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "containerlogging"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type:      operatorv1.ContainerLoggingDestinationType,
				Container: &operatorv1.ContainerLoggingDestinationParameters{},
			},
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
}

func TestIngressControllerCustomEndpoints(t *testing.T) {
	// TODO: Change from spec to status after https://bugzilla.redhat.com/show_bug.cgi?id=1850681
	//       is fixed.
	platform := infraConfig.Spec.PlatformSpec
	switch platform.Type {
	case configv1.AWSPlatformType:
		if len(platform.AWS.ServiceEndpoints) != 0 {
			t.Fatalf("custom endpoints detected for infrastructure %s", infraConfig.Name)
		}
		route53Endpoint := configv1.AWSServiceEndpoint{
			Name: "route53",
			URL:  "https://route53.amazonaws.com",
		}
		taggingEndpoint := configv1.AWSServiceEndpoint{
			Name: "tagging",
			URL:  "https://tagging.us-east-1.amazonaws.com",
		}
		endpoints := []configv1.AWSServiceEndpoint{route53Endpoint, taggingEndpoint}
		infraConfig.Spec.PlatformSpec.AWS.ServiceEndpoints = endpoints
		if err := kclient.Update(context.TODO(), &infraConfig); err != nil {
			t.Logf("failed to update infrastructure config: %v\n", err)
		}
		// Wait for infrastructure status to update with custom endpoints.
		err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
			if err := kclient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, &infraConfig); err != nil {
				t.Logf("failed to get infrastructure config: %v\n", err)
				return false, nil
			}
			// TODO: Change from spec to status after https://bugzilla.redhat.com/show_bug.cgi?id=1850681
			//       is fixed.
			if len(infraConfig.Spec.PlatformSpec.AWS.ServiceEndpoints) == 0 {
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			t.Fatalf("failed to observe status update for infrastructure config %s", infraConfig.Name)
		}
		defer func() {
			// Remove the custom endpoints from the infrastructure config.
			infraConfig.Spec.PlatformSpec.AWS.ServiceEndpoints = []configv1.AWSServiceEndpoint{}
			// Wait for infrastructure status to update.
			err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
				if err := kclient.Update(context.TODO(), &infraConfig); err != nil {
					t.Logf("failed to get infrastructure config: %v\n", err)
					return false, nil
				}
				// TODO: Change from spec to status after https://bugzilla.redhat.com/show_bug.cgi?id=1850681
				//       is fixed.
				if len(infraConfig.Spec.PlatformSpec.AWS.ServiceEndpoints) != 0 {
					return false, nil
				}
				return true, nil
			})
			if err != nil {
				t.Fatalf("failed to remove custom endpoints from infrastructure config %s", infraConfig.Name)
			}
		}()
	default:
		t.Skipf("skipping TestIngressControllerCustomEndpoints test due to platform type: %s", platform.Type)
	}
	// The default ingresscontroller should surface the expected status conditions.
	if err := waitForIngressControllerCondition(kclient, 30*time.Second, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("did not get expected ingress controller conditions: %v", err)
	}
	// Ensure an ingresscontroller can be created with custom endpoints.
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "test-custom-endpoints"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", ic.Name, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	// Ensure the ingress controller is reporting expected status conditions.
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, defaultAvailableConditions...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

func TestHTTPHeaderCapture(t *testing.T) {
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "headercapture"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newNodePortController(icName, domain)
	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type:      operatorv1.ContainerLoggingDestinationType,
				Container: &operatorv1.ContainerLoggingDestinationParameters{},
			},
			HTTPCaptureHeaders: operatorv1.IngressControllerCaptureHTTPHeaders{
				Request: []operatorv1.IngressControllerCaptureHTTPHeader{
					{Name: "X-Test-Header-1", MaxLength: 15},
					{Name: "X-Test-Header-2", MaxLength: 15},
				},
				Response: []operatorv1.IngressControllerCaptureHTTPHeader{
					{Name: "Content-type", MaxLength: 9},
				},
			},
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Get the deployment's pods.  We will use these to curl a route and to
	// scan access logs.
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		t.Fatalf("router deployment has invalid spec.selector: %v", err)
	}
	podList := &corev1.PodList{}
	if err := kclient.List(context.TODO(), podList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		t.Fatalf("failed to list pods for ingresscontroller: %v", err)
	}
	if len(podList.Items) < 1 {
		t.Fatalf("found no pods for ingresscontroller: %v", err)
	}

	// Make a request to the console route.
	routeName := types.NamespacedName{Namespace: "openshift-console", Name: "console"}
	route := &routev1.Route{}
	if err := kclient.Get(context.TODO(), routeName, route); err != nil {
		t.Fatalf("failed to get the console route: %v", err)
	}
	clientPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "headertest",
			Namespace: podList.Items[0].Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "curl",
					Image:   podList.Items[0].Spec.Containers[0].Image,
					Command: []string{"/bin/curl"},
					Args: []string{
						"-k",
						"-o", "/dev/null", "-s",
						"-H", "x-test-header-1:foo",
						"-H", "x-test-header-2:bar",
						"--resolve",
						route.Spec.Host + ":443:" + podList.Items[0].Status.PodIP,
						"https://" + route.Spec.Host,
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
		}
	}()

	// Scan the access logs to make sure the expected headers were captured
	// and logged.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		for _, pod := range podList.Items {
			readCloser, err := client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: "logs",
				Follow:    false,
			}).Stream(context.TODO())
			if err != nil {
				t.Errorf("failed to read logs from pod %s: %v", pod.Name, err)
				continue
			}
			scanner := bufio.NewScanner(readCloser)
			var found bool
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "{foo|bar} {text/html}") {
					t.Logf("found log message for request in pod %s logs: %s", pod.Name, line)
					found = true
					break
				}
			}
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close logs reader for pod %s: %v", pod.Name, err)
			}
			return found, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe the expected log message: %v", err)
	}
}

func TestHTTPCookieCapture(t *testing.T) {
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "cookiecapture"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newNodePortController(icName, domain)
	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type:      operatorv1.ContainerLoggingDestinationType,
				Container: &operatorv1.ContainerLoggingDestinationParameters{},
			},
			HTTPCaptureCookies: []operatorv1.IngressControllerCaptureHTTPCookie{
				{MatchType: "Exact", Name: "foo", MaxLength: 9},
			},
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Get the deployment's pods.  We will use these to curl a route and to
	// scan access logs.
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		t.Fatalf("router deployment has invalid spec.selector: %v", err)
	}
	podList := &corev1.PodList{}
	if err := kclient.List(context.TODO(), podList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		t.Fatalf("failed to list pods for ingresscontroller: %v", err)
	}
	if len(podList.Items) < 1 {
		t.Fatalf("found no pods for ingresscontroller: %v", err)
	}

	// Make a request to the console route.
	routeName := types.NamespacedName{Namespace: "openshift-console", Name: "console"}
	route := &routev1.Route{}
	if err := kclient.Get(context.TODO(), routeName, route); err != nil {
		t.Fatalf("failed to get the console route: %v", err)
	}
	clientPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cookietest",
			Namespace: podList.Items[0].Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "curl",
					Image:   podList.Items[0].Spec.Containers[0].Image,
					Command: []string{"/bin/curl"},
					Args: []string{
						"-k",
						"-o", "/dev/null", "-s",
						"-H", "cookie:foobar=123",
						"-H", "cookie:foo=xyzzypop",
						"-H", "cookie:foobaz=abc",
						"--resolve",
						route.Spec.Host + ":443:" + podList.Items[0].Status.PodIP,
						"https://" + route.Spec.Host,
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
		}
	}()

	// Scan the access logs to make sure the expected cookie was captured
	// and logged.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		for _, pod := range podList.Items {
			readCloser, err := client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: "logs",
				Follow:    false,
			}).Stream(context.TODO())
			if err != nil {
				t.Errorf("failed to read logs from pod %s: %v", pod.Name, err)
				continue
			}
			scanner := bufio.NewScanner(readCloser)
			var found bool
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, " foo=xyzzy ") {
					t.Logf("found log message for request in pod %s logs: %s", pod.Name, line)
					found = true
					break
				}
			}
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close logs reader for pod %s: %v", pod.Name, err)
			}
			return found, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe the expected log message: %v", err)
	}
}

// TestNetworkLoadBalancer creates an ingresscontroller with the
// "LoadBalancerService" endpoint publishing strategy type with
// an AWS Network Load Balancer (NLB).
func TestNetworkLoadBalancer(t *testing.T) {
	platform := infraConfig.Status.PlatformStatus.Type

	if platform != configv1.AWSPlatformType {
		t.Skip(fmt.Sprintf("test skipped on platform %q", platform))
	}

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "test-nlb"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	lb := &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.ExternalLoadBalancer,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Type: operatorv1.AWSNetworkLoadBalancer,
			},
		},
	}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = lb
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, name, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	if actual, ok := lbService.Annotations[ingresscontroller.AWSLBTypeAnnotation]; !ok {
		t.Fatalf("load balancer has no %q annotation: %v", ingresscontroller.AWSLBTypeAnnotation, lbService.Annotations)
	} else if actual != ingresscontroller.AWSNLBAnnotation {
		t.Fatalf("expected %s=%s, found %s=%s", ingresscontroller.AWSLBTypeAnnotation, ingresscontroller.AWSNLBAnnotation,
			ingresscontroller.AWSLBTypeAnnotation, actual)
	}
}

func newLoadBalancerController(name types.NamespacedName, domain string) *operatorv1.IngressController {
	repl := int32(1)
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
		},
		Spec: operatorv1.IngressControllerSpec{
			Domain:   domain,
			Replicas: &repl,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
			},
		},
	}
}

func newNodePortController(name types.NamespacedName, domain string) *operatorv1.IngressController {
	repl := int32(1)
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
		},
		Spec: operatorv1.IngressControllerSpec{
			Domain:   domain,
			Replicas: &repl,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.NodePortServiceStrategyType,
			},
		},
	}
}

func newHostNetworkController(name types.NamespacedName, domain string) *operatorv1.IngressController {
	repl := int32(1)
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
		},
		Spec: operatorv1.IngressControllerSpec{
			Domain:   domain,
			Replicas: &repl,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.HostNetworkStrategyType,
			},
		},
	}
}

func newPrivateController(name types.NamespacedName, domain string) *operatorv1.IngressController {
	repl := int32(1)
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
		},
		Spec: operatorv1.IngressControllerSpec{
			Domain:   domain,
			Replicas: &repl,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			},
		},
	}
}

func waitForAvailableReplicas(cl client.Client, ic *operatorv1.IngressController, timeout time.Duration, expectedReplicas int32) error {
	ic = ic.DeepCopy()
	var lastObservedReplicas int32
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}, ic); err != nil {
			return false, nil
		}
		lastObservedReplicas = ic.Status.AvailableReplicas
		if lastObservedReplicas != expectedReplicas {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to achieve expected replicas, last observed: %v", lastObservedReplicas)
	}
	return nil
}

// Wait for the provided deployment to complete its rollout.
func waitForDeploymentComplete(t *testing.T, cl client.Client, deployment *appsv1.Deployment, timeout time.Duration) error {
	t.Helper()
	replicas := int32(1)
	name := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
	deployment = &appsv1.Deployment{}
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, deployment); err != nil {
			t.Logf("error getting deployment %s: %v", name, err)
			return false, nil
		}
		if deployment.Generation != deployment.Status.ObservedGeneration {
			return false, nil
		}
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		if replicas != deployment.Status.Replicas {
			return false, nil
		}
		if replicas != deployment.Status.AvailableReplicas {
			return false, nil
		}
		if replicas != deployment.Status.UpdatedReplicas {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to observe deployment rollout complete; deployment specifies %v replicas and has generation %v; last observed %v updated, %v available, %v total replicas, with observed generation %v", replicas, deployment.Generation, deployment.Status.UpdatedReplicas, deployment.Status.AvailableReplicas, deployment.Status.Replicas, deployment.Status.ObservedGeneration)
	}
	return nil
}

// Wait for the provided deployment to have the specified environment variable set.
func waitForDeploymentEnvVar(t *testing.T, cl client.Client, deployment *appsv1.Deployment, timeout time.Duration, name, value string) error {
	t.Helper()
	deploymentName := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
			t.Logf("error getting deployment %s: %v", name, err)
			return false, nil
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == "router" {
				for _, v := range container.Env {
					if v.Name == name {
						return v.Value == value, nil
					}
				}
			}
		}
		return false, nil
	})
	return err
}

func clusterOperatorConditionMap(conditions ...configv1.ClusterOperatorStatusCondition) map[string]string {
	conds := map[string]string{}
	for _, cond := range conditions {
		conds[string(cond.Type)] = string(cond.Status)
	}
	return conds
}

func operatorConditionMap(conditions ...operatorv1.OperatorCondition) map[string]string {
	conds := map[string]string{}
	for _, cond := range conditions {
		conds[cond.Type] = string(cond.Status)
	}
	return conds
}

func routeConditionMap(conditions ...routev1.RouteIngressCondition) map[string]string {
	conds := map[string]string{}
	for _, cond := range conditions {
		conds[string(cond.Type)] = string(cond.Status)
	}
	return conds
}

func conditionsMatchExpected(expected, actual map[string]string) bool {
	filtered := map[string]string{}
	for k := range actual {
		if _, comparable := expected[k]; comparable {
			filtered[k] = actual[k]
		}
	}
	return reflect.DeepEqual(expected, filtered)
}

func waitForClusterOperatorConditions(cl client.Client, conditions ...configv1.ClusterOperatorStatusCondition) error {
	return wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		co := &configv1.ClusterOperator{}
		if err := cl.Get(context.TODO(), controller.IngressClusterOperatorName(), co); err != nil {
			return false, err
		}

		expected := clusterOperatorConditionMap(conditions...)
		current := clusterOperatorConditionMap(co.Status.Conditions...)
		return conditionsMatchExpected(expected, current), nil
	})
}

func waitForRouteIngressConditions(cl client.Client, routeName types.NamespacedName, routerName string, conditions ...routev1.RouteIngressCondition) error {
	return wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		route := &routev1.Route{}
		if err := cl.Get(context.TODO(), routeName, route); err != nil {
			return false, err
		}

		for _, ingress := range route.Status.Ingress {
			if ingress.RouterName == routerName {
				expected := routeConditionMap(conditions...)
				current := routeConditionMap(ingress.Conditions...)
				return conditionsMatchExpected(expected, current), nil
			}
		}

		return false, nil
	})
}

func waitForIngressControllerCondition(cl client.Client, timeout time.Duration, name types.NamespacedName, conditions ...operatorv1.OperatorCondition) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := cl.Get(context.TODO(), name, ic); err != nil {
			return false, err
		}
		expected := operatorConditionMap(conditions...)
		current := operatorConditionMap(ic.Status.Conditions...)
		return conditionsMatchExpected(expected, current), nil
	})
}

func assertIngressControllerDeleted(t *testing.T, cl client.Client, ing *operatorv1.IngressController) {
	if err := deleteIngressController(cl, ing, 2*time.Minute); err != nil {
		t.Fatalf("WARNING: cloud resources may have been leaked! failed to delete ingresscontroller %s: %v", ing.Name, err)
	} else {
		t.Logf("deleted ingresscontroller %s", ing.Name)
	}
}

func deleteIngressController(cl client.Client, ic *operatorv1.IngressController, timeout time.Duration) error {
	name := types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}
	if err := cl.Delete(context.TODO(), ic); err != nil {
		return fmt.Errorf("failed to delete ingresscontroller: %v", err)
	}

	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ic); err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("timed out waiting for ingresscontroller to be deleted: %v", err)
	}
	return nil
}

func createDefaultCertTestSecret(cl client.Client, name string) (*corev1.Secret, error) {
	defaultCert := `-----BEGIN CERTIFICATE-----
MIIDIjCCAgqgAwIBAgIBBjANBgkqhkiG9w0BAQUFADCBoTELMAkGA1UEBhMCVVMx
CzAJBgNVBAgMAlNDMRUwEwYDVQQHDAxEZWZhdWx0IENpdHkxHDAaBgNVBAoME0Rl
ZmF1bHQgQ29tcGFueSBMdGQxEDAOBgNVBAsMB1Rlc3QgQ0ExGjAYBgNVBAMMEXd3
dy5leGFtcGxlY2EuY29tMSIwIAYJKoZIhvcNAQkBFhNleGFtcGxlQGV4YW1wbGUu
Y29tMB4XDTE2MDExMzE5NDA1N1oXDTI2MDExMDE5NDA1N1owfDEYMBYGA1UEAxMP
d3d3LmV4YW1wbGUuY29tMQswCQYDVQQIEwJTQzELMAkGA1UEBhMCVVMxIjAgBgkq
hkiG9w0BCQEWE2V4YW1wbGVAZXhhbXBsZS5jb20xEDAOBgNVBAoTB0V4YW1wbGUx
EDAOBgNVBAsTB0V4YW1wbGUwgZ8wDQYJKoZIhvcNAQEBBQADgY0AMIGJAoGBAM0B
u++oHV1wcphWRbMLUft8fD7nPG95xs7UeLPphFZuShIhhdAQMpvcsFeg+Bg9PWCu
v3jZljmk06MLvuWLfwjYfo9q/V+qOZVfTVHHbaIO5RTXJMC2Nn+ACF0kHBmNcbth
OOgF8L854a/P8tjm1iPR++vHnkex0NH7lyosVc/vAgMBAAGjDTALMAkGA1UdEwQC
MAAwDQYJKoZIhvcNAQEFBQADggEBADjFm5AlNH3DNT1Uzx3m66fFjqqrHEs25geT
yA3rvBuynflEHQO95M/8wCxYVyuAx4Z1i4YDC7tx0vmOn/2GXZHY9MAj1I8KCnwt
Jik7E2r1/yY0MrkawljOAxisXs821kJ+Z/51Ud2t5uhGxS6hJypbGspMS7OtBbw7
8oThK7cWtCXOldNF6ruqY1agWnhRdAq5qSMnuBXuicOP0Kbtx51a1ugE3SnvQenJ
nZxdtYUXvEsHZC/6bAtTfNh+/SwgxQJuL2ZM+VG3X2JIKY8xTDui+il7uTh422lq
wED8uwKl+bOj6xFDyw4gWoBxRobsbFaME8pkykP1+GnKDberyAM=
-----END CERTIFICATE-----
`

	defaultKey := `-----BEGIN RSA PRIVATE KEY-----
MIICWwIBAAKBgQDNAbvvqB1dcHKYVkWzC1H7fHw+5zxvecbO1Hiz6YRWbkoSIYXQ
EDKb3LBXoPgYPT1grr942ZY5pNOjC77li38I2H6Pav1fqjmVX01Rx22iDuUU1yTA
tjZ/gAhdJBwZjXG7YTjoBfC/OeGvz/LY5tYj0fvrx55HsdDR+5cqLFXP7wIDAQAB
AoGAfE7P4Zsj6zOzGPI/Izj7Bi5OvGnEeKfzyBiH9Dflue74VRQkqqwXs/DWsNv3
c+M2Y3iyu5ncgKmUduo5X8D9To2ymPRLGuCdfZTxnBMpIDKSJ0FTwVPkr6cYyyBk
5VCbc470pQPxTAAtl2eaO1sIrzR4PcgwqrSOjwBQQocsGAECQQD8QOra/mZmxPbt
bRh8U5lhgZmirImk5RY3QMPI/1/f4k+fyjkU5FRq/yqSyin75aSAXg8IupAFRgyZ
W7BT6zwBAkEA0A0ugAGorpCbuTa25SsIOMxkEzCiKYvh0O+GfGkzWG4lkSeJqGME
keuJGlXrZNKNoCYLluAKLPmnd72X2yTL7wJARM0kAXUP0wn324w8+HQIyqqBj/gF
Vt9Q7uMQQ3s72CGu3ANZDFS2nbRZFU5koxrggk6lRRk1fOq9NvrmHg10AQJABOea
pgfj+yGLmkUw8JwgGH6xCUbHO+WBUFSlPf+Y50fJeO+OrjqPXAVKeSV3ZCwWjKT4
9viXJNJJ4WfF0bO/XwJAOMB1wQnEOSZ4v+laMwNtMq6hre5K8woqteXICoGcIWe8
u3YLAbyW/lHhOCiZu2iAI8AbmXem9lW6Tr7p/97s0w==
-----END RSA PRIVATE KEY-----
`

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "openshift-ingress",
		},
		Data: map[string][]byte{
			"tls.crt": []byte(defaultCert),
			"tls.key": []byte(defaultKey),
		},
		Type: corev1.SecretTypeTLS,
	}

	if err := cl.Delete(context.TODO(), secret); err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	return secret, cl.Create(context.TODO(), secret)
}
