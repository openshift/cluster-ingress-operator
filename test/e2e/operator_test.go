// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	iov1 "github.com/openshift/cluster-ingress-operator/pkg/api/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/apiserver/pkg/storage/names"
)

const (
	ingressControllerName = "test"
)

var (
	defaultAvailableConditions = []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionTrue},
	}
)

func getClient() (client.Client, string, error) {
	// Get a kube client.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return nil, manifests.DefaultOperatorNamespace, fmt.Errorf("failed to get kube config: %s", err)
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		return nil, manifests.DefaultOperatorNamespace, fmt.Errorf("failed to create kube client: %s", err)
	}
	return kubeClient, manifests.DefaultOperatorNamespace, nil
}

func newIngressController(name, ns, domain string, epType operatorv1.EndpointPublishingStrategyType) *operatorv1.IngressController {
	repl := int32(1)
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		// TODO: Test needs to be infrastructure and platform aware in the very near future.
		Spec: operatorv1.IngressControllerSpec{
			Domain:   domain,
			Replicas: &repl,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: epType,
			},
		},
	}
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
	co := &configv1.ClusterOperator{}
	return wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Name: ingresscontroller.IngressClusterOperatorName}, co); err != nil {
			return false, err
		}

		expected := clusterOperatorConditionMap(conditions...)
		current := clusterOperatorConditionMap(co.Status.Conditions...)
		return conditionsMatchExpected(expected, current), nil
	})
}

func waitForIngressControllerCondition(cl client.Client, timeout time.Duration, name types.NamespacedName, conditions ...operatorv1.OperatorCondition) error {
	ic := &operatorv1.IngressController{}
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ic); err != nil {
			return false, err
		}
		expected := operatorConditionMap(conditions...)
		current := operatorConditionMap(ic.Status.Conditions...)
		return conditionsMatchExpected(expected, current), nil
	})
}

func TestOperatorSteadyConditions(t *testing.T) {
	cl, _, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	expected := []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue},
	}
	err = waitForClusterOperatorConditions(cl, expected...)
	if err != nil {
		t.Errorf("did not get expected available condition: %v", err)
	}
}

func TestDefaultIngressControllerSteadyConditions(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	name := types.NamespacedName{Namespace: ns, Name: manifests.DefaultIngressControllerName}
	err = waitForIngressControllerCondition(cl, 10*time.Second, name, defaultAvailableConditions...)
	if err != nil {
		t.Errorf("did not get expected condition: %v", err)
	}
}

func TestUserDefinedIngressController(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	dnsConfig := &configv1.DNS{}
	if err := cl.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		t.Fatalf("failed to get DNS 'cluster': %v", err)
	}

	name := types.NamespacedName{Namespace: ns, Name: ingressControllerName}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ing := newIngressController(name.Name, name.Namespace, domain, operatorv1.LoadBalancerServiceStrategyType)
	if err := cl.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create ingresscontroller %s/%s: %v", name.Namespace, name.Name, err)
	}

	err = waitForIngressControllerCondition(cl, 5*time.Minute, name, defaultAvailableConditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	if err := cl.Delete(context.TODO(), ing); err != nil {
		t.Fatalf("WARNING: cloud resources may have been leaked! failed to delete ingresscontroller %s: %v", name, err)
	}

	err = wait.PollImmediate(1*time.Second, 2*time.Minute, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ing); err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("WARNING: cloud resources may have been leaked! failed to delete ingresscontroller %s: %v", name, err)
	}
}

func TestClusterProxyProtocol(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	infraConfig := &configv1.Infrastructure{}
	err = cl.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig)
	if err != nil {
		t.Fatalf("failed to get infrastructure config: %v", err)
	}

	if infraConfig.Status.Platform != configv1.AWSPlatformType {
		t.Skip("test skipped on non-aws platform")
		return
	}

	ic := &operatorv1.IngressController{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: manifests.DefaultIngressControllerName}, ic); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}

	// Wait for the router deployment to exist.
	deployment := &appsv1.Deployment{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), ingresscontroller.RouterDeploymentName(ic), deployment); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default ingresscontroller deployment: %v", err)
	}

	// Ensure proxy protocol is enabled on the router deployment.
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
		t.Fatalf("expected router deployment to enable the PROXY protocol")
	}

	// Wait for the internal router service to exist.
	internalService := &corev1.Service{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), ingresscontroller.InternalIngressControllerServiceName(ic), internalService); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get internal ingresscontroller service: %v", err)
	}
}

// NOTE: This test will mutate the default ingresscontroller.
//
// TODO: Find a way to do this test without mutating the default ingress?
func TestUpdateDefaultIngressController(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the ingress controller to be available.
	name := types.NamespacedName{Namespace: ns, Name: manifests.DefaultIngressControllerName}
	err = waitForIngressControllerCondition(cl, 10*time.Second, name, defaultAvailableConditions...)
	if err != nil {
		t.Errorf("did not get expected condition: %v", err)
	}
	ic := &operatorv1.IngressController{}
	if err := cl.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}

	// Wait for the CA certificate configmap to exist.
	configmap := &corev1.ConfigMap{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), ingresscontroller.RouterCAConfigMapName(), configmap); err != nil {
			if !errors.IsNotFound(err) {
				t.Logf("failed to get CA certificate configmap, will retry: %v", err)
			}
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get CA certificate configmap: %v", err)
	}

	// Verify that the deployment uses the secret name specified in the
	// ingress controller, or the default if none is set, and store the
	// secret name (if any) so we can reset it at the end of the test.
	deployment := &appsv1.Deployment{}
	if err := cl.Get(context.TODO(), ingresscontroller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get default router deployment: %v", err)
	}
	originalSecret := ic.Spec.DefaultCertificate.DeepCopy()
	expectedSecretName := ingresscontroller.RouterOperatorGeneratedDefaultCertificateSecretName(ic, deployment.Namespace).Name
	if originalSecret != nil {
		expectedSecretName = originalSecret.Name
	}
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != expectedSecretName {
		t.Fatalf("expected router deployment certificate secret to be %s, got %s",
			expectedSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	// Update the ingress controller and wait for the deployment to match.
	secret, err := createDefaultCertTestSecret(cl, names.SimpleNameGenerator.GenerateName("test-"))
	if err != nil {
		t.Fatalf("creating default cert test secret: %v", err)
	}
	defer func() {
		if err := cl.Delete(context.TODO(), secret); err != nil {
			t.Errorf("failed to delete test secret: %v", err)
		}
	}()

	ic.Spec.DefaultCertificate = &corev1.LocalObjectReference{Name: secret.Name}
	if err := cl.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update default IngressController: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, deployment); err != nil {
			return false, nil
		}
		if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != secret.Name {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe updated router deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
	}

	// Wait for the CA certificate configmap to be deleted.
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), ingresscontroller.RouterCAConfigMapName(), configmap); err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			t.Logf("failed to get CA certificate configmap, will retry: %v", err)
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe clean-up of CA certificate configmap: %v", err)
	}

	// Reset .spec.defaultCertificate to its original value.
	if err := cl.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}
	ic.Spec.DefaultCertificate = originalSecret
	if err := cl.Update(context.TODO(), ic); err != nil {
		t.Errorf("failed to reset default ingresscontroller: %v", err)
	}

	// Wait for the CA certificate configmap to be recreated.
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ingresscontroller.GlobalMachineSpecifiedConfigNamespace, Name: "router-ca"}, configmap); err != nil {
			if !errors.IsNotFound(err) {
				t.Logf("failed to get CA certificate configmap, will retry: %v", err)
			}
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get recreated CA certificate configmap: %v", err)
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

	if err := cl.Delete(context.TODO(), secret); err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	return secret, cl.Create(context.TODO(), secret)
}

// TestIngressControllerScale exercises a simple scale up/down scenario. Note that
// the scaling client isn't yet reliable because of issues with CRD scale
// subresource handling upstream (e.g. a persisted nil .spec.replicas will break
// GET /scale). For now, only support scaling through direct update to
// ingresscontroller.spec.replicas.
//
// See also: https://github.com/kubernetes/kubernetes/pull/75210
func TestIngressControllerScale(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	name := types.NamespacedName{Namespace: ns, Name: manifests.DefaultIngressControllerName}
	err = waitForIngressControllerCondition(cl, 15*time.Second, name, defaultAvailableConditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	ic := &operatorv1.IngressController{}
	if err := cl.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}

	deploymentName := ingresscontroller.RouterDeploymentName(ic)
	deployment := &appsv1.Deployment{}
	if err := cl.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get deployment %s: %v", deploymentName, err)
	}

	originalReplicas := *deployment.Spec.Replicas
	newReplicas := originalReplicas + 1

	if err := cl.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	ic.Spec.Replicas = &newReplicas
	if err := cl.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for the deployment scale up to be observed.
	err = waitForAvailableReplicas(cl, name, 2*time.Minute, newReplicas)
	if err != nil {
		t.Fatalf("failed waiting deployment %s to scale to %d: %v", deploymentName, newReplicas, err)
	}

	// Ensure the ingresscontroller remains available
	err = waitForIngressControllerCondition(cl, 2*time.Minute, name, defaultAvailableConditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	// Scale back down.
	if err := cl.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	ic.Spec.Replicas = &originalReplicas
	if err := cl.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for the deployment scale down to be observed.
	err = waitForAvailableReplicas(cl, name, 2*time.Minute, originalReplicas)
	if err != nil {
		t.Fatalf("failed waiting deployment %s to scale to %d: %v", deploymentName, originalReplicas, err)
	}

	// Ensure the ingresscontroller remains available
	err = waitForIngressControllerCondition(cl, 2*time.Minute, name, defaultAvailableConditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}
}

func waitForAvailableReplicas(cl client.Client, name types.NamespacedName, timeout time.Duration, expectedReplicas int32) error {
	ic := &operatorv1.IngressController{}
	var lastObservedReplicas int32
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ic); err != nil {
			return false, nil
		}
		lastObservedReplicas = ic.Status.AvailableReplicas
		if lastObservedReplicas != expectedReplicas {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to achieve expected replicas: last observed : %v", lastObservedReplicas)
	}
	return nil
}

func TestRouterCACertificate(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	name := types.NamespacedName{Namespace: ns, Name: manifests.DefaultIngressControllerName}
	ic := &operatorv1.IngressController{}
	if err := cl.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}

	if ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		t.Skip("test skipped for non-cloud HA type")
		return
	}

	err = waitForIngressControllerCondition(cl, 15*time.Second, name, defaultAvailableConditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	var certData []byte
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		cm := &corev1.ConfigMap{}
		err := cl.Get(context.TODO(), ingresscontroller.RouterCAConfigMapName(), cm)
		if err != nil {
			return false, nil
		}
		if val, ok := cm.Data["ca-bundle.crt"]; !ok {
			return false, fmt.Errorf("router-ca secret is missing %q", "ca-bundle.crt")
		} else {
			certData = []byte(val)
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get CA certificate: %v", err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(certData) {
		t.Fatalf("failed to parse CA certificate")
	}

	wildcardRecordName := ingresscontroller.WildcardDNSRecordName(ic)
	wildcardRecord := &iov1.DNSRecord{}
	err = cl.Get(context.TODO(), wildcardRecordName, wildcardRecord)
	if err != nil {
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

// TestHostNetworkEndpointPublishingStrategy creates an ingresscontroller with
// the "HostNetwork" endpoint publishing strategy type and verifies that the
// operator creates a router and that the router becomes available.
func TestHostNetworkEndpointPublishingStrategy(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	dnsConfig := &configv1.DNS{}
	if err := cl.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		t.Fatalf("failed to get DNS 'cluster': %v", err)
	}

	name := types.NamespacedName{Namespace: ns, Name: ingressControllerName}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ing := newIngressController(name.Name, name.Namespace, domain, operatorv1.HostNetworkStrategyType)
	if err := cl.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create ingresscontroller %s/%s: %v", name.Namespace, name.Name, err)
	}

	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	err = waitForIngressControllerCondition(cl, 5*time.Minute, name, conditions...)
	if err != nil {
		t.Errorf("failed to observe expected conditions: %v", err)
	}

	if err := cl.Delete(context.TODO(), ing); err != nil {
		t.Fatalf("WARNING: cloud resources may have been leaked! failed to delete ingresscontroller %s: %v", name, err)
	}

	err = wait.PollImmediate(1*time.Second, 2*time.Minute, func() (bool, error) {
		if err := cl.Get(context.TODO(), name, ing); err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("WARNING: cloud resources may have been leaked! failed to delete ingresscontroller %s: %v", name, err)
	}
}
