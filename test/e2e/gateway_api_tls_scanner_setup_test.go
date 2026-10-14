//go:build e2e
// +build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// gatewayAPITLSScannerSetupEnv enables TestGatewayAPITLSScannerSetup.
	// The Makefile gatewayapi-tls-scanner-setup target sets this so the test
	// can provision leave-behind Gateway resources for the TLS scanner.
	gatewayAPITLSScannerSetupEnv = "GATEWAYAPI_TLS_SCANNER_SETUP"

	// defaultTLSScannerGatewayName matches the COMPONENT_FILTER / GATEWAY_NAME
	// used by the openshift/release tls-scanner-gatewayapi job.
	defaultTLSScannerGatewayName = "tls-scanner-gatewayapi"
)

// TestGatewayAPITLSScannerSetup provisions a Gateway with an HTTPS listener
// and TLS certificate in openshift-ingress for the CI TLS scanner.
//
// Unlike other Gateway API e2e tests, this intentionally does not clean up
// created resources: the subsequent tls-scanner-run step discovers the Envoy
// pods by COMPONENT_FILTER matching the Gateway infrastructure "app" label.
//
// Run via: make gatewayapi-tls-scanner-setup
// Optional: GATEWAY_NAME=<name> (defaults to tls-scanner-gatewayapi)
func TestGatewayAPITLSScannerSetup(t *testing.T) {
	if os.Getenv(gatewayAPITLSScannerSetupEnv) != "1" {
		t.Skipf("skipping: set %s=1 to provision leave-behind Gateway resources for the TLS scanner", gatewayAPITLSScannerSetupEnv)
	}

	// DNS publishing and AWS ELB provisioning are required for Gateway readiness
	// checks used below; skip on non-AWS platforms.
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("Skipping test: platform status is nil")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("Skipping test on platform %q: test requires AWS for load balancer and DNS publishing", infraConfig.Status.PlatformStatus.Type)
	}

	// Gateway API is GA; verify CRDs are present before provisioning.
	ensureCRDs(t)

	gatewayName := os.Getenv("GATEWAY_NAME")
	if gatewayName == "" {
		gatewayName = defaultTLSScannerGatewayName
	}
	secretName := gatewayName + "-cert"
	domain := gatewayName + ".gws." + dnsConfig.Spec.BaseDomain
	hostname := "*." + domain

	t.Logf("Provisioning TLS scanner Gateway %q in namespace %q (hostname %q)", gatewayName, operatorcontroller.DefaultOperandNamespace, hostname)

	gatewayClass, err := createGatewayClass(t, operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName)
	require.NoError(t, err, "failed to create GatewayClass")
	_, err = assertGatewayClassSuccessful(t, gatewayClass.Name)
	require.NoError(t, err, "GatewayClass was not accepted")

	_, err = ensureGatewayTLSSecret(t, operatorcontroller.DefaultOperandNamespace, secretName, hostname)
	require.NoError(t, err, "failed to create TLS secret")

	gateway, err := ensureHTTPSGateway(t, gatewayClass.Name, gatewayName, operatorcontroller.DefaultOperandNamespace, hostname, secretName)
	require.NoError(t, err, "failed to create Gateway")

	_, err = assertGatewaySuccessful(t, gateway.Namespace, gateway.Name)
	require.NoError(t, err, "Gateway was not accepted/programmed")

	err = assertGatewayInfrastructureLabelsPropagated(t, gateway.Namespace, gateway.Name, string(gateway.Spec.GatewayClassName), map[string]string{
		"app": gateway.Name,
	})
	require.NoError(t, err, "Gateway infrastructure labels were not propagated to Envoy pods")

	err = assertExpectedDNSRecords(t, map[expectedDnsRecord]dnsRecordExpectation{
		{dnsName: hostname + ".", gatewayName: gateway.Name}: expectDNSRecordPublished(),
	})
	require.NoError(t, err, "DNSRecord never got ready")

	assertProxyDeployCustomConfigurations(t, gateway.Namespace, gateway.Name, string(gateway.Spec.GatewayClassName))

	t.Logf("Gateway %s/%s is ready for TLS scanning (resources intentionally left in place)", gateway.Namespace, gateway.Name)
}

// ensureGatewayTLSSecret creates a CA-signed TLS secret for the Gateway
// HTTPS listener if it does not already exist. The secret includes the CA
// certificate (ca.crt) for client certificate verification.
func ensureGatewayTLSSecret(t *testing.T, namespace, name, dnsName string) (*corev1.Secret, error) {
	t.Helper()

	caCertPEM, _, certPEM, keyPEM, err := generateServerTLSKeyPair(dnsName)
	if err != nil {
		return nil, err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte(certPEM),
			"tls.key": []byte(keyPEM),
			"ca.crt":  []byte(caCertPEM),
		},
	}
	if err := createOrGetWithRetry(t, t.Context(), secret, DefaultRetryTimeout); err != nil {
		return nil, fmt.Errorf("failed to create TLS secret %s/%s: %w", namespace, name, err)
	}
	return secret, nil
}

// ensureHTTPSGateway creates a Gateway with an HTTPS listener and the given
// TLS secret, including an infrastructure "app" label matching the Gateway name.
func ensureHTTPSGateway(t *testing.T, gatewayClassName, name, namespace, hostname, secretName string) (*gatewayapiv1.Gateway, error) {
	t.Helper()

	host := gatewayapiv1.Hostname(hostname)
	fromNamespace := gatewayapiv1.FromNamespaces(allNamespaces)
	mode := gatewayapiv1.TLSModeTerminate

	gateway := &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(gatewayClassName),
			Infrastructure: &gatewayapiv1.GatewayInfrastructure{
				Labels: map[gatewayapiv1.LabelKey]gatewayapiv1.LabelValue{
					"app": gatewayapiv1.LabelValue(name),
				},
			},
			Listeners: []gatewayapiv1.Listener{{
				Name:     "https",
				Hostname: &host,
				Port:     443,
				Protocol: gatewayapiv1.HTTPSProtocolType,
				TLS: &gatewayapiv1.ListenerTLSConfig{
					Mode: &mode,
					CertificateRefs: []gatewayapiv1.SecretObjectReference{{
						Name: gatewayapiv1.ObjectName(secretName),
					}},
				},
				AllowedRoutes: &gatewayapiv1.AllowedRoutes{
					Namespaces: &gatewayapiv1.RouteNamespaces{From: &fromNamespace},
				},
			}},
		},
	}

	if err := createOrGetWithRetry(t, t.Context(), gateway, DefaultRetryTimeout); err != nil {
		return nil, fmt.Errorf("failed to create gateway %s/%s: %w", namespace, name, err)
	}
	return gateway, nil
}

// generateServerTLSKeyPair generates a CA certificate and a server certificate
// signed by that CA. It returns PEM-encoded CA cert, CA key, server cert, and
// server key suitable for a Gateway HTTPS listener Secret.
func generateServerTLSKeyPair(dnsName string) (caCertPEM, caKeyPEM, certPEM, keyPEM string, err error) {
	// Generate the CA key and self-signed CA certificate.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to generate CA key: %w", err)
	}

	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to generate CA serial: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"OpenShift E2E Testing"},
			CommonName:   "E2E Test CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to create CA certificate: %w", err)
	}
	caCerts, err := x509.ParseCertificates(caDER)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to parse CA certificate: %w", err)
	}
	if len(caCerts) != 1 {
		return "", "", "", "", fmt.Errorf("expected 1 CA certificate, got %d", len(caCerts))
	}
	caCert := caCerts[0]

	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to marshal CA private key: %w", err)
	}
	caKeyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: caKeyDER,
	}))

	// Generate the server key and certificate signed by the CA.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to generate server key: %w", err)
	}

	serverSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to generate server serial: %w", err)
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject: pkix.Name{
			Organization: []string{"OpenShift E2E Testing"},
			CommonName:   dnsName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{dnsName, "localhost"},
	}

	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to create server certificate: %w", err)
	}
	serverCerts, err := x509.ParseCertificates(serverDER)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to parse server certificate: %w", err)
	}
	if len(serverCerts) != 1 {
		return "", "", "", "", fmt.Errorf("expected 1 server certificate, got %d", len(serverCerts))
	}

	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to marshal server private key: %w", err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: serverKeyDER,
	}))

	return encodeCert(caCert), caKeyPEM, encodeCert(serverCerts[0]), keyPEM, nil
}

// generateClientKeyPair generates a client certificate signed by the given CA.
// The returned PEM-encoded cert has ExtKeyUsageClientAuth for mTLS.
func generateClientKeyPair(caCertPEM, caKeyPEM, commonName string) (clientCertPEM, clientKeyPEM string, err error) {
	caBlock, _ := pem.Decode([]byte(caCertPEM))
	if caBlock == nil {
		return "", "", fmt.Errorf("failed to decode CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	caKeyBlock, _ := pem.Decode([]byte(caKeyPEM))
	if caKeyBlock == nil {
		return "", "", fmt.Errorf("failed to decode CA key PEM")
	}
	caKeyParsed, err := x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse CA private key: %w", err)
	}
	caKey, ok := caKeyParsed.(*ecdsa.PrivateKey)
	if !ok {
		return "", "", fmt.Errorf("CA key is not ECDSA")
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate client key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("failed to generate client serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"OpenShift E2E Testing"},
			CommonName:   commonName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	clientDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to create client certificate: %w", err)
	}
	clientCerts, err := x509.ParseCertificates(clientDER)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse client certificate: %w", err)
	}
	if len(clientCerts) != 1 {
		return "", "", fmt.Errorf("expected 1 client certificate, got %d", len(clientCerts))
	}

	clientKeyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal client private key: %w", err)
	}
	clientKeyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: clientKeyDER,
	}))

	return encodeCert(clientCerts[0]), clientKeyPEM, nil
}
