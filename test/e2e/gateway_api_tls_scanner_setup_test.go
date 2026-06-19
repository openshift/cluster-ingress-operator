//go:build e2e
// +build e2e

package e2e

import (
	"context"
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

	err = assertExpectedDNSRecords(t, map[expectedDnsRecord]bool{
		{dnsName: hostname + ".", gatewayName: gateway.Name}: true,
	})
	require.NoError(t, err, "DNSRecord never got ready")

	assertProxyDeployCustomConfigurations(t, gateway.Namespace, gateway.Name, string(gateway.Spec.GatewayClassName))

	t.Logf("Gateway %s/%s is ready for TLS scanning (resources intentionally left in place)", gateway.Namespace, gateway.Name)
}

// ensureGatewayTLSSecret creates a self-signed TLS secret for the Gateway
// HTTPS listener if it does not already exist.
func ensureGatewayTLSSecret(t *testing.T, namespace, name, dnsName string) (*corev1.Secret, error) {
	t.Helper()

	certPEM, keyPEM, err := generateServerTLSKeyPair(dnsName)
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
		},
	}
	if err := createOrGetWithRetry(t, context.Background(), secret, DefaultRetryTimeout); err != nil {
		return nil, fmt.Errorf("failed to create TLS secret %s/%s: %w", namespace, name, err)
	}
	return secret, nil
}

// ensureHTTPSGateway creates a Gateway with an HTTPS listener and an
// infrastructure "app" label matching the Gateway name so tls-scanner's
// COMPONENT_FILTER can select the generated Envoy pods.
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
					// COMPONENT_FILTER matches app / component / app.kubernetes.io/name.
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

	if err := createOrGetWithRetry(t, context.Background(), gateway, DefaultRetryTimeout); err != nil {
		return nil, fmt.Errorf("failed to create gateway %s/%s: %w", namespace, name, err)
	}
	return gateway, nil
}

// generateServerTLSKeyPair returns PEM-encoded certificate and key material
// suitable for a Gateway HTTPS listener Secret.
func generateServerTLSKeyPair(dnsName string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("failed to generate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
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

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("failed to create certificate: %w", err)
	}
	certs, err := x509.ParseCertificates(der)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse certificate: %w", err)
	}
	if len(certs) != 1 {
		return "", "", fmt.Errorf("expected 1 certificate, got %d", len(certs))
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal private key: %w", err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyDER,
	}))

	return encodeCert(certs[0]), keyPEM, nil
}
