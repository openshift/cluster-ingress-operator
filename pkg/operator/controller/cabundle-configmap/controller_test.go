package cabundleconfigmap

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/events/eventstesting"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/test/unit"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// fakeClientRecorder is a fake client that records adds, updates, and deletes.
type fakeClientRecorder struct {
	client.Client
	*testing.T

	added   []client.Object
	updated []client.Object
	deleted []client.Object
}

func (c *fakeClientRecorder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	err := c.Client.Create(ctx, obj, opts...)
	if err == nil {
		c.added = append(c.added, obj)
	}
	return err
}

func (c *fakeClientRecorder) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	err := c.Client.Delete(ctx, obj, opts...)
	if err == nil {
		c.deleted = append(c.deleted, obj)
	}
	return err
}

func (c *fakeClientRecorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	err := c.Client.Update(ctx, obj, opts...)
	if err == nil {
		c.updated = append(c.updated, obj)
	}
	return err
}

// getCABundleConfigMapControllerConfig initializes Config for tests.
func getCABundleConfigMapControllerConfig() Config {
	return Config{
		ServiceCAConfigMapName: controller.ServiceCAConfigMapName(),
		AdminCAConfigMapName:   controller.AdminCAConfigMapName(),
		IngressCAConfigMapName: controller.IngressCAConfigMapName(),
	}
}

// Test_New verifies that the controller's New method behaves
// as expected.
func Test_New(t *testing.T) {
	mgr, err := manager.New(&rest.Config{}, manager.Options{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = New(mgr, getCABundleConfigMapControllerConfig(), eventstesting.NewTestingEventRecorder(t))
	if err != nil {
		t.Fatal(err)
	}
}

// Test_Reconcile verifies that the controller's Reconcile method behaves
// correctly. The controller is expected to do the following:
//
//   - Create the target configmap ingress-ca-bundle in the openshift-ingress
//     namespace with the contents read from source configmaps service-ca-bundle
//     and admin-ca-bundle in the openshift-ingress and openshift-config namespaces
//     respectively.
//
//   - Update the target configmap when the source configmaps are updated.
//
//   - Validate the CA certificates in both service-ca-bundle and admin-ca-bundle
//     for the validity period, signature algorithm, duplicates, is a CA cert.
//
// The test calls the controller's Reconcile method with a fake client that has
// suitable fake IngressController and configmap objects for the various test
// cases to verify that the controller correctly performs the above tasks.
func Test_Reconcile(t *testing.T) {
	adminCACertificate, err := os.ReadFile("./testdata/admin_ca.crt")
	if err != nil {
		t.Fatalf("failed to read admin-ca certificate: %v", err)
	}
	serviceCACertificate, err := os.ReadFile("./testdata/service_ca.crt")
	if err != nil {
		t.Fatalf("failed to read service-ca certificate: %v", err)
	}

	var (
		invalidCABundleConfigMapKeyName = "ca.crt"

		adminCABundle = map[string]string{
			adminCABundleConfigMapKeyName: string(adminCACertificate),
		}
		emptyAdminCABundle   = map[string]string{}
		invalidAdminCABundle = map[string]string{
			invalidCABundleConfigMapKeyName: string(adminCACertificate),
		}
		serviceCABundle = map[string]string{
			serviceCABundleConfigMapKeyName: string(serviceCACertificate),
		}
		emptyServiceCABundle   = map[string]string{}
		invalidServiceCABundle = map[string]string{
			invalidCABundleConfigMapKeyName: string(serviceCACertificate),
		}
		ingressCABundleWithOnlyServiceCA = map[string]string{
			ingressCABundleConfigMapKeyName: string(serviceCACertificate),
		}
		ingressCABundleWithBothServiceAndAdminCA = map[string]string{
			ingressCABundleConfigMapKeyName: string(serviceCACertificate) + string(adminCACertificate),
		}
	)

	tests := []struct {
		name             string
		request          reconcile.Request
		existingObjects  []runtime.Object
		expectCreate     []client.Object
		expectUpdate     []client.Object
		expectDelete     []client.Object
		expectError      string
		interceptorFuncs interceptor.Funcs
	}{
		{
			name: "do nothing if admin CA bundle configmap is not specified",
			request: reconcile.Request{
				NamespacedName: controller.ServiceCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "create the ingress CA bundle configmap if it is absent and the admin CA bundle configmap is present",
			request: reconcile.Request{
				NamespacedName: controller.AdminCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
			},
			expectCreate: []client.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithBothServiceAndAdminCA).
					Build(),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "update the ingress CA bundle configmap when admin CA bundle configmap is present but differs",
			request: reconcile.Request{
				NamespacedName: controller.IngressCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithBothServiceAndAdminCA).
					Build(),
			},
			expectDelete: []client.Object{},
		},
		// Negative test cases
		{
			name: "creating ingress CA bundle configmap fails",
			request: reconcile.Request{
				NamespacedName: controller.ServiceCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			interceptorFuncs: interceptor.Funcs{
				Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if obj.GetName() == controller.IngressCAConfigMapName().Name {
						return fmt.Errorf("failed to create %s", obj.GetName())
					}
					return client.Create(ctx, obj, opts...)
				},
			},
			expectError: "failed to ensure ingress CA bundle configmap: failed to create ingress-ca-bundle configmap: failed to create ingress-ca-bundle",
		},
		{
			name: "updating ingress CA bundle configmap fails",
			request: reconcile.Request{
				NamespacedName: controller.AdminCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			interceptorFuncs: interceptor.Funcs{
				Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if obj.GetName() == controller.IngressCAConfigMapName().Name {
						return fmt.Errorf("failed to update %s", obj.GetName())
					}
					return client.Update(ctx, obj, opts...)
				},
			},
			expectError: "failed to ensure ingress CA bundle configmap: failed to update ingress-ca-bundle configmap: failed to update ingress-ca-bundle",
		},
		{
			name: "failed to fetch ingress CA bundle configmap",
			request: reconcile.Request{
				NamespacedName: controller.IngressCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			interceptorFuncs: interceptor.Funcs{
				Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if key.Name == controller.IngressCAConfigMapName().Name {
						return fmt.Errorf("failed to get %s", key.Name)
					}
					return client.Get(ctx, key, obj, opts...)
				},
			},
			expectError: "failed to ensure ingress CA bundle configmap: failed to get ingress-ca-bundle",
		},
		{
			name: "failed to fetch service CA bundle configmap",
			request: reconcile.Request{
				NamespacedName: controller.ServiceCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			interceptorFuncs: interceptor.Funcs{
				Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if key.Name == controller.ServiceCAConfigMapName().Name {
						return fmt.Errorf("failed to get %s", key.Name)
					}
					return client.Get(ctx, key, obj, opts...)
				},
			},
			expectError: "failed to ensure ingress CA bundle configmap: failed to get service-ca-bundle",
		},
		{
			name: "failed to fetch admin CA bundle configmap",
			request: reconcile.Request{
				NamespacedName: controller.AdminCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			interceptorFuncs: interceptor.Funcs{
				Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if key.Name == controller.AdminCAConfigMapName().Name {
						return fmt.Errorf("failed to get %s", key.Name)
					}
					return client.Get(ctx, key, obj, opts...)
				},
			},
			expectError: "failed to ensure ingress CA bundle configmap: failed to get admin-ca-bundle",
		},
		{
			name: "empty service CA bundle configmap",
			request: reconcile.Request{
				NamespacedName: controller.IngressCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(emptyServiceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectError:  "failed to ensure ingress CA bundle configmap: service-ca-bundle does not contain any config data",
		},
		{
			name: "invalid service CA bundle configmap",
			request: reconcile.Request{
				NamespacedName: controller.AdminCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(invalidServiceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(adminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectError:  "failed to ensure ingress CA bundle configmap: service-ca-bundle does not contain \"service-ca.crt\" key",
		},
		{
			name: "empty admin CA bundle configmap",
			request: reconcile.Request{
				NamespacedName: controller.IngressCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(emptyAdminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "invalid admin CA bundle configmap",
			request: reconcile.Request{
				NamespacedName: controller.AdminCAConfigMapName(),
			},
			existingObjects: []runtime.Object{
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.ServiceCAConfigMapName()).
					WithKey(serviceCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.AdminCAConfigMapName()).
					WithKey(invalidAdminCABundle).
					Build(),
				unit.NewCABundleConfigMapBuilder().
					WithNamespacedName(controller.IngressCAConfigMapName()).
					WithKey(ingressCABundleWithOnlyServiceCA).
					Build(),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
	}

	scheme := runtime.NewScheme()
	if err := operatorv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				WithInterceptorFuncs(tc.interceptorFuncs).
				Build()
			cl := &fakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
			reconciler := &reconciler{
				client: cl,
				config: Config{
					ServiceCAConfigMapName: controller.ServiceCAConfigMapName(),
					AdminCAConfigMapName:   controller.AdminCAConfigMapName(),
					IngressCAConfigMapName: controller.IngressCAConfigMapName(),
				},
				eventRecorder: eventstesting.NewTestingEventRecorder(t),
			}
			res, err := reconciler.Reconcile(context.Background(), tc.request)
			if tc.expectError != "" {
				assert.Equal(t, tc.expectError, fmt.Sprintf("%v", err))
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, reconcile.Result{}, res)
			cmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Annotations", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectDelete, cl.deleted, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual deletes: %s", diff)
			}
		})
	}
}

// Test_validateCACertificateBundle verifies that the controller's validateCACertificateBundle
// method behaves as expected. The test calls the controller's validateCACertificateBundle
// method with a predefined ca-bundle.crt which contains certificates for verify various
// CA certificate specific scenarios.
func Test_ValidateCACertificate(t *testing.T) {
	malformedCACert := `-----BEGIN CERTIFICATE-----
MIIDYjCCAkqgAwIBAgIUL1IgdSh+zUvv1XhMZDKtJye3030wDQYJKoZIhvcNAQEL
BQAwaDELMAkGA1UEBhMCSU4xCzAJBgNVBAgMAktBMQwwCgYDVQQHDANNWVMxDzAN
ZXNzLW9wZXJhdG9yMB4XDTIzMTIxNzA3NTA1OVoXDTQzMTIxMjA3NTA1OVowVDES
DQYDVQQKDAZSZWRIYXQxEjAQBgNVBAsMCU9wZW5TaGlmdDCCASIwDQYJKoZIhvcN
AQEBBQADggEPADCCAQoCggEBAMfGxuAYBcAg+R/8LUlGhX4KEHdmxSo/It2HQK+/
fQXdIWz44+U6JDMlKgUfc1pPMWQ4FcAQxlFDCYkjmS7h3dm2mYeCfkoz+YJ/LNpe
sHfcmpgW5Lma5tLVxD9Br56ocvCEuofF2MmiPQNAUnoaaWXtXwoDuR6BdQhbjrw6
GKICCvkJgFInZCzbrm1Z7vrWbP+qe1NYyo54D7tvC2q8JXK8itEKOlOpGSs3QKyv
4qoD+E5q
-----END CERTIFICATE-----`
	caCertBundleName := "test-ca-bundle"
	caCertBundle, err := os.ReadFile("./testdata/ca_bundle.crt")
	if err != nil {
		t.Fatalf("failed to read ca_bundle file: %v", err)
	}
	expectedCACertBundle, err := os.ReadFile("./testdata/parsed_ca_bundle.crt")
	if err != nil {
		t.Fatalf("failed to read parsed_ca_bundle file: %v", err)
	}
	expectedCACerts := parseCertificateBundle(t, expectedCACertBundle)

	tests := []struct {
		name         string
		caCertBundle []byte
		wantErr      string
	}{
		{
			name:         "validating CA certificate bundle fails",
			caCertBundle: []byte(malformedCACert),
			wantErr:      `failed to parse CA certificate in test-ca-bundle bundle: x509: malformed certificate`,
		},
		{
			name:         "validating CA certificate bundle succeeds",
			caCertBundle: caCertBundle,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			cl := &fakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
			reconciler := &reconciler{
				client: cl,
				config: Config{
					ServiceCAConfigMapName: controller.ServiceCAConfigMapName(),
					AdminCAConfigMapName:   controller.AdminCAConfigMapName(),
					IngressCAConfigMapName: controller.IngressCAConfigMapName(),
				},
				eventRecorder: eventstesting.NewTestingEventRecorder(t),
			}

			certBuffer := new(bytes.Buffer)
			totalParsedCerts, totalValidCerts, err := reconciler.sanitizeCACertificateBundle(caCertBundleName, tc.caCertBundle, certBuffer)
			if len(tc.wantErr) != 0 {
				if err == nil {
					t.Errorf("error was expected, want: %s", tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("error was not expected, got: %v", err)
			}
			// hardcoded based on the valid certificates in testdata/ca_bundle.crt
			if totalParsedCerts != 7 {
				t.Errorf("totalParsedCerts count did not match, got: %d", totalParsedCerts)
			}
			// hardcoded based on the valid certificates in testdata/parsed_ca_bundle.crt
			if totalValidCerts != 5 {
				t.Errorf("totalValidCerts count did not match, got: %d", totalValidCerts)
			}
			if bytes.Compare(certBuffer.Bytes(), expectedCACertBundle) != 0 {
				t.Errorf("validated CA certificate bundle does not contain all expected CA certificates\n%s", certBuffer.String())
				validatedCACerts := parseCertificateBundle(t, certBuffer.Bytes())
				certs := mergeMaps(expectedCACerts, validatedCACerts)
				for k, v := range certs {
					if _, ok := validatedCACerts[k]; !ok {
						t.Errorf("certificate(%s-%s) expected in the validated list", k, v)
					} else {
						if _, ok := expectedCACerts[k]; !ok {
							t.Errorf("certificate(%s-%s) not expected in the validated list", k, v)
						}
					}
				}
			}
		})
	}
}

func parseCertificateBundle(t *testing.T, caCertBundle []byte) map[string]string {
	t.Helper()
	parsedCerts := make(map[string]string)
	for block, rem := pem.Decode(caCertBundle); block != nil; block, rem = pem.Decode(rem) {
		switch block.Type {
		case "CERTIFICATE":
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Errorf("failed to parse test CA certificate bundle: %v", err)
			}
			certID := getCertificatePrintId(cert)
			parsedCerts[certID] = cert.Subject.CommonName
		default:
			t.Errorf("test certificate bundle has non-certificate type: %s", block.Type)
			continue
		}
	}
	return parsedCerts
}

func mergeMaps(src1, src2 map[string]string) map[string]string {
	src := make(map[string]string)
	for k, v := range src1 {
		src[k] = v
	}
	for k, v := range src2 {
		src[k] = v
	}
	return src
}
