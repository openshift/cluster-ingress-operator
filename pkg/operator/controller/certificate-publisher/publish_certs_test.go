package certificatepublisher

import (
	"bytes"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newSecret returns a secret with the specified name and with data fields
// "tls.crt" and "tls.key" set to the secret's name.  Note that the values for
// "tls.crt" and "tls.key" are not valid PEM data.
func newSecret(name string) corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: map[string][]byte{
			"tls.crt": []byte(name),
			"tls.key": []byte(name),
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

		ic1             = newIngressController("ic1", "s1", "dom1", true)
		ic2             = newIngressController("ic2", "s2", "dom2", true)
		s1              = newSecret("s1")
		s2              = newSecret("s2")
		defaultCertData = bytes.Join([][]byte{
			defaultCert.Data["tls.crt"],
			defaultCert.Data["tls.key"],
		}, nil)
		customDefaultCertData = bytes.Join([][]byte{
			customDefaultCert.Data["tls.crt"],
			customDefaultCert.Data["tls.key"],
		}, nil)
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
