//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// TestClientTLS generates a CA certificate, uses it to sign some client
// certificates, creates a custom ingresscontroller configured to use the CA
// certificate to verify client certificates, and verifies that the
// ingresscontroller accepts connections that use a valid client certificate and
// rejects connections that do not.
//
// The test first verifies that an ingresscontroller that specifies
// spec.clientTLS.clientCertificatePolicy: Optional accepts connections with no
// certificate or a valid certificate and rejects connections with an invalid
// certificate.
//
// Next, the test updates the ingresscontroller to specify
// spec.clientTLS.clientCertificatePolicy: Required as well as
// spec.clientTLS.allowedSubjectPatterns and verifies that the ingresscontroller
// accepts connections with a valid, matching certificate and rejects other
// connections.
func TestClientTLS(t *testing.T) {
	t.Parallel()
	// We will configure the ingresscontroller to recognize certificates
	// signed by this CA.
	ca, caKey, err := generateClientCA()
	if err != nil {
		t.Fatalf("failed to generate client CA certificate: %v", err)
	}
	validMatchingCert, validMatchingKey, err := generateClientCertificate(ca, caKey, "allowed")
	if err != nil {
		t.Fatalf("failed to generate first client certificate: %v", err)
	}
	validMismatchingCert, validMismatchingKey, err := generateClientCertificate(ca, caKey, "disallowed")
	if err != nil {
		t.Fatalf("failed to generate second client certificate: %v", err)
	}
	// The ingresscontroller will not recognize certificates signed by this
	// other CA.
	otherCA, otherCAKey, err := generateClientCA()
	if err != nil {
		t.Fatalf("failed to generate other CA certificate: %v", err)
	}
	invalidMatchingCert, invalidMatchingKey, err := generateClientCertificate(otherCA, otherCAKey, "allowed")
	if err != nil {
		t.Fatalf("failed to generate third client certificate: %v", err)
	}

	// Create the configmap for the CA certificate that our custom
	// ingresscontroller will use.
	clientCAConfigmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ca-certificate",
			Namespace: "openshift-config",
		},
		Data: map[string]string{
			"ca-bundle.pem": encodeCert(ca),
		},
	}
	clientCAConfigmapName := types.NamespacedName{
		Name:      clientCAConfigmap.Name,
		Namespace: clientCAConfigmap.Namespace,
	}
	if err := kclient.Create(context.TODO(), clientCAConfigmap); err != nil {
		t.Fatalf("failed to create configmap %q: %v", clientCAConfigmapName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientCAConfigmap); err != nil {
			t.Fatalf("failed to delete configmap %q: %v", clientCAConfigmapName, err)
		}
	}()

	// Create the custom ingresscontroller.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "client-tls"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.ClientTLS = operatorv1.ClientTLS{
		ClientCertificatePolicy: "Optional",
		ClientCA: configv1.ConfigMapNameReference{
			Name: clientCAConfigmapName.Name,
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// We need some route to which to send requests.  The canary route
	// should always exist and be responsive, so use that.
	route := &routev1.Route{}
	routeName := controller.CanaryRouteName()
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), routeName, route); err != nil {
			t.Logf("failed to get route %q: %v", routeName, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe route %q: %v", routeName, err)
	}

	// We need an IP address to which to send requests.  The test client
	// runs inside the cluster, so we can use the custom router's internal
	// service address.
	service := &corev1.Service{}
	serviceName := controller.InternalIngressControllerServiceName(ic)
	if err := kclient.Get(context.TODO(), serviceName, service); err != nil {
		t.Fatalf("failed to get service %q: %v", serviceName, err)
	}

	// We need an image that we can use for test clients.  The router image
	// has Curl, so we can use that image.
	deployment := &appsv1.Deployment{}
	deploymentName := controller.RouterDeploymentName(ic)
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get deployment %q: %v", deploymentName, err)
	}

	// Create a configmap with the client certificates.  We will mount this
	// configmap into our test client pod.
	clientCertsConfigmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "client-certificates",
			Namespace: "openshift-ingress",
		},
		Data: map[string]string{
			"valid-matching.crt":    encodeCert(validMatchingCert),
			"valid-matching.key":    encodeKey(validMatchingKey),
			"valid-mismatching.crt": encodeCert(validMismatchingCert),
			"valid-mismatching.key": encodeKey(validMismatchingKey),
			"invalid-matching.crt":  encodeCert(invalidMatchingCert),
			"invalid-matching.key":  encodeKey(invalidMatchingKey),
		},
	}
	clientCertsConfigmapName := types.NamespacedName{
		Name:      clientCertsConfigmap.Name,
		Namespace: clientCertsConfigmap.Namespace,
	}
	if err := kclient.Create(context.TODO(), clientCertsConfigmap); err != nil {
		t.Fatalf("failed to create configmap %q: %v", clientCertsConfigmapName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientCertsConfigmap); err != nil {
			t.Fatalf("failed to delete configmap %q: %v", clientCertsConfigmapName, err)
		}
	}()

	// Create a test client pod with the client certificates.  We will exec
	// curl commands in this pod to perform the tests.
	podName := "client-tls-test-client"
	image := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildExecPod(podName, clientCertsConfigmapName.Namespace, image)
	clientPod.Spec.Volumes = []corev1.Volume{{
		Name: "client-certificates",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: clientCertsConfigmapName.Name,
				},
			},
		},
	}}
	clientPod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
		Name:      "client-certificates",
		MountPath: "/tmp/tls",
		ReadOnly:  true,
	}}
	clientPodName := types.NamespacedName{
		Name:      clientPod.Name,
		Namespace: clientPod.Namespace,
	}
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %q: %v", clientPodName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete pod %q: %v", clientPodName, err)
		}
	}()

	err = wait.PollImmediate(2*time.Second, 3*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), clientPodName, clientPod); err != nil {
			t.Logf("failed to get client pod %q: %v", clientPodName, err)
			return false, nil
		}
		for _, cond := range clientPod.Status.Conditions {
			if cond.Type == corev1.PodReady {
				return cond.Status == corev1.ConditionTrue, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for pod %q to become ready: %v", clientPodName, err)
	}

	// We need a client-go client in order to execute commands in the client
	// pod.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	cl, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	// curl execs a Curl command in the test client pod and returns an error
	// value.  The Curl command uses the specified certificate from the
	// client certificates configmap, sends a request for the canary route
	// via the router's internal service, and returns an error if the Curl
	// command fails or the HTTP response status code indicates an error.
	curl := func(cert string) error {
		req := cl.CoreV1().RESTClient().Post().Resource("pods").
			Namespace(clientPod.Namespace).Name(clientPod.Name).
			SubResource("exec").
			Param("container", clientPod.Spec.Containers[0].Name)
		cmd := []string{
			"/bin/curl", "-k", "-v",
			"-w", "%{http_code}",
			"--retry", "10", "--retry-delay", "1",
		}
		if len(cert) != 0 {
			cmd = append(cmd,
				"--cert", fmt.Sprintf("/tmp/tls/%s.crt", cert),
				"--key", fmt.Sprintf("/tmp/tls/%s.key", cert),
			)
		}
		cmd = append(cmd, "--resolve",
			fmt.Sprintf("%s:443:%s", route.Spec.Host,
				service.Spec.ClusterIP),
			fmt.Sprintf("https://%s", route.Spec.Host),
		)
		req.VersionedParams(&corev1.PodExecOptions{
			Container: "curl",
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
		exec, err := remotecommand.NewSPDYExecutor(kubeConfig, "POST", req.URL())
		if err != nil {
			return err
		}
		var stdout, stderr bytes.Buffer
		err = exec.Stream(remotecommand.StreamOptions{
			Stdout: &stdout,
			Stderr: &stderr,
		})
		stdoutStr := stdout.String()
		t.Logf("command: %s\nstdout:\n%s\n\nstderr:\n%s\n",
			strings.Join(cmd, " "), stdoutStr, stderr.String())
		if err != nil {
			return err
		}
		httpStatusCode := stdoutStr[len(stdoutStr)-3:]
		switch string(httpStatusCode[0]) {
		case "0", "4", "5":
			return fmt.Errorf("got HTTP %s status code", httpStatusCode)
		default:
			return nil
		}
	}

	optionalPolicyTestCases := []struct {
		description   string
		cert          string
		expectAllowed bool
	}{{
		description:   "no client certificate",
		expectAllowed: true,
	}, {
		description:   "client certificate with valid issuer and allowed CN",
		cert:          "valid-matching",
		expectAllowed: true,
	}, {
		description: "client certificate with valid issuer and disallowed CN",
		cert:        "valid-mismatching",
		// We have set spec.clientTLS.clientCertificatePolicy: Optional
		// and haven't set spec.clientTLS.allowedSubjectPatterns, so
		// this is allowed.
		expectAllowed: true,
	}, {
		description:   "client certificate with invalid issuer and allowed CN",
		cert:          "invalid-matching",
		expectAllowed: false,
	}}
	for _, tc := range optionalPolicyTestCases {
		err := curl(tc.cert)
		if err == nil && !tc.expectAllowed {
			t.Errorf("%q: expected error, got success", tc.description)
		}
		if err != nil && tc.expectAllowed {
			t.Errorf("%q: expected success, got error: %v", tc.description, err)
		}
	}

	// Now make the client certificate TLS mandatory, and add a filter.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %q: %v", icName, err)
	}
	ic.Spec.ClientTLS.ClientCertificatePolicy = "Required"
	ic.Spec.ClientTLS.AllowedSubjectPatterns = []string{"/CN=allowed"}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller %q: %v", icName, err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_MUTUAL_TLS_AUTH", "required"); err != nil {
		t.Fatalf("expected updated deployment to have ROUTER_MUTUAL_TLS_AUTH=required: %v", err)
	}
	if err := waitForDeploymentCompleteWithCleanup(t, kclient, deploymentName, 3*time.Minute); err != nil {
		t.Fatalf("timed out waiting for old router generation to be cleaned up: %v", err)
	}

	requiredPolicyTestCases := []struct {
		description   string
		cert          string
		expectAllowed bool
	}{{
		description:   "no client certificate",
		expectAllowed: false,
	}, {
		description:   "client certificate with valid issuer and allowed CN",
		cert:          "valid-matching",
		expectAllowed: true,
	}, {
		description:   "client certificate with valid issuer and disallowed CN",
		cert:          "valid-mismatching",
		expectAllowed: false,
	}, {
		description:   "client certificate with invalid issuer and allowed CN",
		cert:          "invalid-matching",
		expectAllowed: false,
	}}
	for _, tc := range requiredPolicyTestCases {
		err := curl(tc.cert)
		if err == nil && !tc.expectAllowed {
			t.Errorf("%q: expected error, got success", tc.description)
		}
		if err != nil && tc.expectAllowed {
			t.Errorf("%q: expected success, got error: %v", tc.description, err)
		}
	}
}

// generateClientCA generates and returns a CA certificate and key.
func generateClientCA() (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	root := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "operator-e2e"},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		SerialNumber:          big.NewInt(1),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	certs, err := x509.ParseCertificates(der)
	if err != nil {
		return nil, nil, err
	}
	if len(certs) != 1 {
		return nil, nil, fmt.Errorf("expected a single certificate from x509.ParseCertificates, got %d: %v", len(certs), certs)
	}

	return certs[0], key, nil
}

// generateClientCertificate generates and returns a client certificate and key
// where the certificate is signed by the provided CA certificate.
func generateClientCertificate(caCert *x509.Certificate, caKey *rsa.PrivateKey, cn string) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{"OpenShift"},
		},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		SerialNumber:          big.NewInt(1),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	certs, err := x509.ParseCertificates(derBytes)
	if err != nil {
		return nil, nil, err
	}
	if len(certs) != 1 {
		return nil, nil, fmt.Errorf("expected a single certificate from x509.ParseCertificates, got %d: %v", len(certs), certs)
	}

	return certs[0], key, nil
}

// encodeCert returns a PEM block encoding the given certificate.
func encodeCert(cert *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}))
}

// encodeKey returns a PEM block encoding the given key.
func encodeKey(key *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}
