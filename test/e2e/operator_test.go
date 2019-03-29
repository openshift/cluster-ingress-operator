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
	"os"
	"strconv"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	operator "github.com/openshift/cluster-ingress-operator/pkg/operator"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/discovery"
	discocache "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/scale"
)

const (
	ingressControllerName = "test"
)

type testClient struct {
	client.Client
}

func (c *testClient) RetryGet(ctx context.Context, key client.ObjectKey, obj runtime.Object, timeout time.Duration) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := c.Client.Get(context.TODO(), key, obj); err == nil {
			return true, nil
		} else {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
	})
}

func (c *testClient) WaitForNotFound(ctx context.Context, key client.ObjectKey, obj runtime.Object, timeout time.Duration) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := c.Client.Get(context.TODO(), key, obj); err == nil {
			return false, nil
		} else {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
	})
}

func getClient() (*testClient, string, error) {
	namespace, ok := os.LookupEnv("WATCH_NAMESPACE")
	if !ok {
		return nil, "", fmt.Errorf("WATCH_NAMESPACE environment variable is required")
	}
	// Get a kube client.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get kube config: %s", err)
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create kube client: %s", err)
	}
	return &testClient{kubeClient}, namespace, nil
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

func ingressControllerAvailable(ic *operatorv1.IngressController) bool {
	for _, cond := range ic.Status.Conditions {
		if cond.Type == operatorv1.OperatorStatusTypeAvailable && cond.Status == operatorv1.ConditionTrue {
			return true
		}
	}
	return false
}

func waitForIngressControllerAvailable(cl client.Client, ic *operatorv1.IngressController, timeout time.Duration) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{ic.Namespace, ic.Name}, ic); err != nil {
			return false, err
		}
		return ingressControllerAvailable(ic), nil
	})
}

func waitForEndpointPublishingStrategyStatus(cl client.Client, ic *operatorv1.IngressController, expected operatorv1.EndpointPublishingStrategyType, timeout time.Duration) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{ic.Namespace, ic.Name}, ic); err != nil {
			return false, nil
		}
		strategy := ic.Status.EndpointPublishingStrategy
		switch {
		case strategy == nil:
			return false, nil
		case strategy.Type == expected:
			return true, nil
		default:
			return false, nil
		}
	})
}

func TestOperatorAvailable(t *testing.T) {
	cl, _, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	co := &configv1.ClusterOperator{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Name: ingresscontroller.IngressClusterOperatorName}, co); err != nil {
			return false, nil
		}

		for _, cond := range co.Status.Conditions {
			if cond.Type == configv1.OperatorAvailable &&
				cond.Status == configv1.ConditionTrue {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Errorf("did not get expected available condition: %v", err)
	}
}

// TODO: Use manifest factory to build the expectation
func TestDefaultClusterIngressExists(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	ci := &operatorv1.IngressController{}
	err = cl.RetryGet(context.TODO(), types.NamespacedName{Namespace: ns, Name: operator.DefaultIngressController}, ci, 10*time.Second)
	if err != nil {
		t.Errorf("failed to get default ClusterIngress: %v", err)
	}
}

func TestClusterIngressControllerCreateDelete(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	ing := &operatorv1.IngressController{}
	if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: operator.DefaultIngressController}, ing); err != nil {
		t.Fatalf("failed to get default IngressController: %v", err)
	}

	dnsConfig := &configv1.DNS{}
	if err := cl.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		t.Fatalf("failed to get DNS 'cluster': %v", err)
	}

	domain := ingressControllerName + "." + dnsConfig.Spec.BaseDomain
	ing = newIngressController(ingressControllerName, ns, domain, operatorv1.LoadBalancerServiceStrategyType)
	if err := cl.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create ingresscontroller %s/%s: %v", ing.Namespace, ing.Name, err)
	}

	// Verify the ingress controller deployment created the specified
	// number of pod replicas.
	err = waitForIngressControllerAvailable(cl, ing, 60*time.Second)
	if err != nil {
		t.Errorf("ingresscontroller %s/%s failed to become available: %v", ing.Namespace, ing.Name, err)
	}

	// Change the publishing strategy type to private
	ing.Spec.EndpointPublishingStrategy.Type = operatorv1.PrivateStrategyType
	err = cl.Update(context.TODO(), ing)
	if err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for status to reflect the strategy change
	err = waitForEndpointPublishingStrategyStatus(cl, ing, operatorv1.PrivateStrategyType, 60*time.Second)
	if err != nil {
		t.Errorf("timed out waiting for ingresscontroller status to reflect private endpoint publishing strategy: %v", err)
	}

	// Wait for the LB service to be deleted
	svc := &corev1.Service{}
	err = cl.WaitForNotFound(context.TODO(), ingresscontroller.LoadBalancerServiceName(ing), svc, 60*time.Second)
	if err != nil {
		t.Errorf("timed out waiting for loadbalancer service to be deleted: %v", err)
	}

	// Change the publishing strategy type back to LB
	ing.Spec.EndpointPublishingStrategy.Type = operatorv1.LoadBalancerServiceStrategyType
	err = cl.Update(context.TODO(), ing)
	if err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for status to reflect the strategy change
	err = waitForEndpointPublishingStrategyStatus(cl, ing, operatorv1.LoadBalancerServiceStrategyType, 60*time.Second)
	if err != nil {
		t.Errorf("timed out waiting for ingresscontroller status to reflect LB endpoint publishing strategy: %v", err)
	}

	// Wait for the LB service to reappear
	err = cl.RetryGet(context.TODO(), ingresscontroller.LoadBalancerServiceName(ing), svc, 60*time.Second)
	if err != nil {
		t.Errorf("timed out waiting for loadbalancer service to be created: %v", err)
	}

	// Delete the ingresscontroller and wait for it to be finalized
	if err := cl.Delete(context.TODO(), ing); err != nil {
		t.Fatalf("failed to delete ingresscontroller %s/%s: %v", ing.Namespace, ing.Name, err)
	}

	err = cl.WaitForNotFound(context.TODO(), types.NamespacedName{ing.Namespace, ing.Name}, ing, 60*time.Second)
	if err != nil {
		t.Fatalf("failed to finalize IngressController %s/%s: %v", ing.Namespace, ing.Name, err)
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

	if infraConfig.Status.Platform != configv1.AWSPlatform {
		t.Skip("test skipped on non-aws platform")
		return
	}

	ic := &operatorv1.IngressController{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "default"}, ic); err != nil {
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

	// TODO: Wait for interal router service selector bug to be fixed.
	// An alternative to test this would be to use an actual proxy protocol
	// request to the internal router service.
	// import "net"
	// connection, err := net.Dial("tcp", internalService.Spec.ClusterIP)
	// if err != nil {
	//	t.Fatalf("failed to connect to internal router service: %v", err)
	// }
	// defer connection.Close()

	// req := []byte("LOCAL\r\nGET / HTTP/1.1\r\nHost: non.existent.test\r\n\r\n")
	// req = []byte(fmt.Sprintf("PROXY TCP4 10.9.8.7 %s 54321 443\r\nGET / HTTP/1.1\r\nHost: non.existent.test\r\n\r\n", internalService.Spec.ClusterIP))
	// connection.Write(req)
	// data := make([]byte, 4096)
	// if _, err := connection.Read(data); err != nil {
	// 	t.Fatalf("failed to read response from internal router service: %v", err)
	// } else {
	// 	check response is a http response 503.
	// }

}

// TODO: Use manifest factory to build expectations
// TODO: Find a way to do this test without mutating the default ingress?
func TestClusterIngressUpdate(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	ci := &operatorv1.IngressController{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: operator.DefaultIngressController}, ci); err != nil {
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
		if err := cl.Get(context.TODO(), ingresscontroller.RouterDeploymentName(ci), deployment); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default ingresscontroller deployment: %v", err)
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

	originalSecret := ci.Spec.DefaultCertificate.DeepCopy()
	expectedSecretName := ingresscontroller.RouterOperatorGeneratedDefaultCertificateSecretName(ci, "openshift-ingress")
	if originalSecret != nil {
		expectedSecretName = types.NamespacedName{Namespace: "openshift-ingress", Name: originalSecret.Name}
	}

	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != expectedSecretName.Name {
		t.Fatalf("expected deployment certificate secret to be %s, got %s",
			expectedSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	secret, err := createDefaultCertTestSecret(cl, names.SimpleNameGenerator.GenerateName("test-"))
	if err != nil {
		t.Fatalf("creating default cert test secret: %v", err)
	}

	// update the ci and wait for the updated deployment to match expectations
	ci.Spec.DefaultCertificate = &corev1.LocalObjectReference{Name: secret.Name}
	err = cl.Update(context.TODO(), ci)
	if err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
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
		t.Fatalf("failed to get updated ingresscontroller deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
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

	// Reset the original secret
	ci.Spec.DefaultCertificate = originalSecret
	err = cl.Update(context.TODO(), ci)
	if err != nil {
		t.Errorf("failed to update ingresscontroller: %v", err)
	}

	// Wait for the CA certificate configmap to be recreated.
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
		t.Fatalf("failed to get recreated CA certificate configmap: %v", err)
	}

	err = cl.Delete(context.TODO(), secret)
	if err != nil {
		t.Errorf("failed to delete test secret: %v", err)
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

// TestClusterIngressScale exercises a simple scale up/down scenario. Note that
// the scaling client isn't yet reliable because of issues with CRD scale
// subresource handling upstream (e.g. a persisted nil .spec.replicas will break
// GET /scale). For now, only support scaling through direct update to
// ingresscontroller.spec.replicas.
//
// See also: https://github.com/kubernetes/kubernetes/pull/75210
func TestClusterIngressScale(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	name := "default"

	// Wait for the clusteringress to exist.
	ci := &operatorv1.IngressController{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: name}, ci); err != nil {
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
		if err := cl.Get(context.TODO(), ingresscontroller.RouterDeploymentName(ci), deployment); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default deployment: %v", err)
	}

	originalReplicas := *deployment.Spec.Replicas
	newReplicas := originalReplicas + 1

	ci.Spec.Replicas = &newReplicas
	if err := cl.Update(context.TODO(), ci); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for the deployment to scale up.
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, deployment); err != nil {
			return false, nil
		}

		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != newReplicas {
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get scaled-up deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
	}

	// Scale back down.
	ci.Spec.Replicas = &originalReplicas
	if err := cl.Update(context.TODO(), ci); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for the deployment to scale down.
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, deployment); err != nil {
			return false, nil
		}

		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != originalReplicas {
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get scaled-down deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
	}
}

func getScaleClient() (scale.ScalesGetter, discovery.CachedDiscoveryInterface, error) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get kube config: %v", err)
	}

	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create kube client: %v", err)
	}

	cachedDiscovery := discocache.NewMemCacheClient(client.Discovery())
	cachedDiscovery.Invalidate()
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscovery)
	restMapper.Reset()
	scaleKindResolver := scale.NewDiscoveryScaleKindResolver(client.Discovery())
	scale, err := scale.NewForConfig(kubeConfig, restMapper, dynamic.LegacyAPIPathResolverFunc, scaleKindResolver)

	return scale, cachedDiscovery, err
}

func TestRouterCACertificate(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	ci := &operatorv1.IngressController{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "default"}, ci); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default ClusterIngress: %v", err)
	}
	if len(ci.Status.Domain) == 0 {
		t.Fatal("default ClusterIngress has no .status.ingressDomain")
	}

	if ci.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		t.Skip("test skipped for non-cloud HA type")
		return
	}

	var certData []byte
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		cm := &corev1.ConfigMap{}
		err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ingresscontroller.GlobalMachineSpecifiedConfigNamespace, Name: "router-ca"}, cm)
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

	var host string
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		svc := &corev1.Service{}
		err := cl.Get(context.TODO(), types.NamespacedName{Namespace: "openshift-ingress", Name: "router-" + ci.Name}, svc)
		if err != nil {
			return false, nil
		}
		if len(svc.Status.LoadBalancer.Ingress) == 0 || len(svc.Status.LoadBalancer.Ingress[0].Hostname) == 0 {
			return false, nil
		}
		host = svc.Status.LoadBalancer.Ingress[0].Hostname
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get router service: %v", err)
	}

	// Make sure we can connect without getting a "certificate signed by
	// unknown authority" or "x509: certificate is valid for [...], not
	// [...]" error.
	serverName := "test." + ci.Status.Domain
	address := net.JoinHostPort(host, "443")
	conn, err := tls.Dial("tcp", address, &tls.Config{
		RootCAs:    certPool,
		ServerName: serverName,
	})
	if err != nil {
		t.Fatalf("failed to connect to router at %s: %v", address, err)
	}
	defer conn.Close()

	conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))

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

	// Create the ingresscontroller.
	domain := ingressControllerName + "." + dnsConfig.Spec.BaseDomain
	ing := newIngressController(ingressControllerName, ns, domain, operatorv1.HostNetworkStrategyType)
	if cl.Create(context.TODO(), ing); err != nil {
		t.Fatalf("failed to create the ingresscontroller: %v", err)
	}

	// Wait for the deployment to exist and be available.
	err = wait.PollImmediate(1*time.Second, 60*time.Second, func() (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := cl.Get(context.TODO(), ingresscontroller.RouterDeploymentName(ing), deployment); err != nil {
			return false, nil
		}
		if ing.Spec.Replicas == nil || deployment.Status.AvailableReplicas != *ing.Spec.Replicas {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		// Use Errorf rather than Fatalf so that we continue on to
		// delete the ingresscontroller.
		t.Errorf("failed to get deployment: %v", err)
	}

	if cl.Delete(context.TODO(), ing); err != nil {
		t.Fatalf("failed to delete the ingresscontroller: %v", err)
	}
}
