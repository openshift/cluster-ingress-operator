//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v2"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	"github.com/aws/aws-sdk-go/aws/endpoints"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	availableConditionsForPrivateIngressController = []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	availableConditionsForIngressControllerWithNodePort = []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
	}
	availableConditionsForIngressControllerWithLoadBalancer = []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
	}
	availableConditionsForIngressControllerWithHostNetwork = []operatorv1.OperatorCondition{
		{Type: ingresscontroller.IngressControllerAdmittedConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: ingresscontroller.IngressControllerDeploymentReplicasAllAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	// The ingress canary check status condition only applies to the default ingress controller.
	defaultAvailableConditions = append(availableConditionsForIngressControllerWithLoadBalancer, operatorv1.OperatorCondition{Type: ingresscontroller.IngressControllerCanaryCheckSuccessConditionType, Status: operatorv1.ConditionTrue})
)

var kclient client.Client
var dnsConfig configv1.DNS
var infraConfig configv1.Infrastructure
var operatorNamespace = operatorcontroller.DefaultOperatorNamespace
var operandNamespace = operatorcontroller.DefaultOperandNamespace
var defaultName = types.NamespacedName{Namespace: operatorNamespace, Name: manifests.DefaultIngressControllerName}
var clusterConfigName = types.NamespacedName{Namespace: operatorNamespace, Name: manifests.ClusterIngressConfigName}

func TestMain(m *testing.M) {
	if os.Getenv("E2E_TEST_MAIN_SKIP_SETUP") == "1" {
		// If we are deriving the set of tests via `go test
		// -list` then we don't always have a KUBECONFIG
		// (e.g., CI, or local dev) so we do none of the
		// overall test setup below because calls to
		// getConfig() will fail and no test names will be
		// discovered.
		os.Exit(m.Run())
	}
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
	if err := waitForClusterOperatorConditions(t, kclient, expected...); err != nil {
		t.Errorf("did not get expected available condition: %v", err)
	}
}

func TestClusterOperatorStatusRelatedObjects(t *testing.T) {
	expected := []configv1.ObjectReference{
		{
			Resource: "namespaces",
			Name:     operatorNamespace,
		},
		{
			Group:     operatorv1.GroupName,
			Resource:  "ingresscontrollers",
			Namespace: operatorNamespace,
		},
		{
			Group:     iov1.GroupVersion.Group,
			Resource:  "dnsrecords",
			Namespace: operatorNamespace,
		},
		{
			Resource: "namespaces",
			Name:     "openshift-ingress",
		},
		{
			Resource: "namespaces",
			Name:     "openshift-ingress-canary",
		},
	}

	coName := controller.IngressClusterOperatorName()
	err := wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		co := &configv1.ClusterOperator{}
		if err := kclient.Get(context.TODO(), coName, co); err != nil {
			t.Logf("failed to get ingress cluster operator %s: %v", coName, err)
			return false, nil
		}

		return reflect.DeepEqual(expected, co.Status.RelatedObjects), nil
	})
	if err != nil {
		t.Errorf("did not get expected status related objects: %v", err)
	}
}

func TestDefaultIngressControllerSteadyConditions(t *testing.T) {
	if err := waitForIngressControllerCondition(t, kclient, 10*time.Second, defaultName, defaultAvailableConditions...); err != nil {
		t.Errorf("did not get expected conditions: %v", err)
	}
}

// TestDefaultIngressClass verifies that the ingressclass controller has created
// an ingressclass for the default ingresscontroller and recreates the
// ingressclass if it is deleted.
func TestDefaultIngressClass(t *testing.T) {
	// The controller should create a "openshift-default" ingressclass.
	name := controller.IngressClassName(manifests.DefaultIngressControllerName)
	ingressclass := &networkingv1.IngressClass{}
	if err := kclient.Get(context.TODO(), name, ingressclass); err != nil {
		t.Fatalf("failed to get ingressclass %q: %v", name.Name, err)
	}

	// The controller should have made the "openshift-default" ingressclass
	// the default ingressclass.
	//
	// TODO This is commented out because it breaks "[sig-network]
	// IngressClass [Feature:Ingress] should not set default value if no
	// default IngressClass"; we need to fix that test and then re-enable
	// this one.
	//
	// const (
	// 	defaultAnnotation = "ingressclass.kubernetes.io/is-default-class"
	// 	expected          = "true"
	// )
	// if actual, ok := ingressclass.Annotations[defaultAnnotation]; !ok {
	// 	t.Fatalf("ingressclass %q has no %q annotation", name.Name, defaultAnnotation)
	// } else if actual != expected {
	// 	t.Fatalf("expected %q annotation to have value %q, found %q", defaultAnnotation, expected, actual)
	// }

	// The controller should recreate the ingressclass if it is deleted.
	if err := kclient.Delete(context.TODO(), ingressclass); err != nil {
		t.Fatalf("failed to delete ingressclass %q: %v", name.Name, err)
	}
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ingressclass); err != nil {
			t.Logf("failed to get ingressclass %q: %v", name.Name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe recreated ingressclass %q: %v", name.Name, err)
	}
}

// TestCustomIngressClass verifies that the ingressclass controller creates an
// ingressclass for a custom ingresscontroller and deletes the ingressclass if
// the ingresscontroller is deleted.
func TestCustomIngressClass(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      "testcustomingressclass",
	}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
		assertIngressControllerDeleted(t, kclient, ic)
		t.FailNow()
	}

	// The controller should create an ingressclass named
	// "openshift-testcustomingressclass".
	ingressclassName := controller.IngressClassName(icName.Name)
	ingressclass := &networkingv1.IngressClass{}
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), ingressclassName, ingressclass); err != nil {
			t.Logf("failed to get ingressclass %q: %v", ingressclassName.Name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Errorf("failed to get ingressclass %q: %v", ingressclassName.Name, err)
		assertIngressControllerDeleted(t, kclient, ic)
		t.FailNow()
	}

	// The controller should *not* have made the
	// "openshift-testcustomingressclass" ingressclass the default
	// ingressclass.
	const defaultAnnotation = "ingressclass.kubernetes.io/is-default-class"
	if actual, ok := ingressclass.Annotations[defaultAnnotation]; ok && actual != "false" {
		t.Errorf("ingressclass %q has annotation %q with value %q", ingressclassName.Name, defaultAnnotation, actual)
	}

	// The controller should delete the ingressclass if the
	// ingresscontroller is deleted.
	assertIngressControllerDeleted(t, kclient, ic)
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), ingressclassName, ingressclass); err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("failed to get ingressclass %q: %v", ingressclassName.Name, err)
			return false, nil
		}
		return false, nil
	})
	if err != nil {
		t.Errorf("failed to observe deletion of ingressclass %q: %v", ingressclassName.Name, err)
	}
}

// TestOperatorRecreatesItsClusterOperator verifies that the ingress operator
// recreates the "ingress" clusteroperator if the clusteroperator is deleted.
//
// This is a serial test because it depends on modifying a shared resource: the
// clusteroperator object (which means that this test could interfere with other
// tests) and then verifying that the operator reconciles the resource
// automatically (which means that other tests could interfere with this one).
func TestOperatorRecreatesItsClusterOperator(t *testing.T) {
	co := &configv1.ClusterOperator{}
	coName := controller.IngressClusterOperatorName()
	if err := kclient.Get(context.TODO(), coName, co); err != nil {
		t.Fatalf("failed to get clusteroperator %q: %v", coName.Name, err)
	}
	oldUid := co.UID
	if err := kclient.Delete(context.TODO(), co); err != nil {
		t.Fatalf("failed to delete clusteroperator %q: %v", coName.Name, err)
	}
	err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), coName, co); err != nil {
			t.Logf("failed to get clusteroperator %q: %v", coName.Name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe recreation of clusteroperator %q: %v", coName.Name, err)
	}
	if co.DeletionTimestamp != nil {
		t.Fatalf("expected clusteroperator %q not to be marked for deletion, but its deletion timestamp is set: %v", coName.Name, co.DeletionTimestamp)
	}
	if co.UID == oldUid {
		t.Fatalf("expected clusteroperator %q to have a new uid after deletion, but it still has the old uid: %v", coName.Name, co.UID)
	}
}

func TestUserDefinedIngressController(t *testing.T) {
	t.Parallel()
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "testuserdefinedingresscontroller"}
	ing := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	if err := kclient.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ing)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

func TestUniqueDomainRejection(t *testing.T) {
	t.Parallel()
	def := &operatorv1.IngressController{}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
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
	err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, conflictName, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

// TestProxyProtocolOnAWS verifies that the default ingresscontroller
// uses PROXY protocol on AWS.
//
// TODO: should this be a test of source IP preservation in the conformance suite?
func TestProxyProtocolOnAWS(t *testing.T) {
	if infraConfig.Status.Platform != configv1.AWSPlatformType {
		t.Skip("test skipped on non-aws platform")
		return
	}

	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
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

// TestProxyProtocolAPI verifies that the operator configures router pod
// replicas to use PROXY protocol if it is specified on an ingresscontroller.
func TestProxyProtocolAPI(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "proxy-protocol"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newNodePortController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithNodePort...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", ""); err != nil {
		t.Fatalf("expected initial deployment not to enable PROXY protocol: %v", err)
	}

	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.EndpointPublishingStrategy.NodePort = &operatorv1.NodePortStrategy{
		Protocol: operatorv1.ProxyProtocol,
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", "true"); err != nil {
		t.Fatalf("expected updated deployment to enable PROXY protocol: %v", err)
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
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

	if err := updateIngressControllerSpecWithRetryOnConflict(t, defaultName, timeout, func(spec *operatorv1.IngressControllerSpec) {
		spec.DefaultCertificate = &corev1.LocalObjectReference{Name: secret.Name}
	}); err != nil {
		t.Fatalf("failed to update default ingress controller: %v", err)
	}

	name := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, deployment); err != nil {
			t.Logf("failed to get deployment %s: %v", name, err)
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
			t.Logf("failed to get CA config map %s: %v", controller.DefaultIngressCertConfigMapName(), err)
			return false, nil
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
	if err := updateIngressControllerSpecWithRetryOnConflict(t, defaultName, timeout, func(spec *operatorv1.IngressControllerSpec) {
		spec.DefaultCertificate = originalSecret
	}); err != nil {
		t.Fatalf("failed to reset default ingresscontroller: %v", err)
	}

	// Wait for the default ingress configmap to be updated back to the original
	previousDefaultIngressCAConfigmap = defaultIngressCAConfigmap.DeepCopy()
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.DefaultIngressCertConfigMapName(), defaultIngressCAConfigmap); err != nil {
			t.Logf("failed to get CA config map %s: %v", controller.DefaultIngressCertConfigMapName(), err)
			return false, nil
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

// TestIngressControllerScale exercises a simple scale up/down scenario.  This
// test creates a private ingresscontroller with 1 replica and then creates a
// scale client and uses it to scale the ingresscontroller up to 2 replicas and
// then back down to 1 replica.
func TestIngressControllerScale(t *testing.T) {
	t.Parallel()
	// Create a new ingresscontroller.
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "scale"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(name, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", name, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Get the ingresscontroller's deployment's selector and replicaset.
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get deployment for ingresscontroller %s: %v", name, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		t.Fatalf("router deployment has invalid spec.selector: %v", err)
	}

	oldRsList := &appsv1.ReplicaSetList{}
	if err := kclient.List(context.TODO(), oldRsList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		t.Fatalf("failed to list replicasets for ingresscontroller %s: %v", name, err)
	}

	// Get a scale client.
	//
	// TODO: Use controller-runtime once it supports the /scale subresource.
	scaleClient, err := getScaleClient()
	if err != nil {
		t.Fatal(err)
	}

	// Get the ingresscontroller's scale.
	resource := schema.GroupResource{
		Group:    "operator.openshift.io",
		Resource: "ingresscontrollers",
	}
	scale, err := scaleClient.Scales(name.Namespace).Get(context.TODO(), resource, name.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get initial scale of ingresscontroller %s: %v", name, err)
	}

	// Make sure the deployment's selector is reflected in the scale status.
	if scale.Status.Selector != selector.String() {
		t.Fatalf("expected scale status.selector to be %q, got %q", selector.String(), scale.Status.Selector)
	}

	// Scale the ingresscontroller up.
	originalReplicas := scale.Spec.Replicas
	newReplicas := originalReplicas + 1
	scale.Spec.Replicas = newReplicas
	updatedScale, err := scaleClient.Scales(name.Namespace).Update(context.TODO(), resource, scale, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to scale ingresscontroller %s up: %v", name, err)
	}

	if updatedScale.Spec.Replicas != scale.Spec.Replicas {
		t.Fatalf("expected ingresscontroller %s to have spec.replicas equal to %d, got %d", name, scale.Spec.Replicas, updatedScale.Spec.Replicas)
	}

	// Wait for the deployment scale-up to be observed.
	if err := waitForAvailableReplicas(t, kclient, ic, 4*time.Minute, newReplicas); err != nil {
		t.Fatalf("failed waiting deployment of ingresscontroller %s to scale to %d: %v", name, newReplicas, err)
	}

	// Ensure the ingresscontroller remains available
	if err := waitForIngressControllerCondition(t, kclient, 2*time.Minute, name, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Scale back down.
	scale, err = scaleClient.Scales(name.Namespace).Get(context.TODO(), resource, name.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get updated scale of ClusterIngress: %v", err)
	}

	scale.Spec.Replicas = originalReplicas
	updatedScale, err = scaleClient.Scales(name.Namespace).Update(context.TODO(), resource, scale, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to scale ingresscontroller down: %v", err)
	}

	if updatedScale.Spec.Replicas != scale.Spec.Replicas {
		t.Fatalf("expected ingresscontroller %s to have spec.replicas equal to %d, got %d", name, scale.Spec.Replicas, updatedScale.Spec.Replicas)
	}

	// Wait for the deployment scale down to be observed.
	if err := waitForAvailableReplicas(t, kclient, ic, 2*time.Minute, originalReplicas); err != nil {
		t.Fatalf("failed waiting deployment of ingresscontroller %s to scale to %d: %v", name, originalReplicas, err)
	}

	// Ensure the ingresscontroller remains available
	// TODO: assert that the conditions hold steady for some amount of time?
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure the deployment did not create a new replicaset
	// (see <https://bugzilla.redhat.com/show_bug.cgi?id=1783007>).
	newRsList := &appsv1.ReplicaSetList{}
	if err := kclient.List(context.TODO(), newRsList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		t.Fatalf("failed to list replicasets for ingresscontroller %s: %v", name, err)
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
		t.Fatalf("scaling the deployment of ingresscontroller %s created a new replicaset\nold replicaset list:\n%#v\nnew replicaset list:\n%#v)", name, oldRsList.Items, newRsList.Items)
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

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
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

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	pdb := &policyv1.PodDisruptionBudget{}
	if err := kclient.Get(context.TODO(), controller.RouterPodDisruptionBudgetName(ic), pdb); err != nil {
		t.Fatalf("failed to get default ingresscontroller poddisruptionbudget: %v", err)
	}
}

// TestHostNetworkEndpointPublishingStrategy creates an ingresscontroller with
// the "HostNetwork" endpoint publishing strategy type and verifies that the
// operator creates a router and that the router becomes available.
func TestHostNetworkEndpointPublishingStrategy(t *testing.T) {
	t.Parallel()
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "hostnetworkendpointpublishingstrategy"}
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
	err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

// TestHostNetworkPortBinding creates two ingresscontrollers on the same node
// with different port bindings and verifies that both routers are available.
func TestHostNetworkPortBinding(t *testing.T) {
	t.Parallel()
	// deploy first ingresscontroller with the default port bindings
	name1 := types.NamespacedName{Namespace: operatorNamespace, Name: "hostnetworkportbinding"}
	ing1 := newHostNetworkController(name1, name1.Name+"."+dnsConfig.Spec.BaseDomain)
	if err := kclient.Create(context.TODO(), ing1); err != nil {
		t.Fatalf("failed to create the first ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ing1)

	err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name1, availableConditionsForIngressControllerWithHostNetwork...)
	if err != nil {
		t.Errorf("failed to observe expected conditions for the first ingresscontroller: %v", err)
	}

	// get first router's single replica
	pods := &corev1.PodList{}
	if err := kclient.List(context.TODO(), pods, client.InNamespace(operandNamespace)); err != nil {
		t.Fatalf("failed to list the first ingresscontroller's PODs: %v", err)
	}
	var pod1 *corev1.Pod
	for _, p := range pods.Items {
		if strings.HasPrefix(p.Name, "router-"+name1.Name) {
			pod1 = &p
			break
		}
	}
	if pod1 == nil {
		t.Fatal("failed to find the first ingresscontroller's POD")
	}

	routerContainer := pod1.Spec.Containers[0]
	assertContainerHasPort(t, routerContainer, "http", 80)
	assertContainerHasPort(t, routerContainer, "https", 443)
	assertContainerHasPort(t, routerContainer, "metrics", 1936)
	if !pod1.Spec.HostNetwork {
		t.Errorf("pod %s is not running on the host's network", pod1.Name)
	}

	// create second ingresscontroller on the same node but with different port bindings
	name2 := types.NamespacedName{Namespace: operatorNamespace, Name: "samehost"}
	strategy := &operatorv1.HostNetworkStrategy{
		HTTPPort:  9080,
		HTTPSPort: 9443,
		StatsPort: 9936,
	}
	// take the node placement of the first router
	placement := &operatorv1.NodePlacement{
		Tolerations: pod1.Spec.Tolerations,
		NodeSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/hostname": pod1.Spec.NodeName},
		},
	}

	ing2 := newHostNetworkController(name2, name2.Name+"."+dnsConfig.Spec.BaseDomain)
	ing2.Spec.NodePlacement = placement
	ing2.Spec.EndpointPublishingStrategy.HostNetwork = strategy
	if err := kclient.Create(context.TODO(), ing2); err != nil {
		t.Fatalf("failed to create the second ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ing2)

	err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, name2, availableConditionsForIngressControllerWithHostNetwork...)
	if err != nil {
		t.Errorf("failed to observe expected conditions for the second ingresscontroller: %v", err)
	}

	if err := kclient.List(context.TODO(), pods, client.InNamespace(operandNamespace)); err != nil {
		t.Fatalf("failed to list the first ingresscontroller's PODs: %v", err)
	}
	var pod2 *corev1.Pod
	for _, p := range pods.Items {
		if strings.HasPrefix(p.Name, "router-"+name2.Name) {
			pod2 = &p
			break
		}
	}
	if pod2 == nil {
		t.Fatalf("failed to find the second ingresscontroller's POD")
	}
	routerContainer = pod2.Spec.Containers[0]
	assertContainerHasPort(t, routerContainer, "http", 9080)
	assertContainerHasPort(t, routerContainer, "https", 9443)
	assertContainerHasPort(t, routerContainer, "metrics", 9936)
	if !pod2.Spec.HostNetwork {
		t.Errorf("pod %s is not running on the host's network", pod2.Name)
	}
}

func assertContainerHasPort(t *testing.T, container corev1.Container, name string, port int32) {
	t.Helper()
	for _, p := range container.Ports {
		if p.Name == name && p.ContainerPort == port {
			return
		}
	}
	t.Errorf("container %s does not have port named %q open on %d", container.Name, name, port)
}

// TestInternalLoadBalancer creates an ingresscontroller with the
// "LoadBalancerService" endpoint publishing strategy type with scope set to
// "Internal" and verifies that the operator creates a load balancer and that
// the load balancer has a private IP address.
func TestInternalLoadBalancer(t *testing.T) {
	t.Parallel()
	platform := infraConfig.Status.Platform

	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AWSPlatformType:          {},
		configv1.AzurePlatformType:        {},
		configv1.GCPPlatformType:          {},
		configv1.IBMCloudPlatformType:     {},
		configv1.AlibabaCloudPlatformType: {},
	}
	if _, supported := supportedPlatforms[platform]; !supported {
		t.Skip(fmt.Sprintf("test skipped on platform %q", platform))
	}

	annotation := ingresscontroller.InternalLBAnnotations[platform]

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "testinternalloadbalancer"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.InternalLoadBalancer,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
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

	// On AWS, verify that the operator normalizes the
	// service.beta.kubernetes.io/aws-load-balancer-internal=0.0.0.0/0
	// annotation to replace "0.0.0.0/0" with "true".  See
	// <https://bugzilla.redhat.com/show_bug.cgi?id=2055470>.
	if platform == configv1.AWSPlatformType {
		const annotation = "service.beta.kubernetes.io/aws-load-balancer-internal"

		// Set the annotation value to "0.0.0.0/0".
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			t.Fatalf("failed to get LoadBalancer service: %v", err)
		}
		lbService.Annotations[annotation] = "0.0.0.0/0"
		if err := kclient.Update(context.TODO(), lbService); err != nil {
			t.Errorf("failed to update LoadBalancer service: %v", err)
		}

		// Verify that the operator reverts the annotation value to
		// "true".
		err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
			if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
				t.Logf("failed to get LoadBalancer service: %v", err)
				return false, nil
			}
			if v, ok := lbService.Annotations[annotation]; !ok {
				t.Logf("load balancer has no %q annotation: %v", annotation, lbService.Annotations)
				return false, nil
			} else if v != "true" {
				t.Logf("expected %s=%s, found %s=%s", annotation, "true", annotation, v)
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			t.Errorf("failed to observe expected annotation on load balancer service %s: %v", controller.LoadBalancerServiceName(ic), err)
		}
	}
}

// TestInternalLoadBalancerGlobalAccessGCP creates an ingresscontroller
// with internal load balancer on GCP with the GCP Global Access provider
// parameter set to both "Global" and "local" to verify that the
// Load Balancer service is created properly.
func TestInternalLoadBalancerGlobalAccessGCP(t *testing.T) {
	t.Parallel()
	platform := infraConfig.Status.Platform

	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.GCPPlatformType: {},
	}
	if _, supported := supportedPlatforms[platform]; !supported {
		t.Skip(fmt.Sprintf("test skipped on platform %q", platform))
	}

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "test-gcp"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.InternalLoadBalancer,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.GCPLoadBalancerProvider,
			GCP: &operatorv1.GCPLoadBalancerParameters{
				ClientAccess: operatorv1.GCPGlobalAccess,
			},
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	// Verify load balancer has desired global access annotation
	annotation := ingresscontroller.GCPGlobalAccessAnnotation
	// A ClientAccess value of "Global" results in the Global Access Annotation
	// being set to "true".
	expected := "true"

	if actual, ok := lbService.Annotations[annotation]; !ok {
		t.Fatalf("load balancer has no %q annotation: %v", annotation, lbService.Annotations)
	} else if actual != expected {
		t.Fatalf("expected %s=%s, found %s=%s", annotation, expected, annotation, actual)
	}

	// Update ingress controller to use "Local" Global Access
	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}

	ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.GCP.ClientAccess = operatorv1.GCPLocalAccess

	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Errorf("failed to update ingresscontroller %s: %v", name, err)
	}

	// A ClientAccess value of "Local" results in the Global Access Annotation
	// being set to "false".
	expected = "false"

	// Verify load balancer has desired global access annotation.
	// Use a polling loop since the operator might not switch out the annotation
	// immediately.
	err := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			t.Logf("failed to get LoadBalancer service: %v", err)
			return false, nil
		}
		if actual, ok := lbService.Annotations[annotation]; !ok {
			t.Logf("load balancer has no %q annotation: %v", annotation, lbService.Annotations)
			return false, nil
		} else if actual != expected {
			t.Logf("expected %s=%s, found %s=%s", annotation, expected, annotation, actual)
			return false, nil
		}
		return true, nil
	})

	if err != nil {
		t.Errorf("failed to observe expected annotations on load balancer service %s: %v", controller.LoadBalancerServiceName(ic), err)
	}
}

// TestScopeChange creates an ingresscontroller with the "LoadBalancerService"
// endpoint publishing strategy type and verifies that the operator behaves
// correctly when the ingresscontroller's scope is mutated.  The correct
// behavior depends on the platform on which the test is running.  On AWS and
// IBM Cloud, the operator should set the Progressing=True status condition to
// prompt the administrator to delete the old LoadBalancer service.  On Azure
// and GCP, the operator should update the LoadBalancer service's annotations.
//
// As a special case, if the ingresscontroller's scope has been changed, the
// "ingress.operator.openshift.io/auto-delete-load-balancer" annotation is set
// on the ingresscontroller, and the current platform requires deleting and
// recreating the LoadBalancer service to change its scope, then the operator
// should delete and recreate the service automatically.
func TestScopeChange(t *testing.T) {
	t.Parallel()
	platform := infraConfig.Status.Platform
	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AlibabaCloudPlatformType: {},
		configv1.AWSPlatformType:          {},
		configv1.AzurePlatformType:        {},
		configv1.GCPPlatformType:          {},
		configv1.IBMCloudPlatformType:     {},
		configv1.PowerVSPlatformType:      {},
	}
	if _, supported := supportedPlatforms[platform]; !supported {
		t.Skipf("test skipped on platform %q", platform)
	}

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "scope"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.ExternalLoadBalancer,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	if ingresscontroller.IsServiceInternal(lbService) {
		t.Fatalf("load balancer is internal but should be external")
	}

	// Change the scope to internal and wait for everything to come back to
	// normal.
	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.Scope = operatorv1.InternalLoadBalancer

	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatal(err)
	}

	switch platform {
	case configv1.AlibabaCloudPlatformType, configv1.AWSPlatformType, configv1.IBMCloudPlatformType, configv1.PowerVSPlatformType:
		progressingTrue := operatorv1.OperatorCondition{
			Type:   operatorv1.OperatorStatusTypeProgressing,
			Status: operatorv1.ConditionTrue,
		}
		if err := waitForIngressControllerCondition(t, kclient, 1*time.Minute, name, progressingTrue); err != nil {
			t.Fatalf("failed to observe the ingresscontroller report Progressing=True: %v", err)
		}
	case configv1.AzurePlatformType, configv1.GCPPlatformType:
		err := wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
			service := &corev1.Service{}
			if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), service); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				t.Logf("failed to get service %s: %v", controller.LoadBalancerServiceName(ic), err)
				return false, nil
			}
			if ingresscontroller.IsServiceInternal(service) {
				return true, nil
			}
			t.Logf("service is still external: %#v\n", service)
			return false, nil
		})
		if err != nil {
			t.Fatalf("expected load balancer to become internal: %v", err)
		}
	}

	// Change the scope back to external and wait for everything to come
	// back to normal.
	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.Scope = operatorv1.ExternalLoadBalancer

	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatal(err)
	}

	switch platform {
	case configv1.AWSPlatformType, configv1.IBMCloudPlatformType:
		progressingFalse := operatorv1.OperatorCondition{
			Type:   operatorv1.OperatorStatusTypeProgressing,
			Status: operatorv1.ConditionFalse,
		}
		if err := waitForIngressControllerCondition(t, kclient, 1*time.Minute, name, progressingFalse); err != nil {
			t.Fatalf("failed to observe the ingresscontroller report Progressing=True: %v", err)
		}
	case configv1.AzurePlatformType, configv1.GCPPlatformType:
		err := wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
			service := &corev1.Service{}
			if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), service); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				t.Logf("failed to get ingresscontroller %s: %v", name.Name, err)
				return false, nil
			}
			if ingresscontroller.IsServiceInternal(service) {
				t.Logf("service is still internal: %#v\n", service)
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			t.Fatalf("expected load balancer to become external: %v", err)
		}
	}

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Annotate the ingresscontroller to tell the operator to automatically
	// delete and recreate the LoadBalancer service if necessary when the
	// scope changes, and change the scope.
	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	if ic.Annotations == nil {
		ic.Annotations = map[string]string{}
	}
	ic.Annotations["ingress.operator.openshift.io/auto-delete-load-balancer"] = ""
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.Scope = operatorv1.InternalLoadBalancer

	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatal(err)
	}

	err := wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
		service := &corev1.Service{}
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), service); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			t.Logf("failed to get service %s: %v", controller.LoadBalancerServiceName(ic), err)
			return false, nil
		}
		if ingresscontroller.IsServiceInternal(service) {
			return true, nil
		}
		t.Logf("service is still external: %#v\n", service)
		return false, nil
	})
	if err != nil {
		t.Fatalf("expected load balancer to become internal: %v", err)
	}
}

// TestNodePortServiceEndpointPublishingStrategy creates an ingresscontroller
// with the "NodePortService" endpoint publishing strategy type and verifies
// that the operator creates a router, that the router becomes available, and
// that the operator creates the expected NodePort-type service.
//
// The test then removes the "metrics" port from the NodePort-type service and
// verifies that the operator does not add the port back.  See
// <https://bugzilla.redhat.com/show_bug.cgi?id=1881210>.
func TestNodePortServiceEndpointPublishingStrategy(t *testing.T) {
	t.Parallel()
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
	err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	// Make sure the ingresscontroller has a nodeport service
	// with the expected ports.
	svcName := controller.NodePortServiceName(ing)
	service := &corev1.Service{}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), svcName, service); err != nil {
			t.Logf("failed to get service %q: %v", svcName, err)
			return false, nil
		}
		return true, nil
	})
	ports := sets.String{}
	for _, port := range service.Spec.Ports {
		ports.Insert(port.Name)
	}
	expectedPorts := sets.NewString("http", "https", "metrics")
	if !ports.Equal(expectedPorts) {
		t.Fatalf("expected service %q to have ports %v, got %v", svcName, expectedPorts.List(), ports.List())
	}

	// Delete the "metrics" port and verify that the operator
	// does not restore it.
	for i, port := range service.Spec.Ports {
		if port.Name == "metrics" {
			ports := service.Spec.Ports
			ports = append(ports[:i], ports[i+1:]...)
			service.Spec.Ports = ports
		}
	}
	if err := kclient.Update(context.TODO(), service); err != nil {
		t.Fatalf("failed to update service %q: %v", svcName, err)
	}
	expectedPorts = sets.NewString("http", "https")
	wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), svcName, service); err != nil {
			t.Logf("failed to get service %q: %v", svcName, err)
			return false, nil
		}
		ports = sets.String{}
		for _, port := range service.Spec.Ports {
			ports.Insert(port.Name)
		}
		return ports.Equal(expectedPorts), nil
	})
	if !ports.Equal(expectedPorts) {
		t.Fatalf("after deleting the \"metrics\" port, expected service %q to have ports %v, got %v", svcName, expectedPorts.List(), ports.List())
	}
}

// TestTLSSecurityProfile creates an ingresscontroller with no explicit TLS
// profile, then verifies that the operator sets the default "Intermediate" TLS
// profile, then updates the ingresscontroller to use a custom TLS profile, and
// then verifies that the operator reflects the custom profile in its status.
func TestTLSSecurityProfile(t *testing.T) {
	t.Parallel()
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "testtlssecurityprofile"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(name, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", name, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	if ic.Status.TLSProfile == nil {
		t.Fatalf("ingresscontroller status has no security profile")
	}
	intermediateProfileSpec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]

	actualCiphers := ic.Status.TLSProfile.Ciphers
	expectedCiphers := intermediateProfileSpec.Ciphers
	sort.Strings(actualCiphers)
	sort.Strings(expectedCiphers)

	if !reflect.DeepEqual(actualCiphers, expectedCiphers) || !reflect.DeepEqual(intermediateProfileSpec.MinTLSVersion, ic.Status.TLSProfile.MinTLSVersion) {
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
			t.Logf("failed to get ingresscontroller %s: %v", name.Name, err)
			return false, nil
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
	t.Parallel()
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
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
	// use unique names for each test route to simplify debugging.
	route1Name := types.NamespacedName{Namespace: ns1.Name, Name: "route1"}
	route1 := makeRoute(route1Name, "routeadmission.test.example.com", "/foo")

	route2Name := types.NamespacedName{Namespace: ns2.Name, Name: "route2"}
	route2 := makeRoute(route2Name, "routeadmission.test.example.com", "/bar")

	admittedCondition := routev1.RouteIngressCondition{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}
	rejectedCondition := routev1.RouteIngressCondition{Type: routev1.RouteAdmitted, Status: corev1.ConditionFalse}

	// The first route should be admitted
	if err := kclient.Create(context.TODO(), route1); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	if err := waitForRouteIngressConditions(t, kclient, route1Name, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// sleep for a brief second to ensure that route1 and route2 _do not_
	// have the same creation timestamp. In theory, it's possible for route2
	// to be created < 1 second after route1 was created. If route1 and
	// route2 have the same creation timestamp, we cannot determine which
	// route will ultimately be rejected.
	time.Sleep(2 * time.Second)

	// The second route should be rejected because the policy is Strict
	if err := kclient.Create(context.TODO(), route2); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	if err := waitForRouteIngressConditions(t, kclient, route2Name, ic.Name, rejectedCondition); err != nil {
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
			t.Logf("failed to get deployment %s: %v", controller.RouterDeploymentName(ic), err)
			return false, nil
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
	if err := waitForRouteIngressConditions(t, kclient, route2Name, ic.Name, admittedCondition); err != nil {
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
	if err := waitForRouteIngressConditions(t, kclient, route3Name, ic.Name, admittedCondition); err != nil {
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
	if err := waitForRouteIngressConditions(t, kclient, route4Name, ic.Name, rejectedCondition); err != nil {
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
			t.Logf("failed to get deployment %s: %v", controller.RouterDeploymentName(ic), err)
			return false, nil
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
	if err := waitForRouteIngressConditions(t, kclient, route4Name, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
}

func TestSyslogLogging(t *testing.T) {
	t.Parallel()
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
	name := types.NamespacedName{Namespace: syslogPod.Namespace, Name: syslogPod.Name}
	err := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, syslogPod); err != nil {
			t.Logf("failed to get syslog pod %s/%s: %v", name.Namespace, name.Name, err)
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
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
	t.Parallel()
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
}

func TestIngressControllerCustomEndpoints(t *testing.T) {
	platform := infraConfig.Status.PlatformStatus
	if platform == nil {
		t.Fatalf("platform status is missing for infrastructure %s", infraConfig.Name)
	}
	switch platform.Type {
	case configv1.AWSPlatformType:
		switch {
		case platform.AWS == nil:
			t.Fatalf("aws platform status is missing for infrastructure %s", infraConfig.Name)
		case len(platform.AWS.ServiceEndpoints) != 0:
			t.Skipf("custom endpoints detected for infrastructure %s, skipping TestIngressControllerCustomEndpoints",
				infraConfig.Name)
		case len(platform.AWS.Region) == 0:
			t.Fatalf("region is missing from aws platform status for infrastructure %s", infraConfig.Name)
		case platform.AWS.Region == endpoints.CnNorth1RegionID || platform.AWS.Region == endpoints.CnNorthwest1RegionID:
			t.Skipf("region %s or %s detected for infrastructure %s, skipping TestIngressControllerCustomEndpoints",
				endpoints.CnNorth1RegionID, endpoints.CnNorthwest1RegionID, infraConfig.Name)
		}
		route53Endpoint := configv1.AWSServiceEndpoint{
			Name: "route53",
			// AWS Route 53 is a non-regionalized service, so the endpoint URL
			// does not include a region.
			URL: "https://route53.amazonaws.com",
		}
		taggingEndpoint := configv1.AWSServiceEndpoint{
			Name: "tagging",
			// us-east-1 region is required to get hosted zone resources
			// since route 53 is a non-regionalized service.
			URL: "https://tagging.us-east-1.amazonaws.com",
		}
		elbEndpoint := configv1.AWSServiceEndpoint{
			Name: "elasticloadbalancing",
			URL:  fmt.Sprintf("https://elasticloadbalancing.%s.amazonaws.com", platform.AWS.Region),
		}
		if err := updateInfrastructureConfigSpecWithRetryOnConflict(t, types.NamespacedName{Name: "cluster"}, timeout, func(spec *configv1.InfrastructureSpec) {
			spec.PlatformSpec.AWS = &configv1.AWSPlatformSpec{
				ServiceEndpoints: []configv1.AWSServiceEndpoint{
					route53Endpoint,
					taggingEndpoint,
					elbEndpoint,
				},
			}
		}); err != nil {
			t.Fatalf("failed to update infrastructure config: %v\n", err)
		}
		defer func() {
			// Remove the custom endpoints from the infrastructure config.
			if err := updateInfrastructureConfigSpecWithRetryOnConflict(t, types.NamespacedName{Name: "cluster"}, timeout, func(spec *configv1.InfrastructureSpec) {
				spec.PlatformSpec.AWS = nil
			}); err != nil {
				t.Fatalf("failed to update infrastructure config: %v", err)
			}
		}()
		// Wait for infrastructure status to update with custom endpoints.
		if err := wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
			if err := kclient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, &infraConfig); err != nil {
				t.Logf("failed to get infrastructure config: %v\n", err)
				return false, err
			}
			if len(infraConfig.Status.PlatformStatus.AWS.ServiceEndpoints) == 0 {
				return false, nil
			}
			return true, nil
		}); err != nil {
			t.Fatalf("failed to observe status update for infrastructure config %s", infraConfig.Name)
		}
	default:
		t.Skipf("skipping TestIngressControllerCustomEndpoints test due to platform type: %s", platform.Type)
	}
	// The default ingresscontroller should surface the expected status conditions.
	if err := waitForIngressControllerCondition(t, kclient, 30*time.Second, defaultName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

func TestHTTPHeaderCapture(t *testing.T) {
	t.Parallel()
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, conditions...); err != nil {
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
	if len(podList.Items) != 1 {
		var podNames []string
		for i := range podList.Items {
			podNames = append(podNames, podList.Items[i].Name)
		}
		t.Fatalf("expected ingress controller %s to have exactly 1 router pod, but it has %d: %s", ic.Name, len(podList.Items), strings.Join(podNames, ", "))
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
			if apierrors.IsNotFound(err) {
				return
			}
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
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		for _, pod := range podList.Items {
			readCloser, err := client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: "logs",
				Follow:    false,
			}).Stream(context.TODO())
			if err != nil {
				t.Errorf("failed to read logs from pod %s: %v", pod.Name, err)
				continue
			}
			data, _ := ioutil.ReadAll(readCloser)
			scanner := bufio.NewScanner(bytes.NewBuffer(data))
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
			if !found {
				t.Logf("failed to find output:\n\n%s", string(data))
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
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "cookiecapture"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newNodePortController(icName, domain)
	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type:      operatorv1.ContainerLoggingDestinationType,
				Container: &operatorv1.ContainerLoggingDestinationParameters{},
			},
			HTTPCaptureCookies: []operatorv1.IngressControllerCaptureHTTPCookie{{
				IngressControllerCaptureHTTPCookieUnion: operatorv1.IngressControllerCaptureHTTPCookieUnion{
					MatchType: "Exact",
					Name:      "foo",
				},
				MaxLength: 9,
			}},
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, conditions...); err != nil {
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
	if len(podList.Items) != 1 {
		t.Fatalf("expected ingress controller %s to have exactly 1 router pod, but it has %d", ic.Name, len(podList.Items))
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
			if apierrors.IsNotFound(err) {
				return
			}
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
	t.Parallel()
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
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
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

// TestAWSELBConnectionIdleTimeout verifies that the AWS ELB connection-idle
// timeout works as expected.
func TestAWSELBConnectionIdleTimeout(t *testing.T) {
	t.Parallel()
	if platform := infraConfig.Status.PlatformStatus.Type; platform != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", platform)
	}

	// Create an ingresscontroller that specifies an ELB with an idle
	// timeout of 3 seconds.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "test-idle-timeout"}
	ic := newLoadBalancerController(icName, icName.Name+"."+dnsConfig.Spec.BaseDomain)
	lb := &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.ExternalLoadBalancer,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Type: operatorv1.AWSClassicLoadBalancer,
				ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
					ConnectionIdleTimeout: metav1.Duration{Duration: 3 * time.Second},
				},
			},
		},
	}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = lb
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Create a pod with an HTTP application that sends delayed responses.
	httpdPod := buildSlowHTTPDPod("idle-timeout-httpd", operatorcontroller.DefaultOperandNamespace)
	if err := kclient.Create(context.TODO(), httpdPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", httpdPod.Namespace, httpdPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), httpdPod); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", httpdPod.Namespace, httpdPod.Name, err)
		}
	}()

	httpdService := buildEchoService(httpdPod.Name, httpdPod.Namespace, httpdPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), httpdService); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", httpdService.Namespace, httpdService.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), httpdService); err != nil {
			t.Fatalf("failed to delete service %s/%s: %v", httpdService.Namespace, httpdService.Name, err)
		}
	}()

	route := buildRoute(httpdPod.Name, httpdPod.Namespace, httpdService.Name)
	route.Spec.Host = fmt.Sprintf("%s-%s.%s", route.Name, route.Namespace, ic.Spec.Domain)
	if err := kclient.Create(context.TODO(), route); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", route.Namespace, route.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), route); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", route.Namespace, route.Name, err)
		}
	}()

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify that the ingresscontroller has the expected annotation.
	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	const key = "service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout"
	expected := "3"
	if v, ok := lbService.Annotations[key]; !ok {
		t.Fatalf("missing expected %s=%s annotation: %+v", key, expected, lbService.Annotations)
	} else if v != expected {
		t.Fatalf("expected %s=%s, found %s=%s", key, expected, key, v)
	}

	// Make sure we can resolve the route's host name.  It may take some
	// time for the ingresscontroller's wildcard DNS record to propagate.
	if err := wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
		_, err := net.LookupIP(route.Spec.Host)
		if err != nil {
			t.Log(err)
			return false, nil
		}

		return true, nil
	}); err != nil {
		t.Fatalf("failed to observe expected condition: %v", err)
	}

	// Open a connection to the route, send a request, and verify that the
	// connection times out after ~10 seconds.
	request, err := http.NewRequest("GET", "http://"+route.Spec.Host, nil)
	if err != nil {
		t.Fatalf("failed to create HTTP request: %v", err)
	}

	client := &http.Client{}

	if err := wait.PollImmediate(5*time.Second, 1*time.Minute, func() (bool, error) {
		start := time.Now()
		response, err := client.Do(request)
		if err != nil {
			elapsed := time.Now().Sub(start)

			// Ignore errors other than EOF.
			if !errors.Is(err, io.EOF) {
				t.Logf("got unexpected error after elapsed time %v: %v", elapsed, err)
				return false, nil
			}

			// Allow up to 15 seconds.  This is well above the
			// configured connection idle timeout, but it is also
			// well below the default idle timeout, so it should be
			// unlikely to cause false negatives or false positives.
			if elapsed.Seconds() > float64(15) {
				t.Logf("got expected EOF error after unexpectedly long elapsed time %v", elapsed)
				return false, nil
			}

			t.Logf("got expected EOF after elapsed time %v", elapsed)
			return true, nil
		} else {
			defer response.Body.Close()
			body, err := ioutil.ReadAll(response.Body)
			if err != nil {
				t.Log(err)
				return false, nil
			}

			elapsed := time.Now().Sub(start)
			t.Logf("got response after elapsed time %v: %v", elapsed, string(body))
		}

		return false, nil
	}); err != nil {
		t.Fatalf("failed to observe expected condition: %v", err)
	}

	// Configure the ingresscontroller with a longer ELB idle timeout of 120
	// seconds.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.ClassicLoadBalancerParameters.ConnectionIdleTimeout = metav1.Duration{Duration: 2 * time.Minute}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Verify that the ingresscontroller has the updated annotation.
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			t.Logf("failed to get LoadBalancer service: %v", err)

			return false, nil
		}

		expected := "120"
		if v, ok := lbService.Annotations[key]; !ok {
			t.Logf("missing expected %s=%s annotation: %+v", key, expected, lbService.Annotations)

			return false, nil
		} else if v != expected {
			t.Logf("expected %s=%s, found %s=%s", key, expected, key, v)

			return false, nil
		}

		t.Logf("found expected annotation %s=%s", key, expected)

		return true, nil
	}); err != nil {
		t.Fatalf("failed to observe expected condition: %v", err)
	}

	// Configure the route with a 90-second timeout.
	routeName := types.NamespacedName{Namespace: route.Namespace, Name: route.Name}
	if err := kclient.Get(context.TODO(), routeName, route); err != nil {
		t.Fatalf("failed to get route: %v", err)
	}
	if route.Annotations == nil {
		route.Annotations = map[string]string{}
	}
	route.Annotations["haproxy.router.openshift.io/timeout"] = "90s"
	if err := kclient.Update(context.TODO(), route); err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	// Open a connection to the route, send a request, and verify that a
	// response is received.  Use a polling loop because the ELB may need
	// time to assume the new idle timeout value.
	if err := wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
		start := time.Now()
		response, err := client.Do(request)
		if err != nil {
			elapsed := time.Now().Sub(start)
			t.Logf("got unexpected error after elapsed time %v: %v", elapsed, err)
			return false, nil
		}

		defer response.Body.Close()
		body, err := ioutil.ReadAll(response.Body)
		if err != nil {
			t.Log(err)
			return false, nil
		}

		elapsed := time.Now().Sub(start)
		t.Logf("got expected response after elapsed time %v: %v", elapsed, string(body))

		return true, nil
	}); err != nil {
		t.Fatalf("failed to observe expected condition: %v", err)
	}
}

func TestUniqueIdHeader(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "uniqueid"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		UniqueId: operatorv1.IngressControllerHTTPUniqueIdHeaderPolicy{Name: "x-unique-id"},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}

	service := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.InternalIngressControllerServiceName(ic), service); err != nil {
		t.Fatalf("failed to get ingresscontroller service: %v", err)
	}

	echoPod := buildEchoPod("unique-id-echo", deployment.Namespace)
	if err := kclient.Create(context.TODO(), echoPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoPod); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
		}
	}()

	echoService := buildEchoService(echoPod.Name, echoPod.Namespace, echoPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), echoService); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoService); err != nil {
			t.Fatalf("failed to delete service %s/%s: %v", echoService.Namespace, echoService.Name, err)
		}
	}()

	echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}
	uniqueHeaders := map[string]int{}
	const numRequests = 3
	for i := 1; i <= numRequests; i++ {
		name := fmt.Sprintf("unique-id-header-test-%d", i)
		image := deployment.Spec.Template.Spec.Containers[0].Image
		clientPod := buildCurlPod(name, echoRoute.Namespace, image, echoRoute.Spec.Host, service.Spec.ClusterIP)
		if err := kclient.Create(context.TODO(), clientPod); err != nil {
			t.Fatalf("failed to create pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
		}
		defer func() {
			if err := kclient.Delete(context.TODO(), clientPod); err != nil {
				if apierrors.IsNotFound(err) {
					return
				}
				t.Fatalf("failed to delete pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
			}
		}()

		err = wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
			readCloser, err := client.CoreV1().Pods(clientPod.Namespace).GetLogs(clientPod.Name, &corev1.PodLogOptions{
				Container: "curl",
				Follow:    false,
			}).Stream(context.TODO())
			if err != nil {
				t.Logf("failed to read output from pod %s: %v", clientPod.Name, err)
				return false, nil
			}
			scanner := bufio.NewScanner(readCloser)
			defer func() {
				if err := readCloser.Close(); err != nil {
					t.Errorf("failed to close reader for pod %s: %v", clientPod.Name, err)
				}
			}()
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "x-unique-id:") {
					t.Logf("found x-unique-id header from pod %s: %q", clientPod.Name, line)
					uniqueHeaders[line]++
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("failed to observe the expected log message: %v", err)
		}
	}
	if len(uniqueHeaders) != numRequests {
		t.Errorf("expected %d distinct x-unique-id headers, found %d", numRequests, len(uniqueHeaders))
	}
	for header, count := range uniqueHeaders {
		if count != 1 {
			t.Errorf("expected each x-unique-id header to be unique, found %d occurrences of %q", count, header)
		}
	}
}

// TestLoadBalancingAlgorithmUnsupportedConfigOverride verifies that the
// operator configures router pod replicas to use the "leastconn" load-balancing
// algorithm for non-passthrough routes if the ingresscontroller is so
// configured using an unsupported config override.  The test also verifies that
// the operator always configures router pod replicas to use the "source"
// algorithm for passthrough routes irrespective of the override.
func TestLoadBalancingAlgorithmUnsupportedConfigOverride(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "leastconn"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	expectedAlgorithm := "random"
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 30*time.Second, "ROUTER_LOAD_BALANCE_ALGORITHM", expectedAlgorithm); err != nil {
		t.Fatalf("expected initial deployment to have ROUTER_LOAD_BALANCE_ALGORITHM=%s: %v", expectedAlgorithm, err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 30*time.Second, "ROUTER_TCP_BALANCE_SCHEME", "source"); err != nil {
		t.Fatalf("expected initial deployment to have ROUTER_TCP_BALANCE_SCHEME=source: %v", err)
	}

	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"loadBalancingAlgorithm":"leastconn"}`),
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	expectedAlgorithm = "leastconn"
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_LOAD_BALANCE_ALGORITHM", expectedAlgorithm); err != nil {
		t.Fatalf("expected updated deployment to have ROUTER_LOAD_BALANCE_ALGORITHM=%s: %v", expectedAlgorithm, err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 30*time.Second, "ROUTER_TCP_BALANCE_SCHEME", "source"); err != nil {
		t.Fatalf("expected updated deployment to have ROUTER_TCP_BALANCE_SCHEME=source: %v", err)
	}
}

// TestDynamicConfigManagerUnsupportedConfigOverride verifies that the operator
// configures router pod replicas to use the dynamic config manager if the
// ingresscontroller is so configured using an unsupported config override.
func TestDynamicConfigManagerUnsupportedConfigOverride(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "dynamic-config-manager"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 30*time.Second, "ROUTER_HAPROXY_CONFIG_MANAGER", ""); err != nil {
		t.Fatalf("expected initial deployment not to set ROUTER_HAPROXY_CONFIG_MANAGER=true: %v", err)
	}

	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"dynamicConfigManager":"true"}`),
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_HAPROXY_CONFIG_MANAGER", "true"); err != nil {
		t.Fatalf("expected updated deployment to set ROUTER_HAPROXY_CONFIG_MANAGER=true: %v", err)
	}
}

// TestLocalWithFallbackOverrideForLoadBalancerService verifies that the
// operator does not set the local-with-fallback annotation on a LoadBalancer
// service if the the localWithFallback unsupported config override is set to
// "false".
//
// Note: This test mutates the default ingresscontroller rather than creating a
// new one to reduce the risk of failing due to cloud provider API throttling.
func TestLocalWithFallbackOverrideForLoadBalancerService(t *testing.T) {
	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AWSPlatformType:   {},
		configv1.AzurePlatformType: {},
		configv1.GCPPlatformType:   {},
	}
	platform := infraConfig.Status.Platform
	if _, supported := supportedPlatforms[platform]; !supported {
		t.Skipf("test skipped on platform %q", platform)
	}

	ic := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %q: %v", defaultName, err)
	}

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	service := &corev1.Service{}
	serviceName := controller.LoadBalancerServiceName(ic)
	if err := kclient.Get(context.TODO(), serviceName, service); err != nil {
		t.Fatalf("failed to get service %q: %v", serviceName, err)
	}

	const annotation = "traffic-policy.network.alpha.openshift.io/local-with-fallback"

	if _, ok := service.Annotations[annotation]; !ok {
		t.Fatalf("failed to observe the %q annotation on service %q", annotation, serviceName)
	}

	ic.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"localWithFallback":"false"}`),
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller %q with override: %v", defaultName, err)
	}
	defer func() {
		if err := updateIngressControllerSpecWithRetryOnConflict(t, defaultName, timeout, func(spec *operatorv1.IngressControllerSpec) {
			spec.UnsupportedConfigOverrides = runtime.RawExtension{}
		}); err != nil {
			t.Fatalf("failed to update ingresscontroller %q to remove the override: %v", defaultName, err)
		}
	}()

	wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), serviceName, service); err != nil {
			t.Logf("failed to get service %q: %v", serviceName, err)
			return false, nil
		}
		_, ok := service.Annotations[annotation]
		return !ok, nil
	})
	if _, ok := service.Annotations[annotation]; ok {
		t.Fatalf("failed to observe removal of the %q annotation on service %q", annotation, serviceName)
	}
}

// TestLocalWithFallbackOverrideForNodePortService verifies that the operator
// does not set the local-with-fallback annotation on a NodePort service if the
// the localWithFallback unsupported config override is set to "false".
func TestLocalWithFallbackOverrideForNodePortService(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      "local-with-fallback",
	}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newNodePortController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %q: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithNodePort...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	service := &corev1.Service{}
	serviceName := controller.NodePortServiceName(ic)
	if err := kclient.Get(context.TODO(), serviceName, service); err != nil {
		t.Fatalf("failed to get service %q: %v", serviceName, err)
	}

	const annotation = "traffic-policy.network.alpha.openshift.io/local-with-fallback"

	if _, ok := service.Annotations[annotation]; !ok {
		t.Fatalf("failed to observe the %q annotation on ingresscontroller %q", annotation, icName)
	}

	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %q: %v", icName, err)
	}
	ic.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"localWithFallback":"false"}`),
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller %q with override: %v", icName, err)
	}

	wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
		if err := kclient.Get(context.TODO(), serviceName, service); err != nil {
			t.Logf("failed to get service %q: %v", serviceName, err)
			return false, nil
		}
		_, ok := service.Annotations[annotation]
		return !ok, nil
	})
	if _, ok := service.Annotations[annotation]; ok {
		t.Fatalf("failed to observe removal of the %q annotation on service %q", annotation, serviceName)
	}
}

// TestReloadIntervalUnsupportedConfigOverride verifies that the operator
// configures router pod replicas with the specified value for RELOAD_INTERVAL
// if one is specified using an unsupported config override on the
// ingresscontroller.
func TestReloadIntervalUnsupportedConfigOverride(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "reload-interval"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 30*time.Second, "RELOAD_INTERVAL", "5s"); err != nil {
		t.Fatalf("expected initial deployment to set RELOAD_INTERVAL=5s: %v", err)
	}

	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"reloadInterval":60}`),
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "RELOAD_INTERVAL", "60s"); err != nil {
		t.Fatalf("expected updated deployment to set RELOAD_INTERVAL=60s: %v", err)
	}
}

// TestCustomErrorpages verifies that the custom error-pages API works properly,
// and that the error-page configmap controller properly synchs the operator's
// error-page configmap when it is deleted or when the user-provided configmap
// is updated.
func TestCustomErrorpages(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "errorpage"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	errorPageConfigmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-error-pages",
			Namespace: "openshift-config",
		},
		Data: map[string]string{
			"error-page-503.http": "HTTP/1.0 503 Service Unavailable\r\nPragma: no-cache\r\nCache-Control: private, max-age=0, no-cache, no-store\r\nConnection: close\r\nContent-Type: text/text\r\n\r\nNot found.\r\n",
		},
	}
	ic.Spec.HttpErrorCodePages.Name = errorPageConfigmap.Name
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %q: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	errorPageConfigmapName := types.NamespacedName{
		Name:      errorPageConfigmap.Name,
		Namespace: errorPageConfigmap.Namespace,
	}
	if err := kclient.Create(context.TODO(), errorPageConfigmap); err != nil {
		t.Fatalf("failed to create configmap %q: %v", errorPageConfigmapName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), errorPageConfigmap); err != nil {
			t.Fatalf("failed to delete configmap %q: %v", errorPageConfigmapName, err)
		}
	}()

	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// The controller should create a configmap in "openshift-ingress".
	cmName := controller.HttpErrorCodePageConfigMapName(ic)
	cm := &corev1.ConfigMap{}
	err := wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), cmName, cm); err != nil {
			t.Logf("failed to get configmap %q: %v", cmName, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe initial configmap %q: %v", cmName, err)
	}
	if actual, expected := cm.Data["error-page-503.http"], errorPageConfigmap.Data["error-page-503.http"]; actual != expected {
		t.Errorf("failed to observe expected data in configmap %q: expected %q, got %q", cmName, expected, actual)
	}

	// The deployment should use the custom error-page configmap.
	deploymentName := controller.RouterDeploymentName(ic)
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get deployment %q: %v", deploymentName, err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_ERRORFILE_503", "/var/lib/haproxy/conf/error_code_pages/error-page-503.http"); err != nil {
		t.Fatalf("expected deployment %q to use the custom error-page file: %v", deploymentName, err)
	}

	// The controller should recreate the configmap if it is deleted.
	if err := kclient.Delete(context.TODO(), cm); err != nil {
		t.Fatalf("failed to delete configmap %q: %v", cmName, err)
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), cmName, cm); err != nil {
			t.Logf("failed to get configmap %q: %v", cmName, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe recreated configmap %q: %v", cmName, err)
	}

	// The controller should update the its configmap if the user's changes.
	// This example is technically invalid
	errorPageConfigmap.Data["error-page-503.http"] = "HTTP/1.0 503 Service Unavailable\r\nPragma: no-cache\r\nCache-Control: private, max-age=0, no-cache, no-store\r\nConnection: close\r\nContent-Type: text/text\r\n\r\nNot found!\r\n"
	if err := kclient.Update(context.TODO(), errorPageConfigmap); err != nil {
		t.Fatalf("failed to update configmap %q: %v", errorPageConfigmapName, err)
	}
	err = wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), cmName, cm); err != nil {
			t.Logf("failed to get configmap %q: %v", cmName, err)
			return false, nil
		}
		if cm.Data["error-page-503.http"] != errorPageConfigmap.Data["error-page-503.http"] {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe update to configmap %q: %v\nexpected %q, got %q", cmName, err, errorPageConfigmap.Data["error-page-503.http"], cm.Data["error-page-503.http"])
	}
}

// TestTunableRouterKubeletProbesForCustomIngressController verifies that the
// operator allows changes to the kubelet probe timeouts for the router
// deployment associated with a custom ingresscontroller.
func TestTunableRouterKubeletProbesForCustomIngressController(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      "tunable-kubelet-probes",
	}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deploymentName := controller.RouterDeploymentName(ic)
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get default deployment: %v", err)
	}

	deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(2)
	deployment.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds = int32(11)
	deployment.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = int32(4)
	deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(2)
	deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds = int32(11)
	deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = int32(2)
	deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold = int32(4)
	deployment.Spec.Template.Spec.Containers[0].StartupProbe.TimeoutSeconds = int32(2)
	deployment.Spec.Template.Spec.Containers[0].StartupProbe.PeriodSeconds = int32(2)
	deployment.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold = int32(121)
	if err := kclient.Update(context.TODO(), deployment); err != nil {
		t.Fatalf("failed to update default deployment: %v", err)
	}

	if err := wait.PollImmediate(1*time.Second, time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
			t.Logf("error getting deployment %s: %v", deploymentName, err)

			return false, nil
		}

		container := deployment.Spec.Template.Spec.Containers[0]
		ok := true
		// Verify that all parameters *except* timeoutSeconds are reset.
		if e, a := probe(2, 10, 1, 3), container.LivenessProbe; !cmpProbes(e, a) {
			t.Logf("expected livenessProbe %v, got %v", e, a)
			ok = false
		}
		if e, a := probe(2, 10, 1, 3), container.ReadinessProbe; !cmpProbes(e, a) {
			t.Logf("expected readinessProbe %v, got %v", e, a)
			ok = false
		}
		if e, a := probe(2, 1, 1, 120), container.StartupProbe; !cmpProbes(e, a) {
			t.Logf("expected startupProbe %v, got %v", e, a)
			ok = false
		}

		if ok {
			t.Log("observed expected probes")
		}

		return ok, nil
	}); err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

// TestIngressControllerServiceNameCollision validates BZ2054200: Don't delete services that are not directly owned by this controller.
// It creates a service with the same naming convention as the ingress controller creates its own load balancing services.
// Then it triggers a reconcilation of the ingress operator to see if it will delete our service.
func TestIngressControllerServiceNameCollision(t *testing.T) {
	t.Parallel()
	// Create the new private controller that we will later create a service to collide with the naming scheme of this.
	icName := types.NamespacedName{
		Namespace: operatorNamespace,
		Name:      "e2e-name-collision-test",
	}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}

	// Clean up our new Ingress Controller after we are done.
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for new ingress controller to come online.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Create services that could collide with the new ingress controller's naming convention for loadbalancer and nodeport.
	// TRICKY: Our private ingress controller will reconcile and delete extra loadBalancerServices, even though the
	//         ingress controller isn't a loadbalancer type itself.
	conflictingLoadBalancerServiceName := types.NamespacedName{
		Name:      "router-" + icName.Name,
		Namespace: "openshift-ingress",
	}
	conflictingLoadBalancerService := buildEchoService(conflictingLoadBalancerServiceName.Name, conflictingLoadBalancerServiceName.Namespace, nil)
	if err := kclient.Create(context.TODO(), conflictingLoadBalancerService); err != nil {
		t.Fatalf("failed to create service %s: %v", conflictingLoadBalancerServiceName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), conflictingLoadBalancerService); err != nil {
			t.Fatalf("failed to delete service %s: %v", conflictingLoadBalancerServiceName, err)
		}
	}()

	conflictingNodeportServiceName := types.NamespacedName{
		Name:      "router-nodeport-" + icName.Name,
		Namespace: "openshift-ingress",
	}
	conflictingNodeportService := buildEchoService(conflictingNodeportServiceName.Name, conflictingNodeportServiceName.Namespace, nil)
	if err := kclient.Create(context.TODO(), conflictingNodeportService); err != nil {
		t.Fatalf("failed to create service %s: %v", conflictingNodeportServiceName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), conflictingNodeportService); err != nil {
			t.Fatalf("failed to delete service %s: %v", conflictingNodeportServiceName, err)
		}
	}()

	ic, err := getIngressController(t, kclient, icName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	// Annotate the ingress operator, to trigger a reconcilation to determine if our service is deleted.
	// This may not be needed, but it ensures a reconcilation occurs ASAP.
	if ic.Annotations == nil {
		ic.Annotations = map[string]string{}
	}
	ic.Annotations["ingress.operator.openshift.io/e2e-name-collision-test"] = ""

	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatal(err)
	}

	// Wait to see if our service gets deleted by the operator due to name collision.
	oldLoadBalancerUID := conflictingLoadBalancerService.UID
	oldNodePortUID := conflictingNodeportService.UID
	wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		// Check if LoadBalancer and Nodeport Service don't get deleted for the entire duration of this loop.
		// Will throw fatal error if deleted or marked for deletion and this loop stops.
		assertServiceNotDeleted(t, conflictingLoadBalancerServiceName, oldLoadBalancerUID)
		assertServiceNotDeleted(t, conflictingNodeportServiceName, oldNodePortUID)

		return false, nil
	})
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

// TestIngressOperatorCacheIsNotGlobal validates BZ2075671: Don't include all objects in all namespaces in the
// Ingress Operator's cache. This tests adds an Ingress Controller in another namespace that isn't in the
// Ingress Operator's cache and ensures the Ingress Operator ignores it.
func TestIngressOperatorCacheIsNotGlobal(t *testing.T) {
	t.Parallel()
	// Setup a namespace that will be ignored by the ingress operator.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ignored-ns",
		},
	}
	if err := kclient.Create(context.TODO(), ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), ns); err != nil {
			t.Fatalf("failed to delete namespace %v: %v", ns.Name, err)
		}
	}()
	// Create the new private controller in a namespace that will be ignored by the Ingress Operator.
	icName := types.NamespacedName{
		Namespace: ns.Name,
		Name:      "test-cache-is-not-global",
	}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}

	// Clean up our new Ingress Controller after we are done.
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Verify new Ingress Controller is ignored by ensuring its status never gets modified.
	wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), icName, ic); err != nil {
			t.Fatalf("failed to get ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
		}

		if len(ic.Status.Conditions) != 0 {
			t.Fatalf("expected ingress controller %s to be ignored by the ingress operator: conditions: %+v", icName, ic.Status.Conditions)
		}
		return false, nil
	})
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

func waitForAvailableReplicas(t *testing.T, cl client.Client, ic *operatorv1.IngressController, timeout time.Duration, expectedReplicas int32) error {
	ic = ic.DeepCopy()
	name := types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}
	var lastObservedReplicas int32
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ic); err != nil {
			t.Logf("failed to get ingresscontroller %s: %v", name.Name, err)
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
			t.Logf("error getting deployment %s/%s: %v", name.Namespace, name.Name, err)
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

// Wait for the provided deployment to have the specified environment variable
// set with the provided value, or unset if the provided value is the empty
// string.
func waitForDeploymentEnvVar(t *testing.T, cl client.Client, deployment *appsv1.Deployment, timeout time.Duration, name, value string) error {
	t.Helper()
	deploymentName := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
			t.Logf("error getting deployment %s/%s: %v", deploymentName.Namespace, deploymentName.Name, err)
			return false, nil
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == "router" {
				for _, v := range container.Env {
					if v.Name == name {
						return v.Value == value, nil
					}
				}
				return len(value) == 0, nil
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

func waitForClusterOperatorConditions(t *testing.T, cl client.Client, conditions ...configv1.ClusterOperatorStatusCondition) error {
	return wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		co := &configv1.ClusterOperator{}
		coName := controller.IngressClusterOperatorName()
		if err := cl.Get(context.TODO(), coName, co); err != nil {
			t.Logf("failed to get cluster operator %s: %v", coName.Name, err)
			return false, nil
		}

		expected := clusterOperatorConditionMap(conditions...)
		current := clusterOperatorConditionMap(co.Status.Conditions...)
		return conditionsMatchExpected(expected, current), nil
	})
}

func waitForRouteIngressConditions(t *testing.T, cl client.Client, routeName types.NamespacedName, routerName string, conditions ...routev1.RouteIngressCondition) error {
	return wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		route := &routev1.Route{}
		if err := cl.Get(context.TODO(), routeName, route); err != nil {
			t.Logf("failed to get route %s: %v", routeName.Name, err)
			return false, nil
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

// waitForIngressControllerCondition is a test helper that polls the specified
// ingresscontroller until its status reports the specified conditions.  If the
// polling loop reaches the timeout without observing the specified conditions,
// waitForIngressControllerCondition marks the test as failed and returns an
// error.  The caller can check the error value to stop execution of the test
// using Fatal or FailNow if appropriate.
func waitForIngressControllerCondition(t *testing.T, cl client.Client, timeout time.Duration, name types.NamespacedName, conditions ...operatorv1.OperatorCondition) error {
	t.Helper()

	ic := &operatorv1.IngressController{}
	expected := map[string]string{}
	current := map[string]string{}

	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ic); err != nil {
			t.Logf("failed to get ingresscontroller %s: %v", name.Name, err)
			return false, nil
		}
		expected = operatorConditionMap(conditions...)
		current = operatorConditionMap(ic.Status.Conditions...)

		return conditionsMatchExpected(expected, current), nil
	})

	if err != nil {
		t.Errorf("Expected conditions: %v\n Current conditions: %v", expected, current)
	}
	return err
}

func assertIngressControllerDeleted(t *testing.T, cl client.Client, ing *operatorv1.IngressController) {
	t.Helper()
	if err := deleteIngressController(t, cl, ing, 4*time.Minute); err != nil {
		if apierrors.IsNotFound(errors.Unwrap(err)) {
			return
		}
		t.Fatalf("WARNING: cloud resources may have been leaked! failed to delete ingresscontroller %s: %v", ing.Name, err)
	} else {
		t.Logf("deleted ingresscontroller %s", ing.Name)
	}
}

func deleteIngressController(t *testing.T, cl client.Client, ic *operatorv1.IngressController, timeout time.Duration) error {
	t.Helper()
	name := types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}
	if err := cl.Delete(context.TODO(), ic); err != nil {
		return fmt.Errorf("failed to delete ingresscontroller: %w", err)
	}

	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ic); err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("failed to delete ingress controller %s/%s: %v", ic.Namespace, ic.Name, err)
			return false, nil
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("timed out waiting for ingresscontroller to be deleted: %v", err)
	}
	return nil
}

// assertServiceNotDeleted asserts that a provide service wasn't deleted.
func assertServiceNotDeleted(t *testing.T, serviceName types.NamespacedName, oldUid types.UID) {
	t.Helper()

	// First check our LoadBalancer Service.
	service := &corev1.Service{}
	if err := kclient.Get(context.TODO(), serviceName, service); err != nil {
		t.Fatalf("expected %s to be present: %v", serviceName, err)
	}
	// If there is a DeletionTimestamp, it has been marked for deletion.
	if service.DeletionTimestamp != nil {
		t.Fatalf("expected service %s to not be marked for deletion: %v", serviceName, service.DeletionTimestamp)
	}
	// If the UID has changed, then the service has been recreated.
	if service.UID != oldUid {
		t.Fatalf("expected service %s to have UID %v, got %v", serviceName, oldUid, service.UID)
	}
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

	if err := cl.Delete(context.TODO(), secret); err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	return secret, cl.Create(context.TODO(), secret)
}
