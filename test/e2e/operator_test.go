// +build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	discocache "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/scale"
)

func getClient() (client.Client, string, error) {
	namespace, ok := os.LookupEnv("WATCH_NAMESPACE")
	if !ok {
		return nil, "", fmt.Errorf("WATCH_NAMESPACE environment variable is required")
	}
	// Get a kube client.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get kube config: %s", err)
	}
	kubeClient, err := operator.Client(kubeConfig)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create kube client: %s", err)
	}
	return kubeClient, namespace, nil
}

func TestOperatorAvailable(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	co := &configv1.ClusterOperator{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Name: ns}, co); err != nil {
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

	ci := &ingressv1alpha1.ClusterIngress{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "default"}, ci); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Errorf("failed to get default ClusterIngress: %v", err)
	}
}

func TestRouterServiceInternalEndpoints(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	ci := &ingressv1alpha1.ClusterIngress{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "default"}, ci); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default ClusterIngress: %v", err)
	}

	// Wait for the router deployment to exist.
	deployment := &appsv1.Deployment{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: "openshift-ingress", Name: fmt.Sprintf("router-%s", ci.Name)}, deployment); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default router deployment: %v", err)
	}

	// check if service exists and has endpoints
	svc := &corev1.Service{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: "openshift-ingress", Name: fmt.Sprintf("router-internal-%s", ci.Name)}, svc); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get internal router service: %v", err)
	}

	endpoints := &corev1.Endpoints{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: "openshift-ingress", Name: fmt.Sprintf("router-internal-%s", ci.Name)}, endpoints); err != nil {
			return false, nil
		}

		nready := 0
		for i := range endpoints.Subsets {
			es := &endpoints.Subsets[i]
			nready = nready + len(es.Addresses)
		}
		if nready > 0 {
			return true, nil
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to get internal router service endpoints: %v", err)
	}
}

// TODO: Use manifest factory to build expectations
// TODO: Find a way to do this test without mutating the default ingress?
func TestClusterIngressUpdate(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	ci := &ingressv1alpha1.ClusterIngress{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "default"}, ci); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default ClusterIngress: %v", err)
	}

	// Wait for the router deployment to exist.
	deployment := &appsv1.Deployment{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: "openshift-ingress", Name: fmt.Sprintf("router-%s", ci.Name)}, deployment); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default router deployment: %v", err)
	}

	// update router namespace with a label.
	routerNS := &corev1.Namespace{}
	if err := cl.Get(context.TODO(), types.NamespacedName{Name: "openshift-ingress"}, routerNS); err != nil {
		t.Fatalf("failed to get default router namespace: %v", err)
	}

	if routerNS.Labels == nil {
		routerNS.Labels = map[string]string{}
	}

	e2eLabel := fmt.Sprintf("e2e-label-%v", time.Now().UnixNano())
	routerNS.Labels[e2eLabel] = "e2e-test"

	err = cl.Update(context.TODO(), routerNS)
	if err != nil {
		t.Fatalf("failed to update router namespace with e2e test label %s: %v", e2eLabel, err)
	}

	originalSecret := ci.Spec.DefaultCertificateSecret
	expectedSecretName := fmt.Sprintf("router-certs-%s", ci.Name)
	if originalSecret != nil {
		expectedSecretName = *originalSecret
	}

	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != expectedSecretName {
		t.Fatalf("expected router deployment certificate secret to be %s, got %s",
			expectedSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	err, secret := createDefaultCertTestSecret(cl)
	if err != nil || secret == nil {
		t.Fatalf("creating default cert test secret: %v", err)
	}

	// update the ci and wait for the updated deployment to match expectations
	ci.Spec.DefaultCertificateSecret = &secret.Name
	err = cl.Update(context.TODO(), ci)
	if err != nil {
		t.Fatalf("failed to get default ClusterIngress: %v", err)
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
		t.Fatalf("failed to get updated router deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
	}

	updatedNamespace := &corev1.Namespace{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Name: "openshift-ingress"}, updatedNamespace); err != nil {
			return false, err
		}
		if _, ok := updatedNamespace.Labels[e2eLabel]; ok {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected router namespace to be updated without the e2e test label %s: %v", e2eLabel, err)
	}

	ci.Spec.DefaultCertificateSecret = originalSecret
	err = cl.Update(context.TODO(), ci)
	if err != nil {
		t.Errorf("failed to reset ClusterIngress: %v", err)
	}

	err = cl.Delete(context.TODO(), secret)
	if err != nil {
		t.Errorf("failed to delete test secret: %v", err)
	}
}

func createDefaultCertTestSecret(cl client.Client) (error, *corev1.Secret) {
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
			Name:      "e2e-test-secret",
			Namespace: "openshift-ingress",
		},
		Data: map[string][]byte{
			"tls.crt": []byte(defaultCert),
			"tls.key": []byte(defaultKey),
		},
		Type: corev1.SecretTypeTLS,
	}

	if err := cl.Delete(context.TODO(), secret); err != nil && !errors.IsNotFound(err) {
		return err, nil
	}

	return cl.Create(context.TODO(), secret), secret
}

func TestClusterIngressScale(t *testing.T) {
	cl, ns, err := getClient()
	if err != nil {
		t.Fatal(err)
	}

	name := "default"

	// Wait for the clusteringress to exist.
	ci := &ingressv1alpha1.ClusterIngress{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: name}, ci); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default ClusterIngress: %v", err)
	}

	// Wait for the router deployment to exist.
	deployment := &appsv1.Deployment{}
	err = wait.PollImmediate(1*time.Second, 10*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: "openshift-ingress", Name: "router-" + ci.Name}, deployment); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get default router deployment: %v", err)
	}

	// Get the deployment's selector so we can make sure it is reflected in
	// the scale status.
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		t.Fatalf("router deployment has invalid spec.selector: %v", err)
	}

	// Get the scale of the clusteringress.
	// TODO Use controller-runtime once it supports the /scale subresource.
	scaleClient, cachedDiscovery, err := getScaleClient()
	if err != nil {
		t.Fatal(err)
	}

	resource := schema.GroupResource{
		Group:    "ingress.openshift.io",
		Resource: "clusteringresses",
	}

	//scale, err := scaleClient.Scales(ns).Get(resource, name)
	// XXX The following polling is a workaround for a problem that will be
	// fixed in client-go[1].  Once we get a newer client-go, the workaround
	// will no longer be needed; in this case, uncomment the assignment
	// immediately above this comment, and delete the variable declaration
	// and assignment below.
	// 1. https://github.com/kubernetes/client-go/commit/58652542545735c4b421dbf3631d81c7ec58e0c1.
	var scale *autoscalingv1.Scale
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		scale, err = scaleClient.Scales(ns).Get(resource, name)
		if err != nil {
			if !cachedDiscovery.Fresh() {
				t.Logf("failed to get scale due to stale cache, will retry: %v", err)
				return false, nil
			}

			t.Logf("failed to get scale due to error, will retry: %v", err)
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get initial scale of ClusterIngress: %v", err)
	}

	// Make sure the deployment's selector is reflected in the scale status.
	if scale.Status.Selector != selector.String() {
		t.Fatalf("expected scale status.selector to be %q, got %q", selector.String(), scale.Status.Selector)
	}

	// Scale the clusteringress up.
	scale.Spec.Replicas = scale.Spec.Replicas + 1
	updatedScale, err := scaleClient.Scales(ns).Update(resource, scale)
	if err != nil {
		t.Fatalf("failed to scale ClusterIngress up: %v", err)
	}
	if updatedScale.Spec.Replicas != scale.Spec.Replicas {
		t.Fatalf("expected scaled-up ClusterIngress's spec.replicas to be %d, got %d", scale.Spec.Replicas, updatedScale.Spec.Replicas)
	}

	// Wait for the deployment to scale up.
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, deployment); err != nil {
			return false, nil
		}

		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != updatedScale.Spec.Replicas {
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get scaled-up router deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
	}

	// Get the latest scale so that we can update it again.
	scale, err = scaleClient.Scales(ns).Get(resource, name)
	if err != nil {
		t.Fatalf("failed to get updated scale of ClusterIngress: %v", err)
	}

	// Scale the clusteringress back down.
	scale.Spec.Replicas = scale.Spec.Replicas - 1
	updatedScale, err = scaleClient.Scales(ns).Update(resource, scale)
	if err != nil {
		t.Fatalf("failed to scale ClusterIngress down: %v", err)
	}
	if updatedScale.Spec.Replicas != scale.Spec.Replicas {
		t.Fatalf("expected scaled-down ClusterIngress's spec.replicas to be %d, got %d", scale.Spec.Replicas, updatedScale.Spec.Replicas)
	}

	// Wait for the deployment to scale down.
	err = wait.PollImmediate(1*time.Second, 15*time.Second, func() (bool, error) {
		if err := cl.Get(context.TODO(), types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}, deployment); err != nil {
			return false, nil
		}

		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != updatedScale.Spec.Replicas {
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get scaled-down router deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
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
