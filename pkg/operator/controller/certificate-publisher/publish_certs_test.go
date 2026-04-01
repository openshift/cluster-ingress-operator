package certificatepublisher

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// generatePEMKeyPair generates a unique self-signed certificate and key in PEM
// format.  The certificate's CN is set to the provided name so that each call
// produces distinct PEM data, preserving the ability of table-driven tests to
// verify which secret was selected.
func generatePEMKeyPair(name string) (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("failed to generate key for %q: %v", name, err))
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(fmt.Sprintf("failed to create certificate for %q: %v", name, err))
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal key for %q: %v", name, err))
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// newSecret returns a secret with the specified name and with data fields
// "tls.crt" and "tls.key" set to valid PEM data unique to the secret name.
func newSecret(name string) corev1.Secret {
	certPEM, keyPEM := generatePEMKeyPair(name)
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}
}

// newIngressController returns a new ingresscontroller with the specified name,
// default certificate secret name (or nil if empty), and ingress domain, for
// use as a test input.
func newIngressController(name, defaultCertificateSecretName, domain string, admitted bool) operatorv1.IngressController {
	admittedStatus := operatorv1.ConditionFalse
	if admitted {
		admittedStatus = operatorv1.ConditionTrue
	}
	ingresscontroller := operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.IngressControllerStatus{
			Conditions: []operatorv1.OperatorCondition{{
				Type:   ingress.IngressControllerAdmittedConditionType,
				Status: admittedStatus,
			}},
			Domain: domain,
		},
	}
	if len(defaultCertificateSecretName) != 0 {
		ingresscontroller.Spec.DefaultCertificate = &corev1.LocalObjectReference{Name: defaultCertificateSecretName}
	}
	return ingresscontroller
}

// Test_desiredRouterCertsGlobalSecret verifies that we get the expected global
// secret for the default ingresscontroller and for various combinations of
// ingresscontrollers and default certificate secrets.
func Test_desiredRouterCertsGlobalSecret(t *testing.T) {
	type testInputs struct {
		ingresses []operatorv1.IngressController
		secrets   []corev1.Secret
	}
	type testOutputs struct {
		secret *corev1.Secret
	}
	var (
		defaultCert = newSecret("router-certs-default")

		// defaultICWithDefaultCertUnspecified is an ingresscontroller
		// named "default" that does not specify a default certificate.
		// The operator should use the operator-generated default
		// certificate (the "default default certificate") in this case.
		defaultICWithDefaultCertUnspecified = newIngressController("default", "", "apps.my.devcluster.openshift.com", true)

		// defaultICWithDefaultCertSetToDefault is an ingresscontroller
		// named "default" that specifies an explicit reference for a
		// default certificate secret, where that secret is the same
		// default one that the operator generates when none is
		// specified (the "default default certificate").
		defaultICWithDefaultCertSetToDefault = newIngressController("default", "router-certs-default", "apps.my.devcluster.openshift.com", true)

		customDefaultCert = newSecret("custom-router-certs-default")

		// defaultICWithDefaultCertSetToCustom is an ingresscontroller
		// named "default" that specifies a reference for a default
		// certificate secret where that secret is a custom one (a
		// "custom default certificate").
		defaultICWithDefaultCertSetToCustom = newIngressController("default", "custom-router-certs-default", "apps.my.devcluster.openshift.com", true)

		// customICWithClusterIngressDomain is an ingresscontroller
		// named "custom" that specifies a reference for a default
		// certificate secret where that secret is a custom one.  The
		// ingresscontroller also specifies the cluster ingress domain
		// (which is usually owned by the "default" ingresscontroller.
		customICWithClusterIngressDomain = newIngressController("custom", "custom-router-certs-default", "apps.my.devcluster.openshift.com", true)

		// invalidCustomICWithClusterIngressDomain is the same as
		// customICWithClusterIngressDomain except that it has not been
		// admitted by the operator.
		invalidCustomICWithClusterIngressDomain = newIngressController("custom", "custom-router-certs-default", "apps.my.devcluster.openshift.com", false)

		ic1                   = newIngressController("ic1", "s1", "dom1", true)
		ic2                   = newIngressController("ic2", "s2", "dom2", true)
		s1                    = newSecret("s1")
		s2                    = newSecret("s2")
		defaultCertData       = joinPEMBlocks(defaultCert.Data["tls.crt"], defaultCert.Data["tls.key"])
		customDefaultCertData = joinPEMBlocks(customDefaultCert.Data["tls.crt"], customDefaultCert.Data["tls.key"])
	)
	testCases := []struct {
		description string
		inputs      testInputs
		output      testOutputs
	}{
		{
			description: "default certificate, implicit",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertUnspecified},
				[]corev1.Secret{defaultCert},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": defaultCertData},
				},
			},
		},
		// spec.defaultCertificate.name can specify the name of the
		// operator-generated certificate; see
		// <https://bugzilla.redhat.com/show_bug.cgi?id=1912922>.
		{
			description: "default certificate, explicit",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertSetToDefault},
				[]corev1.Secret{defaultCert},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": defaultCertData},
				},
			},
		},
		{
			description: "custom certificate",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertSetToCustom},
				[]corev1.Secret{defaultCert, customDefaultCert},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": customDefaultCertData},
				},
			},
		},
		{
			description: "custom certificate, with secrets having the custom one and then default one",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertSetToCustom},
				[]corev1.Secret{customDefaultCert, defaultCert},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": customDefaultCertData},
				},
			},
		},
		// Fall back to the operator-generated default certificate if
		// the specified secret doesn't exist; see
		// <https://bugzilla.redhat.com/show_bug.cgi?id=1887441>.
		{
			description: "missing custom certificate",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertSetToCustom},
				[]corev1.Secret{defaultCert},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": defaultCertData},
				},
			},
		},
		{
			description: "custom ingresscontroller with the cluster ingress domain",
			inputs: testInputs{
				[]operatorv1.IngressController{customICWithClusterIngressDomain},
				[]corev1.Secret{customDefaultCert},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": customDefaultCertData},
				},
			},
		},
		{
			description: "default certificate and custom ingresscontroller that conflicts on domain",
			inputs: testInputs{
				[]operatorv1.IngressController{invalidCustomICWithClusterIngressDomain, defaultICWithDefaultCertUnspecified},
				[]corev1.Secret{defaultCert, customDefaultCert},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": defaultCertData},
				},
			},
		},
		{
			description: "no ingresses",
			inputs: testInputs{
				[]operatorv1.IngressController{},
				[]corev1.Secret{},
			},
			output: testOutputs{nil},
		},
		{
			description: "no secrets",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertUnspecified},
				[]corev1.Secret{},
			},
			output: testOutputs{nil},
		},
		{
			description: "missing secret",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertUnspecified},
				[]corev1.Secret{s1, s2},
			},
			output: testOutputs{nil},
		},
		{
			description: "extra secret",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertUnspecified, ic2},
				[]corev1.Secret{s1, defaultCert, s2},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": defaultCertData},
				},
			},
		},
		{
			description: "perfect match",
			inputs: testInputs{
				[]operatorv1.IngressController{defaultICWithDefaultCertUnspecified, ic1, ic2},
				[]corev1.Secret{defaultCert, s1, s2},
			},
			output: testOutputs{
				&corev1.Secret{
					Data: map[string][]byte{"apps.my.devcluster.openshift.com": defaultCertData},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			expected := tc.output.secret
			actual, err := desiredRouterCertsGlobalSecret(tc.inputs.secrets, tc.inputs.ingresses, "openshift-ingress", "apps.my.devcluster.openshift.com")
			if err != nil {
				t.Errorf("failed to get desired router-ca global secret: %v", err)
			}
			if expected == nil || actual == nil {
				if expected != nil {
					t.Errorf("expected %v, got nil", expected)
				}
				if actual != nil {
					t.Errorf("expected nil, got %v", actual)
				}
			}
			if expected != nil && actual != nil {
				if !routerCertsSecretsEqual(expected, actual) {
					t.Errorf("expected %v, got %v", expected, actual)
				}
			}
		})
	}
}

// generateTestCertAndKey creates a self-signed cert and private key in PEM
// format.  If noTrailingNewline is true, the trailing newline is stripped from
// the certificate PEM to simulate certs produced by certain tools.
func generateTestCertAndKey(t *testing.T, noTrailingNewline bool) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.apps.example.com"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if noTrailingNewline {
		certPEM = bytes.TrimRight(certPEM, "\n")
	}

	return certPEM, keyPEM
}

// Test_desiredRouterCertsGlobalSecret_PEMNewline verifies that
// desiredRouterCertsGlobalSecret produces a valid PEM bundle regardless of
// whether tls.crt has a trailing newline, and rejects malformed PEM input.
// Previously, bytes.Join with a nil separator would concatenate the END
// CERTIFICATE and BEGIN PRIVATE KEY markers on the same line when tls.crt
// lacked a trailing newline, producing a malformed PEM bundle that downstream
// consumers could not parse.
func Test_desiredRouterCertsGlobalSecret_PEMNewline(t *testing.T) {
	domain := "apps.my.devcluster.openshift.com"

	testCases := []struct {
		name              string
		noTrailingNewline bool
		mutateCertPEM     func([]byte) []byte
		mutateKeyPEM      func([]byte) []byte
		wantErrSubstring  string
	}{
		{
			name:              "cert with trailing newline produces valid PEM",
			noTrailingNewline: false,
		},
		{
			name:              "cert without trailing newline produces valid PEM",
			noTrailingNewline: true,
		},
		{
			name: "malformed key PEM returns error",
			mutateKeyPEM: func([]byte) []byte {
				return []byte("not a PEM block")
			},
			wantErrSubstring: "failed to decode PEM block",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			certPEM, keyPEM := generateTestCertAndKey(t, tc.noTrailingNewline)
			if tc.mutateCertPEM != nil {
				certPEM = tc.mutateCertPEM(certPEM)
			}
			if tc.mutateKeyPEM != nil {
				keyPEM = tc.mutateKeyPEM(keyPEM)
			}

			secret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "router-certs-default",
				},
				Data: map[string][]byte{
					"tls.crt": certPEM,
					"tls.key": keyPEM,
				},
			}

			ic := newIngressController("default", "", domain, true)

			result, err := desiredRouterCertsGlobalSecret(
				[]corev1.Secret{secret},
				[]operatorv1.IngressController{ic},
				"openshift-ingress",
				domain,
			)
			if len(tc.wantErrSubstring) != 0 {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstring) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrSubstring, err)
				}
				if result != nil {
					t.Fatalf("expected no secret on error, got %#v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("expected a secret, got nil")
			}

			bundleData := result.Data[domain]

			// The bundle must not contain concatenated PEM markers
			if strings.Contains(string(bundleData), "-----END CERTIFICATE----------BEGIN") {
				t.Errorf("PEM bundle contains concatenated markers:\n%s", string(bundleData))
			}

			// The bundle must contain exactly a CERTIFICATE block followed by the key block.
			firstBlock, rest := pem.Decode(bundleData)
			if firstBlock == nil {
				t.Fatalf("expected first PEM block, got nil\nPEM bundle:\n%s", string(bundleData))
			}
			if firstBlock.Type != "CERTIFICATE" {
				t.Fatalf("expected first PEM block type CERTIFICATE, got %q\nPEM bundle:\n%s", firstBlock.Type, string(bundleData))
			}

			secondBlock, rest := pem.Decode(rest)
			if secondBlock == nil {
				t.Fatalf("expected second PEM block, got nil\nPEM bundle:\n%s", string(bundleData))
			}
			if secondBlock.Type != "EC PRIVATE KEY" {
				t.Fatalf("expected second PEM block type EC PRIVATE KEY, got %q\nPEM bundle:\n%s", secondBlock.Type, string(bundleData))
			}

			if len(bytes.TrimSpace(rest)) != 0 {
				t.Fatalf("expected exactly 2 PEM blocks, found trailing data %q\nPEM bundle:\n%s", string(rest), string(bundleData))
			}
		})
	}
}

// Test_joinPEMBlocks verifies that joinPEMBlocks produces well-formed output
// for various input combinations.
func Test_joinPEMBlocks(t *testing.T) {
	withNewline := []byte("-----BEGIN CERTIFICATE-----\ndata\n-----END CERTIFICATE-----\n")
	withoutNewline := []byte("-----BEGIN CERTIFICATE-----\ndata\n-----END CERTIFICATE-----")
	keyBlock := []byte("-----BEGIN EC PRIVATE KEY-----\nkeydata\n-----END EC PRIVATE KEY-----\n")

	testCases := []struct {
		name   string
		blocks [][]byte
		check  func(t *testing.T, result []byte)
	}{
		{
			name:   "both blocks have trailing newlines",
			blocks: [][]byte{withNewline, keyBlock},
			check: func(t *testing.T, result []byte) {
				// Should not double up newlines
				if strings.Contains(string(result), "\n\n") {
					t.Error("unexpected double newline")
				}
			},
		},
		{
			name:   "first block missing trailing newline",
			blocks: [][]byte{withoutNewline, keyBlock},
			check: func(t *testing.T, result []byte) {
				if strings.Contains(string(result), "----------") {
					t.Error("markers concatenated without newline")
				}
			},
		},
		{
			name:   "empty block is skipped",
			blocks: [][]byte{nil, withNewline, {}, keyBlock},
			check: func(t *testing.T, result []byte) {
				if !bytes.Equal(result, append(withNewline, keyBlock...)) {
					t.Errorf("unexpected output: %q", result)
				}
			},
		},
		{
			name:   "all empty blocks",
			blocks: [][]byte{nil, {}},
			check: func(t *testing.T, result []byte) {
				if len(result) != 0 {
					t.Errorf("expected empty output, got %q", result)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := joinPEMBlocks(tc.blocks...)
			tc.check(t, result)
		})
	}
}
