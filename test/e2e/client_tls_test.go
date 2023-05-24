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
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiserver/pkg/storage/names"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
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
			"valid-matching.pem":    encodeCert(validMatchingCert),
			"valid-matching.key":    encodeKey(validMatchingKey),
			"valid-mismatching.pem": encodeCert(validMismatchingCert),
			"valid-mismatching.key": encodeKey(validMismatchingKey),
			"invalid-matching.pem":  encodeCert(invalidMatchingCert),
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
		_, err := curlGetStatusCode(t, clientPod, tc.cert, route.Spec.Host, service.Spec.ClusterIP, true)
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
	if err := waitForDeploymentCompleteWithOldPodTermination(t, kclient, deploymentName, 3*time.Minute); err != nil {
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
		_, err := curlGetStatusCode(t, clientPod, tc.cert, route.Spec.Host, service.Spec.ClusterIP, true)
		if err == nil && !tc.expectAllowed {
			t.Errorf("%q: expected error, got success", tc.description)
		}
		if err != nil && tc.expectAllowed {
			t.Errorf("%q: expected success, got error: %v", tc.description, err)
		}
	}
}

// TestMTLSWithCRLsCerts includes all the certificates needed for a particular test case of TestMTLSWithCRLs.
type TestMTLSWithCRLsCerts struct {
	// CABundle is the complete PEM-encoded list of certificates that will be used both by HAProxy for client
	// validation, and by openshift-router for CRL distribution points.
	CABundle []string
	// CRLs is a map of the PEM-encoded CRLs, indexed by the filename that will be used in the CRL host pod's configmap.
	CRLs map[string]string
	// ClientCerts contains maps of the client certificates used to verify that mTLS is working as intended.
	ClientCerts struct {
		// Accepted is a map containing the client keys and certificates that should be able to connect to backends
		// successfully, indexed by a unique name.
		Accepted map[string]KeyCert
		// Rejected is a map containing the client keys and certificates that should NOT be able to connect to backends,
		// indexed by a unique name.
		Rejected map[string]KeyCert
	}
}

// TestMTLSWithCRLs verifies that mTLS works when the client auth chain includes certificate revocation lists (CRLs).
func TestMTLSWithCRLs(t *testing.T) {
	t.Parallel()
	namespaceName := names.SimpleNameGenerator.GenerateName("mtls-with-crls")
	crlHostName := types.NamespacedName{
		Name:      "crl-host",
		Namespace: namespaceName,
	}
	// When generating certificates, the CRL distribution points need to be specified by URL
	crlHostServiceName := "crl-host-service"
	crlHostURL := crlHostServiceName + "." + crlHostName.Namespace + ".svc"
	testCases := []struct {
		// Name is the name of the test case.
		Name string
		// CreateCerts generates the certificates for the test case. Certificates and CRLs must not have expired at the
		// time of the run, so they must be generated at runtime.
		CreateCerts func() TestMTLSWithCRLsCerts
	}{
		{
			// This test case has CA certificates including a CRL distribution point (CDP) for the CRL that they
			// generate and sign. This is the default way to distribute CRLs according to RFC-5280
			//
			// CA Bundle:
			// - Intermediate CA
			//   - Includes CRL distribution point for intermediate-ca.crl
			//   - Signed by Root CA.
			// - Root CA
			//   - Includes CRL distribution point for root-ca.crl
			//   - Self signed.
			//
			// Client Certificates:
			// - signed-by-root
			//   - Signed by Root CA.
			//   - Should successfully connect.
			// - signed-by-intermediate
			//   - Signed by Intermediate CA.
			//   - Should successfully connect.
			// - revoked-by-root
			//   - Signed by Root CA.
			//   - Has been revoked.
			//   - Should be rejected due to revocation.
			// - revoked-by-intermediate
			//   - Signed by Intermediate CA
			//   - Has been revoked
			//   - Should be rejected due to revocation.
			// - self-signed
			//   - Self signed
			//   - Should be rejected because it's not signed by any trusted CA.
			Name: "certificate-distributes-its-own-crl",
			CreateCerts: func() TestMTLSWithCRLsCerts {
				rootCDP := "http://" + crlHostURL + "/root/root.crl"
				intermediateCDP := "http://" + crlHostURL + "/intermediate/intermediate.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &rootCA)

				signedByRoot := MustCreateTLSKeyCert("client signed by root", time.Now(), time.Now().Add(24*time.Hour), false, nil, &rootCA)
				signedByIntermediate := MustCreateTLSKeyCert("client signed by intermediate", time.Now(), time.Now().Add(24*time.Hour), false, nil, &intermediateCA)
				revokedByRoot := MustCreateTLSKeyCert("client revoked by root", time.Now(), time.Now().Add(24*time.Hour), false, nil, &rootCA)
				revokedByIntermediate := MustCreateTLSKeyCert("client revoked by intermediate", time.Now(), time.Now().Add(24*time.Hour), false, nil, &intermediateCA)
				selfSigned := MustCreateTLSKeyCert("self signed cert", time.Now(), time.Now().Add(24*time.Hour), false, nil, nil)

				_, rootCRLPem := MustCreateCRL(nil, rootCA, time.Now(), time.Now().Add(1*time.Hour), RevokeCertificates(time.Now(), revokedByRoot))
				_, intermediateCRLPem := MustCreateCRL(nil, intermediateCA, time.Now(), time.Now().Add(1*time.Hour), RevokeCertificates(time.Now(), revokedByIntermediate))

				return TestMTLSWithCRLsCerts{
					CABundle: []string{
						intermediateCA.CertPem,
						rootCA.CertPem,
					},
					CRLs: map[string]string{
						"root":         rootCRLPem,
						"intermediate": intermediateCRLPem,
					},
					ClientCerts: struct {
						Accepted map[string]KeyCert
						Rejected map[string]KeyCert
					}{
						Accepted: map[string]KeyCert{
							"signed-by-root":         signedByRoot,
							"signed-by-intermediate": signedByIntermediate,
						},
						Rejected: map[string]KeyCert{
							"revoked-by-root":         revokedByRoot,
							"revoked-by-intermediate": revokedByIntermediate,
							"self-signed":             selfSigned,
						},
					},
				}
			},
		},
		{
			// This test case has certificates including the CRL distribution point of their signer (i.e. intermediate
			// CA is signed by root CA, and includes the URL for root's CRL). In this case, neither of the certificates
			// in the CA bundle include the intermediate CRL, so connections that rely on it will be rejected.
			//
			// CA Bundle:
			// - Intermediate CA
			//   - Includes CRL distribution point for root-ca.crl
			//   - Signed by Root CA.
			// - Root CA
			//   - No CRL distribution point.
			//   - Self signed.
			//
			// Note that intermediate-ca.crl is not present in the CA bundle.
			//
			// Client Certificates:
			// - signed-by-root
			//   - Includes CRL distribution point for root-ca.crl
			//   - Signed by Root CA.
			//   - Should successfully connect.
			// - signed-by-intermediate
			//   - Includes CRL distribution point for intermediate-ca.crl
			//   - Signed by Intermediate CA.
			//   - Should be rejected because HAProxy doesn't have intermediate-ca.crl (SSL error "unknown ca").
			// - revoked-by-root
			//   - Includes CRL distribution point for root-ca.crl
			//   - Signed by Root CA.
			//   - Has been revoked.
			//   - Should be rejected due to revocation.
			// - revoked-by-intermediate
			//   - Includes CRL distribution point for intermediate-ca.crl
			//   - Signed by Intermediate CA
			//   - Has been revoked
			//   - Should be rejected because HAProxy doesn't have intermediate-ca.crl (SSL error "unknown ca").
			// - self-signed
			//   - Self signed
			//   - Should be rejected because it's not signed by any trusted CA (SSL error "unknown ca").
			Name: "certificate-distributes-its-signers-crl",
			CreateCerts: func() TestMTLSWithCRLsCerts {
				rootCDP := "http://" + crlHostURL + "/root/root.crl"
				intermediateCDP := "http://" + crlHostURL + "/intermediate/intermediate.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, nil, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)

				signedByRoot := MustCreateTLSKeyCert("client signed by root", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)
				signedByIntermediate := MustCreateTLSKeyCert("client signed by intermediate", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &intermediateCA)
				revokedByRoot := MustCreateTLSKeyCert("client revoked by root", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)
				revokedByIntermediate := MustCreateTLSKeyCert("client revoked by intermediate", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &intermediateCA)
				selfSigned := MustCreateTLSKeyCert("self signed cert", time.Now(), time.Now().Add(24*time.Hour), false, nil, nil)

				_, rootCRLPem := MustCreateCRL(nil, rootCA, time.Now(), time.Now().Add(1*time.Hour), RevokeCertificates(time.Now(), revokedByRoot))
				_, intermediateCRLPem := MustCreateCRL(nil, intermediateCA, time.Now(), time.Now().Add(1*time.Hour), RevokeCertificates(time.Now(), revokedByIntermediate))

				return TestMTLSWithCRLsCerts{
					CABundle: []string{
						intermediateCA.CertPem,
						rootCA.CertPem,
					},
					CRLs: map[string]string{
						"root":         rootCRLPem,
						"intermediate": intermediateCRLPem,
					},
					ClientCerts: struct {
						Accepted map[string]KeyCert
						Rejected map[string]KeyCert
					}{
						Accepted: map[string]KeyCert{
							"signed-by-root": signedByRoot,
						},
						Rejected: map[string]KeyCert{
							"signed-by-intermediate":  signedByIntermediate,
							"revoked-by-root":         revokedByRoot,
							"revoked-by-intermediate": revokedByIntermediate,
							"self-signed":             selfSigned,
						},
					},
				}
			},
		},
		{
			// This test case has certificates including the CRL distribution point of their signer. In this case, a
			// leaf (client) certificate is included in the CA bundle so that openshift-router is aware of the
			// intermediate CRL's distribution point, so certificates signed by intermediate will work.
			// TODO: update this test case when RFE-3605 or a similar fix is implemented
			//
			// CA Bundle:
			// - revoked-by-intermediate
			//   - Includes CRL distribution point for intermediate-ca.crl
			//   - Signed by Intermediate CA
			// - Intermediate CA
			//   - Includes CRL distribution point for root-ca.crl
			//   - Signed by Root CA.
			// - Root CA
			//   - No CRL distribution point.
			//   - Self signed.
			//
			// Including revoked-by-intermediate in the CA bundle is the "workaround" in the test name. It makes sure
			// intermediate-ca.crl is listed in the CA bundle, so openshift-router knows to download it.
			//
			// Client Certificates:
			// - signed-by-root
			//   - Includes CRL distribution point for root-ca.crl
			//   - Signed by Root CA.
			//   - Should successfully connect.
			// - signed-by-intermediate
			//   - Includes CRL distribution point for intermediate-ca.crl
			//   - Signed by Intermediate CA.
			//   - Should successfully connect.
			// - revoked-by-root
			//   - Includes CRL distribution point for root-ca.crl
			//   - Signed by Root CA.
			//   - Has been revoked.
			//   - Should be rejected due to revocation.
			// - revoked-by-intermediate
			//   - Includes CRL distribution point for intermediate-ca.crl
			//   - Signed by Intermediate CA
			//   - Has been revoked
			//   - Should be rejected due to revocation.
			// - self-signed
			//   - Self signed
			//   - Should be rejected because it's not signed by any trusted CA (SSL error "unknown ca").
			Name: "certificate-distributes-its-signers-crl-with-workaround",
			CreateCerts: func() TestMTLSWithCRLsCerts {
				rootCDP := "http://" + crlHostURL + "/root/root.crl"
				intermediateCDP := "http://" + crlHostURL + "/intermediate/intermediate.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, nil, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)

				signedByRoot := MustCreateTLSKeyCert("client signed by root", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)
				signedByIntermediate := MustCreateTLSKeyCert("client signed by intermediate", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &intermediateCA)
				revokedByRoot := MustCreateTLSKeyCert("client revoked by root", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)
				revokedByIntermediate := MustCreateTLSKeyCert("client revoked by intermediate", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &intermediateCA)
				selfSigned := MustCreateTLSKeyCert("self signed cert", time.Now(), time.Now().Add(24*time.Hour), false, nil, nil)

				_, rootCRLPem := MustCreateCRL(nil, rootCA, time.Now(), time.Now().Add(1*time.Hour), RevokeCertificates(time.Now(), revokedByRoot))
				_, intermediateCRLPem := MustCreateCRL(nil, intermediateCA, time.Now(), time.Now().Add(1*time.Hour), RevokeCertificates(time.Now(), revokedByIntermediate))

				return TestMTLSWithCRLsCerts{
					CABundle: []string{
						revokedByIntermediate.CertPem,
						intermediateCA.CertPem,
						rootCA.CertPem,
					},
					CRLs: map[string]string{
						"root":         rootCRLPem,
						"intermediate": intermediateCRLPem,
					},
					ClientCerts: struct {
						Accepted map[string]KeyCert
						Rejected map[string]KeyCert
					}{
						Accepted: map[string]KeyCert{
							"signed-by-root":         signedByRoot,
							"signed-by-intermediate": signedByIntermediate,
						},
						Rejected: map[string]KeyCert{
							"revoked-by-root":         revokedByRoot,
							"revoked-by-intermediate": revokedByIntermediate,
							"self-signed":             selfSigned,
						},
					},
				}
			},
		},
		{
			// large-crl verifies that CRLs larger than 1MB can be used. This tests the fix for OCPBUGS-6661
			Name: "large-crl",
			CreateCerts: func() TestMTLSWithCRLsCerts {
				maxDummyRevokedSerialNumber := 25000
				rootCDP := "http://" + crlHostURL + "/root/root.crl"
				intermediateCDP := "http://" + crlHostURL + "/intermediate/intermediate.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &rootCA)

				signedByRoot := MustCreateTLSKeyCert("client signed by root", time.Now(), time.Now().Add(24*time.Hour), false, nil, &rootCA)
				signedByIntermediate := MustCreateTLSKeyCert("client signed by intermediate", time.Now(), time.Now().Add(24*time.Hour), false, nil, &intermediateCA)
				revokedByRoot := MustCreateTLSKeyCert("client revoked by root", time.Now(), time.Now().Add(24*time.Hour), false, nil, &rootCA)
				revokedByIntermediate := MustCreateTLSKeyCert("client revoked by intermediate", time.Now(), time.Now().Add(24*time.Hour), false, nil, &intermediateCA)
				selfSigned := MustCreateTLSKeyCert("self signed cert", time.Now(), time.Now().Add(24*time.Hour), false, nil, nil)

				// Generate a set of CRL files that are larger than 1MB by revoking a large number of certificates. The
				// revocation list only includes the serial number of the certificate and the time of revocation, so we
				// don't actually need to generate real certificates, just some serial numbers. We can also repeat the
				// same serial numbers in each CRL, cutting down on the number we need to generate by half
				revokedCerts := []pkix.RevokedCertificate{}
				for i := int64(1); i <= int64(maxDummyRevokedSerialNumber); i++ {
					serialNumber := big.NewInt(i)
					// It's highly unlikely that any of the certs we explicitly generated have serial numbers less than
					// maxDummyRevokedSerialNumber since they're 20-byte UUIDs, but is possible. In the unlikely event
					// that there's some overlap, don't include those serial numbers in the initial list of "revoked"
					// certs.
					switch {
					case signedByRoot.Cert.SerialNumber.Cmp(serialNumber) == 0:
						continue
					case revokedByRoot.Cert.SerialNumber.Cmp(serialNumber) == 0:
						continue
					case signedByIntermediate.Cert.SerialNumber.Cmp(serialNumber) == 0:
						continue
					case revokedByIntermediate.Cert.SerialNumber.Cmp(serialNumber) == 0:
						continue
					}
					revokedCerts = append(revokedCerts, pkix.RevokedCertificate{
						SerialNumber:   serialNumber,
						RevocationTime: time.Now(),
					})
				}

				rootRevokedCerts := make([]pkix.RevokedCertificate, len(revokedCerts))
				copy(rootRevokedCerts, revokedCerts)
				rootRevokedCerts = append(rootRevokedCerts, pkix.RevokedCertificate{
					SerialNumber:   revokedByRoot.Cert.SerialNumber,
					RevocationTime: time.Now(),
				})
				_, rootCRLPem := MustCreateCRL(nil, rootCA, time.Now(), time.Now().Add(1*time.Hour), rootRevokedCerts)

				intermediateRevokedCerts := make([]pkix.RevokedCertificate, len(revokedCerts))
				copy(intermediateRevokedCerts, revokedCerts)
				intermediateRevokedCerts = append(intermediateRevokedCerts, pkix.RevokedCertificate{
					SerialNumber:   revokedByIntermediate.Cert.SerialNumber,
					RevocationTime: time.Now(),
				})
				_, intermediateCRLPem := MustCreateCRL(nil, intermediateCA, time.Now(), time.Now().Add(1*time.Hour), intermediateRevokedCerts)
				t.Logf("Root CRL Size: %dKB\nIntermediate CRL Size: %dKB\nTotal Size: %dKB", len(rootCRLPem)/1024, len(intermediateCRLPem)/1024, (len(rootCRLPem)+len(intermediateCRLPem))/1024)

				return TestMTLSWithCRLsCerts{
					CABundle: []string{
						intermediateCA.CertPem,
						rootCA.CertPem,
					},
					CRLs: map[string]string{
						"root":         rootCRLPem,
						"intermediate": intermediateCRLPem,
					},
					ClientCerts: struct {
						Accepted map[string]KeyCert
						Rejected map[string]KeyCert
					}{
						Accepted: map[string]KeyCert{
							"signed-by-root":         signedByRoot,
							"signed-by-intermediate": signedByIntermediate,
						},
						Rejected: map[string]KeyCert{
							"revoked-by-root":         revokedByRoot,
							"revoked-by-intermediate": revokedByIntermediate,
							"self-signed":             selfSigned,
						},
					},
				}
			},
		},
		{
			// multiple-intermediate-ca tests that more than 2 CAs can be used. Each CA lists its own CRL's distribution point.
			Name: "multiple-intermediate-ca",
			CreateCerts: func() TestMTLSWithCRLsCerts {
				CANames := []string{
					"root",
					"foo",
					"bar",
					"baz",
					"quux",
				}
				caCerts := map[string]KeyCert{}
				acceptedClientCerts := map[string]KeyCert{}
				rejectedClientCerts := map[string]KeyCert{}
				crls := map[string]string{}
				caBundle := []string{}

				for i, name := range CANames {
					crlDistributionPoint := "http://" + crlHostURL + "/" + name + "/" + name + ".crl"
					caCert := KeyCert{}
					if i == 0 {
						// i = 0 is the root certificate, so it's self signed.
						caCert = MustCreateTLSKeyCert(name, time.Now(), time.Now().Add(24*time.Hour), true, []string{crlDistributionPoint}, nil)
					} else {
						// Non-root certificates are signed by the previous CA in the list.
						signer := caCerts[CANames[i-1]]
						caCert = MustCreateTLSKeyCert(name, time.Now(), time.Now().Add(24*time.Hour), true, []string{crlDistributionPoint}, &signer)
					}
					caCerts[name] = caCert
					caBundle = append(caBundle, caCerts[name].CertPem)

					// For each CA, generate 1 cert that will be accepted, and 1 that will be revoked (and therefore rejected).
					acceptedCert := MustCreateTLSKeyCert("client signed by "+name, time.Now(), time.Now().Add(24*time.Hour), false, nil, &caCert)
					revokedCert := MustCreateTLSKeyCert("client revoked by "+name, time.Now(), time.Now().Add(24*time.Hour), false, nil, &caCert)
					_, crls[name] = MustCreateCRL(nil, caCerts[name], time.Now(), time.Now().Add(1*time.Hour), RevokeCertificates(time.Now(), revokedCert))
					acceptedClientCerts["signed-by-"+name] = acceptedCert
					rejectedClientCerts["revoked-by-"+name] = revokedCert
				}

				// In addition to the certificates for each CA, include a self-signed certificate to make sure it's rejected.
				rejectedClientCerts["self-signed"] = MustCreateTLSKeyCert("self signed cert", time.Now(), time.Now().Add(24*time.Hour), false, nil, nil)

				return TestMTLSWithCRLsCerts{
					CABundle: caBundle,
					CRLs:     crls,
					ClientCerts: struct {
						Accepted map[string]KeyCert
						Rejected map[string]KeyCert
					}{
						Accepted: acceptedClientCerts,
						Rejected: rejectedClientCerts,
					},
				}
			},
		},
	}

	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		},
	}
	if err := kclient.Create(context.TODO(), &namespace); err != nil {
		t.Fatalf("Failed to create namespace %q: %v", namespace.Name, err)
	}
	defer assertDeletedWaitForCleanup(t, kclient, &namespace)
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			tcCerts := tc.CreateCerts()
			// Get the URL path of one of the CRLs to use in the CRL host pod's readiness probe.
			readinessProbePath := ""
			for crlName := range tcCerts.CRLs {
				readinessProbePath = fmt.Sprintf("/%s/%s.crl", crlName, crlName)
				break
			}
			// Create a pod which will host the CRLs.
			crlHostPod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crlHostName.Name,
					Namespace: namespace.Name,
					Labels:    map[string]string{"app": crlHostName.Name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "httpd",
						Image: "quay.io/centos7/httpd-24-centos7",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 8080,
							Name:          "http-svc",
						}},
						SecurityContext: generateUnprivilegedSecurityContext(),
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: readinessProbePath,
									Port: intstr.IntOrString{
										Type:   intstr.String,
										StrVal: "http-svc",
									},
								},
							},
						},
					}},
				},
			}
			for name, crl := range tcCerts.CRLs {
				crlConfigMapName := name + "-crl"
				crlConfigMap := corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      crlConfigMapName,
						Namespace: namespace.Name,
					},
					Data: map[string]string{
						name + ".crl": crl,
					},
				}
				crlHostPod.Spec.Volumes = append(crlHostPod.Spec.Volumes, corev1.Volume{
					Name: name,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: crlConfigMap.Name,
							},
						},
					},
				})
				crlHostPod.Spec.Containers[0].VolumeMounts = append(crlHostPod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
					Name:      name,
					MountPath: filepath.Join("/var/www/html", name),
					ReadOnly:  true,
				})

				if err := kclient.Create(context.TODO(), &crlConfigMap); err != nil {
					t.Fatalf("Failed to create configmap %q: %v", crlConfigMap.Name, err)
				}
				defer assertDeleted(t, kclient, &crlConfigMap)
			}

			if err := kclient.Create(context.TODO(), &crlHostPod); err != nil {
				t.Fatalf("Failed to create pod %q: %v", crlHostPod.Name, err)
			}
			// the crlHostPod is one of the first resources to be created, and one of the last to be deleted thanks to
			// defer stack ordering. calling assertDeletedWaitForCleanup here makes sure that the test case doesn't
			// finish until it's fully cleaned up, so when the next test case creates its own version of crlHostPod, it
			// won't be clashing. As of this writing, the other resources are normally cleaned up before the next test
			// case comes through and creates a new one, but if that stops being true in the future, their assertDeleted
			// calls may need to be replaced by the slower assertDeletedWaitForCleanup option.
			defer assertDeletedWaitForCleanup(t, kclient, &crlHostPod)
			crlHostService := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crlHostServiceName,
					Namespace: namespace.Name,
				},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{"app": crlHostName.Name},
					Ports: []corev1.ServicePort{{
						Name:       "http",
						Port:       80,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromString("http-svc"),
					}},
				},
			}
			if err := kclient.Create(context.TODO(), &crlHostService); err != nil {
				t.Fatalf("Failed to create service %q: %v", crlHostService.Name, err)
			}
			defer assertDeleted(t, kclient, &crlHostService)
			// Wait for CRL host to be ready.
			err := wait.PollImmediate(2*time.Second, 3*time.Minute, func() (bool, error) {
				if err := kclient.Get(context.TODO(), crlHostName, &crlHostPod); err != nil {
					t.Logf("error getting pod %s/%s: %v", crlHostName.Namespace, crlHostName.Name, err)
					return false, nil
				}
				for _, condition := range crlHostPod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						return true, nil
					}
				}
				return false, nil
			})
			// Create CA cert bundle
			clientCAConfigmapName := "client-ca-cm-" + namespace.Name
			clientCAConfigmap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clientCAConfigmapName,
					Namespace: "openshift-config",
				},
				Data: map[string]string{
					"ca-bundle.pem": strings.Join(tcCerts.CABundle, "\n"),
				},
			}
			if err := kclient.Create(context.TODO(), &clientCAConfigmap); err != nil {
				t.Fatalf("Failed to create CA cert configmap: %v", err)
			}
			defer assertDeleted(t, kclient, &clientCAConfigmap)
			icName := types.NamespacedName{
				Name:      "mtls-with-crls",
				Namespace: operatorNamespace,
			}
			icDomain := icName.Name + "." + dnsConfig.Spec.BaseDomain
			ic := newPrivateController(icName, icDomain)
			ic.Spec.ClientTLS = operatorv1.ClientTLS{
				ClientCA: configv1.ConfigMapNameReference{
					Name: clientCAConfigmapName,
				},
				ClientCertificatePolicy: operatorv1.ClientCertificatePolicyRequired,
			}
			if err := kclient.Create(context.TODO(), ic); err != nil {
				t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
			}
			defer assertIngressControllerDeleted(t, kclient, ic)

			if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
				t.Fatalf("failed to observe expected conditions: %v", err)
			}

			// The client pod will need the client certificates we generated, so create a configmap with all the client
			// certificates and keys, and mount that to the client pod.
			clientCertsConfigmap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "client-certificates",
					Namespace: namespace.Name,
				},
				Data: map[string]string{},
			}
			for name, keyCert := range tcCerts.ClientCerts.Accepted {
				clientCertsConfigmap.Data[name+".key"] = encodeKey(keyCert.Key)
				clientCertsConfigmap.Data[name+".pem"] = keyCert.CertFullChain
			}
			for name, keyCert := range tcCerts.ClientCerts.Rejected {
				clientCertsConfigmap.Data[name+".key"] = encodeKey(keyCert.Key)
				clientCertsConfigmap.Data[name+".pem"] = keyCert.CertFullChain
			}
			if err := kclient.Create(context.TODO(), &clientCertsConfigmap); err != nil {
				t.Fatalf("failed to create configmap %q: %v", clientCertsConfigmap.Name, err)
			}
			defer assertDeleted(t, kclient, &clientCertsConfigmap)

			// Use the router image for the exec pod since it has curl.
			routerDeployment := &appsv1.Deployment{}
			routerDeploymentName := controller.RouterDeploymentName(ic)
			if err := kclient.Get(context.TODO(), routerDeploymentName, routerDeployment); err != nil {
				t.Fatalf("failed to get routerDeployment %q: %v", routerDeploymentName, err)
			}

			podName := "mtls-with-crls-client"
			image := routerDeployment.Spec.Template.Spec.Containers[0].Image
			clientPod := buildExecPod(podName, namespace.Name, image)
			clientPod.Spec.Volumes = []corev1.Volume{{
				Name: "client-certificates",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: clientCertsConfigmap.Name,
						},
					},
				},
			}}
			clientPod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
				Name:      "client-certificates",
				MountPath: "/tmp/tls/",
				ReadOnly:  true,
			}}
			clientPodName := types.NamespacedName{
				Name:      clientPod.Name,
				Namespace: clientPod.Namespace,
			}
			if err := kclient.Create(context.TODO(), clientPod); err != nil {
				t.Fatalf("failed to create pod %q: %v", clientPodName, err)
			}
			defer assertDeleted(t, kclient, clientPod)

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

			// Wait until the CRLs are downloaded
			podList := &corev1.PodList{}
			labels := map[string]string{
				controller.ControllerDeploymentLabel: icName.Name,
			}
			if err := kclient.List(context.TODO(), podList, client.InNamespace("openshift-ingress"), client.MatchingLabels(labels)); err != nil {
				t.Logf("failed to list pods for ingress controllers %s: %v", ic.Name, err)
			}
			if len(podList.Items) == 0 {
				t.Fatalf("no router pods found for ingresscontroller %s: %v", ic.Name, err)
			}
			routerPod := podList.Items[0]
			err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
				// Get the current CRL file from the router container
				cmd := []string{"cat", "/var/lib/haproxy/mtls/latest/crls.pem"}
				stdout := bytes.Buffer{}
				stderr := bytes.Buffer{}
				if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
					t.Logf("exec %q failed. error: %v\nstdout:\n%s\nstderr:\n%s", cmd, err, stdout.String(), stderr.String())
					return false, err
				}
				// Parse the first CRL. If CRLs haven't been downloaded yet, it will be the placeholder CRL.
				block, _ := pem.Decode(stdout.Bytes())
				crl, err := x509.ParseRevocationList(block.Bytes)
				if err != nil {
					return false, fmt.Errorf("invalid CRL: %v", err)
				}
				return crl.Issuer.CommonName != "Placeholder CA", nil
			})

			// Get the canary route to use as the target for curl.
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

			// If the canary route is used, normally the default ingress controller will handle the request, but by
			// using curl's --resolve flag, we can send an HTTP request intended for the canary pod directly to our
			// ingress controller instead. In order to do that, we need the ingress controller's service IP.
			service := &corev1.Service{}
			serviceName := controller.InternalIngressControllerServiceName(ic)
			if err := kclient.Get(context.TODO(), serviceName, service); err != nil {
				t.Fatalf("failed to get service %q: %v", serviceName, err)
			}

			for certName := range tcCerts.ClientCerts.Accepted {
				if _, err := curlGetStatusCode(t, clientPod, certName, route.Spec.Host, service.Spec.ClusterIP, false); err != nil {
					t.Errorf("Failed to curl route with cert %q: %v", certName, err)
				}
			}
			for certName := range tcCerts.ClientCerts.Rejected {
				if httpStatusCode, err := curlGetStatusCode(t, clientPod, certName, route.Spec.Host, service.Spec.ClusterIP, false); err != nil {
					if httpStatusCode == 0 {
						// TLS/SSL verification failures result in a 0 http status code (no connection is made to the backend, so no http status code is returned).
						continue
					}
					t.Errorf("Unexpected error from curl for cert %q: %v", certName, err)
				} else {
					t.Errorf("Expected curl route with cert %q to fail but succeeded", certName)
				}
			}
		})
	}
}

// TestCRLUpdate verifies that CRLs are updated when they expire
func TestCRLUpdate(t *testing.T) {
	t.Parallel()
	testName := names.SimpleNameGenerator.GenerateName("crl-update")
	crlHostName := types.NamespacedName{
		Name:      "crl-host",
		Namespace: testName,
	}
	// When generating certificates, the CRL distribution points need to be specified by URL
	crlHostServiceName := "crl-host-service"
	crlHostURL := crlHostServiceName + "." + crlHostName.Namespace + ".svc"
	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testName,
		},
	}
	if err := kclient.Create(context.TODO(), &namespace); err != nil {
		t.Fatalf("Failed to create namespace %q: %v", namespace.Name, err)
	}
	defer assertDeleted(t, kclient, &namespace)
	testCases := []struct {
		// Test case name
		Name string
		// Function to generate the necessary certificates. These will be put into the ingresscontroller's client CA
		// bundle, and will be used to generate CRLs during the test.
		CreateCerts func() map[string]KeyCert
		// The names of the CAs whose CRLs are expected to be downloaded by the router pod. Only the certs with the
		// corresponding names will be used to generate CRLs
		ExpectedCRLs []string
	}{
		{
			Name: "certificate-distributes-its-own-crl",
			CreateCerts: func() map[string]KeyCert {
				rootCDP := "http://" + crlHostURL + "/root.crl"
				intermediateCDP := "http://" + crlHostURL + "/intermediate.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &rootCA)
				return map[string]KeyCert{
					"root":         rootCA,
					"intermediate": intermediateCA,
				}
			},
			ExpectedCRLs: []string{
				"root",
				"intermediate",
			},
		},
		{
			Name: "certificate-distributes-its-signers-crl",
			CreateCerts: func() map[string]KeyCert {
				rootCDP := "http://" + crlHostURL + "/root.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{}, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)
				return map[string]KeyCert{
					"root":         rootCA,
					"intermediate": intermediateCA,
				}
			},
			ExpectedCRLs: []string{
				"root",
			},
		},
		{
			Name: "certificate-distributes-its-signers-crl-with-workaround",
			CreateCerts: func() map[string]KeyCert {
				rootCDP := "http://" + crlHostURL + "/root.crl"
				intermediateCDP := "http://" + crlHostURL + "/intermediate.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{}, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)
				client := MustCreateTLSKeyCert("workaround client cert", time.Now(), time.Now().Add(24*time.Hour), false, []string{intermediateCDP}, &intermediateCA)
				return map[string]KeyCert{
					"root":         rootCA,
					"intermediate": intermediateCA,
					"client":       client,
				}
			},
			ExpectedCRLs: []string{
				"root",
				"intermediate",
			},
		},
		{
			Name: "many-CAs-with-signers-crl-workaround",
			CreateCerts: func() map[string]KeyCert {
				rootCDP := "http://" + crlHostURL + "/root.crl"
				intermediateCDP := "http://" + crlHostURL + "/intermediate.crl"
				fooCDP := "http://" + crlHostURL + "/foo.crl"
				barCDP := "http://" + crlHostURL + "/bar.crl"

				rootCA := MustCreateTLSKeyCert("testing root CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{}, nil)
				intermediateCA := MustCreateTLSKeyCert("testing intermediate CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{rootCDP}, &rootCA)
				fooCA := MustCreateTLSKeyCert("testing foo CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{intermediateCDP}, &intermediateCA)
				barCA := MustCreateTLSKeyCert("testing bar CA", time.Now(), time.Now().Add(24*time.Hour), true, []string{fooCDP}, &fooCA)
				client := MustCreateTLSKeyCert("workaround client cert", time.Now(), time.Now().Add(24*time.Hour), false, []string{barCDP}, &barCA)
				return map[string]KeyCert{
					"root":         rootCA,
					"intermediate": intermediateCA,
					"foo":          fooCA,
					"bar":          barCA,
					"client":       client,
				}
			},
			ExpectedCRLs: []string{
				"root",
				"intermediate",
				"foo",
				"bar",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			caCerts := tc.CreateCerts()
			// Generate CRLs. Offset the expiration times by 1 minute each so that we can verify that only the correct CRLs get updated at each expiration
			currentCRLs := map[string]*x509.RevocationList{}
			crlPems := map[string]string{}
			caBundle := []string{}
			validTime := 3 * time.Minute
			//expirations := []time.Time{}
			for _, caName := range tc.ExpectedCRLs {
				currentCRLs[caName], crlPems[caName+".crl"] = MustCreateCRL(nil, caCerts[caName], time.Now(), time.Now().Add(validTime), nil)
				validTime += time.Minute
			}
			for _, caCert := range caCerts {
				caBundle = append(caBundle, caCert.CertPem)
			}
			// Create a pod which will host the CRLs.
			crlConfigMapName := crlHostName.Name + "-configmap"
			crlConfigMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crlConfigMapName,
					Namespace: namespace.Name,
				},
				Data: crlPems,
			}
			if err := kclient.Create(context.TODO(), &crlConfigMap); err != nil {
				t.Fatalf("Failed to create configmap %q: %v", crlConfigMap.Name, err)
			}
			defer assertDeleted(t, kclient, &crlConfigMap)
			crlHostPod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crlHostName.Name,
					Namespace: namespace.Name,
					Labels:    map[string]string{"app": crlHostName.Name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "httpd",
						Image: "quay.io/centos7/httpd-24-centos7",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 8080,
							Name:          "http-svc",
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "data",
							MountPath: "/var/www/html",
							ReadOnly:  true,
						}},
						SecurityContext: generateUnprivilegedSecurityContext(),
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: fmt.Sprintf("/%s.crl", tc.ExpectedCRLs[0]),
									Port: intstr.IntOrString{
										Type:   intstr.String,
										StrVal: "http-svc",
									},
								},
							},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: crlConfigMap.Name,
								},
							},
						},
					}},
				},
			}

			if err := kclient.Create(context.TODO(), &crlHostPod); err != nil {
				t.Fatalf("Failed to create pod %q: %v", crlHostPod.Name, err)
			}
			// the crlHostPod is one of the first resources to be created, and one of the last to be deleted thanks to
			// defer stack ordering. calling assertDeletedWaitForCleanup here makes sure that the test case doesn't
			// finish until it's fully cleaned up, so when the next test case creates its own version of crlHostPod, it
			// won't be clashing. As of this writing, the other resources are normally cleaned up before the next test
			// case comes through and creates a new one, but if that stops being true in the future, their assertDeleted
			// calls may need to be replaced by the slower assertDeletedWaitForCleanup option.
			defer assertDeletedWaitForCleanup(t, kclient, &crlHostPod)
			crlHostService := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crlHostServiceName,
					Namespace: namespace.Name,
				},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{"app": crlHostName.Name},
					Ports: []corev1.ServicePort{{
						Name:       "http",
						Port:       80,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromString("http-svc"),
					}},
				},
			}
			if err := kclient.Create(context.TODO(), &crlHostService); err != nil {
				t.Fatalf("Failed to create service %q: %v", crlHostService.Name, err)
			}
			defer assertDeleted(t, kclient, &crlHostService)
			// Wait for CRL host to be ready.
			err := wait.PollImmediate(2*time.Second, 3*time.Minute, func() (bool, error) {
				if err := kclient.Get(context.TODO(), crlHostName, &crlHostPod); err != nil {
					t.Logf("error getting pod %s/%s: %v", crlHostName.Namespace, crlHostName.Name, err)
					return false, nil
				}
				for _, condition := range crlHostPod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						return true, nil
					}
				}
				return false, nil
			})
			// Create CA cert bundle.
			clientCAConfigmapName := "client-ca-cm-" + namespace.Name
			clientCAConfigmap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clientCAConfigmapName,
					Namespace: "openshift-config",
				},
				Data: map[string]string{
					"ca-bundle.pem": strings.Join(caBundle, "\n"),
				},
			}
			if err := kclient.Create(context.TODO(), &clientCAConfigmap); err != nil {
				t.Fatalf("Failed to create CA cert configmap: %v", err)
			}
			defer assertDeleted(t, kclient, &clientCAConfigmap)
			icName := types.NamespacedName{
				Name:      testName,
				Namespace: operatorNamespace,
			}
			icDomain := icName.Name + "." + dnsConfig.Spec.BaseDomain
			ic := newPrivateController(icName, icDomain)
			ic.Spec.ClientTLS = operatorv1.ClientTLS{
				ClientCA: configv1.ConfigMapNameReference{
					Name: clientCAConfigmapName,
				},
				ClientCertificatePolicy: operatorv1.ClientCertificatePolicyRequired,
			}
			if err := kclient.Create(context.TODO(), ic); err != nil {
				t.Fatalf("failed to create ingresscontroller %s/%s: %v", icName.Namespace, icName.Name, err)
			}
			defer assertIngressControllerDeleted(t, kclient, ic)

			if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
				t.Fatalf("failed to observe expected conditions: %v", err)
			}

			deploymentName := controller.RouterDeploymentName(ic)
			deployment, err := getDeployment(t, kclient, deploymentName, 1*time.Minute)
			if err != nil {
				t.Fatalf("failed to get deployment %s/%s: %v", deploymentName.Namespace, deploymentName.Name, err)
			}
			routerPodList, err := getPods(t, kclient, deployment)
			if err != nil {
				t.Fatalf("failed to get pods in deployment %s/%s: %v", deploymentName.Namespace, deploymentName.Name, err)
			} else if len(routerPodList.Items) == 0 {
				t.Fatalf("no pods found in deployment %s/%s", deploymentName.Namespace, deploymentName.Name)
			}
			routerPod := routerPodList.Items[0]

			// Verify that the router pod has downloaded all the correct CRLs.
			err = wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
				return verifyCRLs(t, &routerPod, currentCRLs)
			})
			if err != nil {
				t.Fatalf("Failed initial CRL check: %v", err)
			}
			// Once initial CRLs are downloaded, generate new CRLs, and update the crl-host configmap. These new CRLs
			// should be downloaded as the old ones expire.
			newCRLs := map[string]*x509.RevocationList{}
			for _, caName := range tc.ExpectedCRLs {
				newCRLs[caName], crlPems[caName+".crl"] = MustCreateCRL(currentCRLs[caName], caCerts[caName], time.Now(), time.Now().Add(1*time.Hour), nil)
			}
			crlConfigMap.Data = crlPems
			if err := kclient.Update(context.TODO(), &crlConfigMap); err != nil {
				t.Fatalf("Failed to update crl-host configmap: %v", err)
			}
			// Wait until each update CRL is updated, and verify that the updated CRL file matches the expected CRLs
			for updates := 0; updates < len(currentCRLs); updates++ {
				// Find the CRL that will expire first, and wait until its expiration.
				nextExpiration := time.Time{}
				expiringCRLCAName := ""
				for caName, CRL := range currentCRLs {
					if nextExpiration.IsZero() || CRL.NextUpdate.Before(nextExpiration) {
						nextExpiration = CRL.NextUpdate
						// keep track of the index of the next crl to expire
						expiringCRLCAName = caName
					}
				}
				t.Logf("Waiting until %s for CRL expiration", nextExpiration.Format(time.Stamp))
				// Replace the expiring CRL with its updated version in currentCRLs, and when the expiration time
				// occurs, verify that the CRL file in the router pod is updated.
				currentCRLs[expiringCRLCAName] = newCRLs[expiringCRLCAName]
				// Wait for expiration
				<-time.After(time.Until(nextExpiration))
				// Verify correct CRLs are present
				if err := wait.PollImmediate(5*time.Second, 1*time.Minute, func() (bool, error) {
					return verifyCRLs(t, &routerPod, currentCRLs)
				}); err != nil {
					if err != nil {
						t.Fatalf("Failed waiting for %s CRL to be updated: %v", expiringCRLCAName, err)
					}
				}
			}
		})
	}
}

func verifyCRLs(t *testing.T, pod *corev1.Pod, expectedCRLs map[string]*x509.RevocationList) (bool, error) {
	t.Helper()
	activeCRLs, err := getActiveCRLs(t, pod)
	if err != nil {
		return false, err
	}
	if len(activeCRLs) == 0 {
		// 0 CRLs probably means the router hasn't completed startup yet. Retry.
		return false, nil
	}
	if len(activeCRLs) == 1 && activeCRLs[0].Issuer.CommonName == "Placeholder CA" {
		// Placeholder CA CRL means the actual CRLs haven't been downloaded yet.
		return false, nil
	}
	if len(activeCRLs) != len(expectedCRLs) {
		return false, fmt.Errorf("incorrect number of CRLs found in pod %s/%s. expected %d, got %d", pod.Namespace, pod.Name, len(expectedCRLs), len(activeCRLs))
	}
	matchingCRLs := 0
	for _, expectedCRL := range expectedCRLs {
		for _, foundCRL := range activeCRLs {
			if foundCRL.Issuer.String() == expectedCRL.Issuer.String() {
				if foundCRL.Number.Cmp(expectedCRL.Number) == 0 {
					// Name and sequence number match, so the CRLs should be equivalent.
					matchingCRLs++
					break
				} else {
					// Name matches but version doesn't. Wait for it to potentially be updated
					t.Logf("Found %s but version does not match. expected %d, got %d", expectedCRL.Issuer.String(), expectedCRL.Number, foundCRL.Number)
					return false, nil
				}
			}
		}
	}
	if len(expectedCRLs) != matchingCRLs {
		t.Errorf("Expected:")
		for _, crl := range expectedCRLs {
			t.Errorf("%q version %d", crl.Issuer.String(), crl.Number)
		}
		t.Errorf("Found:")
		for _, crl := range activeCRLs {
			t.Errorf("%q version %d", crl.Issuer.String(), crl.Number)
		}
		return false, fmt.Errorf("found %d CRLs, but only %d matched", len(activeCRLs), matchingCRLs)
	}
	return true, nil
}

func getPods(t *testing.T, cl client.Client, deployment *appsv1.Deployment) (*corev1.PodList, error) {
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("deployment %s has invalid spec.selector: %w", deployment.Name, err)
	}
	podList := &corev1.PodList{}
	if err := cl.List(context.TODO(), podList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		t.Logf("failed to list pods for deployment %q: %v", deployment.Name, err)
		return nil, err
	}
	return podList, nil
}

func getActiveCRLs(t *testing.T, clientPod *corev1.Pod) ([]*x509.RevocationList, error) {
	t.Helper()
	cmd := []string{
		"cat",
		"/var/lib/haproxy/mtls/latest/crls.pem",
	}
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	err := podExec(t, *clientPod, &stdout, &stderr, cmd)
	if err != nil {
		t.Logf("stdout:\n%s", stdout.String())
		t.Logf("stderr:\n%s", stderr.String())
		return nil, err
	}
	crls := []*x509.RevocationList{}
	crlData := []byte(stdout.String())
	for len(crlData) > 0 {
		block, data := pem.Decode(crlData)
		if block == nil {
			break
		}
		crl, err := x509.ParseRevocationList(block.Bytes)
		if err != nil {
			return nil, err
		}
		crls = append(crls, crl)
		crlData = data
	}
	return crls, nil
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

// curlGetStatusCode execs a Curl command in the test client pod and returns an error value.  The Curl command uses the
// specified certificate from the client certificates configmap, sends a request for the canary route via the router's
// internal service. Returns the HTTP status code returned from curl, and an error either if there is an HTTP error, or
// if there's another error in running the command. If the error was not an HTTP error, the HTTP status code returned
// will be -1.
func curlGetStatusCode(t *testing.T, clientPod *corev1.Pod, certName, endpoint, ingressControllerIP string, verbose bool) (int64, error) {
	t.Helper()
	cmd := []string{
		"/bin/curl",
		"--silent",
		// Allow self-signed certs.
		"-k",
		// Output the http status code (i.e. 200 (OK) or 404 (Not found)) to stdout.
		"-w", "%{http_code}",
		// Retry on timeouts, 4xx errors, or 500/502/503/504 errors.
		"--retry", "10",
		// Sleep 1 second between retries.
		"--retry-delay", "1",
		// Use --resolve to guarantee that the request is sent through this test's ingress controller.
		"--resolve", fmt.Sprintf("%s:443:%s", endpoint, ingressControllerIP),
		fmt.Sprintf("https://%s", endpoint),
	}
	if verbose {
		cmd = append(cmd, "-v")
	}
	if len(certName) != 0 {
		cmd = append(cmd,
			"--cert", fmt.Sprintf("/tmp/tls/%s.pem", certName),
			"--key", fmt.Sprintf("/tmp/tls/%s.key", certName),
		)
	}
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	curlErr := podExec(t, *clientPod, &stdout, &stderr, cmd)
	stdoutStr := stdout.String()
	t.Logf("command: %s\nstdout:\n%s\n\nstderr:\n%s\n",
		strings.Join(cmd, " "), stdoutStr, stderr.String())
	// Try to parse the http status code even if curl returns an error; it may still be relevant.
	httpStatusCode := stdoutStr[len(stdoutStr)-3:]
	httpStatusCodeInt, err := strconv.ParseInt(httpStatusCode, 10, 64)
	if err != nil {
		// If parsing the status code returns an error but curl also returned an error, just send the curl one.
		if curlErr != nil {
			return -1, curlErr
		}
		return -1, err
	}
	if curlErr != nil {
		return httpStatusCodeInt, curlErr
	}
	switch httpStatusCode[0] {
	case '0', '4', '5':
		return httpStatusCodeInt, fmt.Errorf("got HTTP %s status code", httpStatusCode)
	default:
		return httpStatusCodeInt, nil
	}
}
