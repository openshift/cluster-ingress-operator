//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TestIngressCABundle is for verifying the ingress-ca-bundle surrounding scenarios, like
//  1. CA certificate in user-configured `admin-ca-bundle` is made available for router to
//     verify services' certificates.
//  2. OpenShift Service CA certificate injected by `service-ca-operator` to `service-ca-bundle`
//     is present in the `ingress-ca-bundle`.
//  3. On updating `admin-ca-bundle` or `service-ca-bundle`, operator detects changes
//     and updates `ingress-ca-bundle` and router reloads HAProxy.
func TestIngressCABundle(t *testing.T) {
	var (
		ctx = context.TODO()

		testServerNamespace           = "ingress-ca-bundle-test"
		ingressOperatorDeploymentName = types.NamespacedName{
			Name:      "ingress-operator",
			Namespace: controller.DefaultOperatorNamespace,
		}
		openshiftServerCA = types.NamespacedName{
			Name:      "openshift-service-ca.crt",
			Namespace: "openshift-config",
		}
		serverCABundleConfigMapName = types.NamespacedName{
			Name:      "test-server-ca-bundle",
			Namespace: testServerNamespace,
		}

		serviceName1      = "hello-world-1"
		svcTLSSecretName1 = types.NamespacedName{
			Name:      serviceName1,
			Namespace: testServerNamespace,
		}
		svcDeploymentName1 = types.NamespacedName{
			Name:      serviceName1,
			Namespace: testServerNamespace,
		}
		svcLabels1 = map[string]string{
			"app": serviceName1,
		}
		svcPort1 = int32(8082)
		svcName1 = types.NamespacedName{
			Name:      serviceName1,
			Namespace: testServerNamespace,
		}
		routeName1 = types.NamespacedName{
			Name:      serviceName1,
			Namespace: testServerNamespace,
		}

		serviceName2      = "hello-world-2"
		svcTLSSecretName2 = types.NamespacedName{
			Name:      serviceName2,
			Namespace: testServerNamespace,
		}
		svcDeploymentName2 = types.NamespacedName{
			Name:      serviceName2,
			Namespace: testServerNamespace,
		}
		svcLabels2 = map[string]string{
			"app": serviceName2,
		}
		svcPort2 = int32(8083)
		svcName2 = types.NamespacedName{
			Name:      serviceName2,
			Namespace: testServerNamespace,
		}
		routeName2 = types.NamespacedName{
			Name:      serviceName2,
			Namespace: testServerNamespace,
		}
	)

	svcCACert1, svcCAKey1, err := generateCAKeyPair("operator-e2e-1")
	if err != nil {
		t.Fatalf("failed to generate first server CA certificate: %v", err)
	}
	svcCert1, svcKey1, err := generateServerCertificate(svcCACert1, svcCAKey1, serviceName1, []string{serviceName1, fmt.Sprintf("%s.%s.svc", serviceName1, testServerNamespace)})
	if err != nil {
		t.Fatalf("failed to generate first server certificate: %v", err)
	}
	clientCert, clientKey, err := generateClientCertificate(svcCACert1, svcCAKey1, "test-client")
	if err != nil {
		t.Fatalf("failed to generate client certificate: %v", err)
	}
	svcCACert2, svcCAKey2, err := generateCAKeyPair("operator-e2e-2")
	if err != nil {
		t.Fatalf("failed to generate first server CA certificate: %v", err)
	}
	svcCert2, svcKey2, err := generateServerCertificate(svcCACert2, svcCAKey2, serviceName2, []string{serviceName2, fmt.Sprintf("%s.%s.svc", serviceName2, testServerNamespace)})
	if err != nil {
		t.Fatalf("failed to generate second server certificate: %v", err)
	}

	ingressOperator := &appsv1.Deployment{}
	t.Logf("fetching %s deployment", ingressOperatorDeploymentName)
	if err := kclient.Get(ctx, ingressOperatorDeploymentName, ingressOperator); err != nil {
		t.Fatalf("failed to get %s deployment", ingressOperatorDeploymentName)
	}
	var ingressOperatorImageName string
	for _, container := range ingressOperator.Spec.Template.Spec.Containers {
		if container.Name == ingressOperatorDeploymentName.Name {
			ingressOperatorImageName = container.Image
		}
	}
	if ingressOperatorImageName == "" {
		t.Fatalf("failed to fetch ingress operator image name in %s deployment", ingressOperator)
	}

	adminCABundleConfigMapName := controller.AdminCAConfigMapName()
	t.Logf("creating %s configmap", adminCABundleConfigMapName)
	adminCABundleConfigMap := buildConfigMap(adminCABundleConfigMapName,
		map[string]string{
			"ca-bundle.crt": encodeCert(svcCACert1),
		})
	if err := kclient.Create(ctx, adminCABundleConfigMap); err != nil {
		t.Fatalf("failed to create configmap %q: %v", adminCABundleConfigMapName, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, adminCABundleConfigMap); err != nil {
			t.Fatalf("failed to delete configmap %q: %v", adminCABundleConfigMapName, err)
		}
	})
	// router would be already active, and it takes time for CA changes to be propagated
	// Will provide a grace time here for changes to reflected in router pod.
	t.Logf("grace wait for admin-ca-bundle configmap changes to propagate to router pod")
	time.Sleep(time.Minute)

	// create test server namespace
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testServerNamespace,
		},
	}
	t.Logf("creating %s namespace", testServerNamespace)
	if err := kclient.Create(ctx, namespace); err != nil {
		t.Fatalf("failed to create namespace %q: %v", testServerNamespace, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, namespace); err != nil {
			t.Fatalf("failed to delete namespace %q: %v", testServerNamespace, err)
		}
	})

	// create a server CA bundle for sample server to verify router certificate.
	serviceCA := &corev1.ConfigMap{}
	if err := kclient.Get(ctx, openshiftServerCA, serviceCA); err != nil {
		t.Fatalf("failed to get configmap %q: %v", openshiftServerCA, err)
	}
	serverCA := serviceCA.Data["service-ca.crt"]

	serverCABundleConfigMap := buildConfigMap(serverCABundleConfigMapName,
		map[string]string{
			"ca-bundle.crt": fmt.Sprintf("%s\n%s\n%s", serverCA, encodeCert(svcCACert1), encodeCert(svcCACert2)),
		})
	t.Logf("creating %s configmap", serverCABundleConfigMapName)
	if err := kclient.Create(ctx, serverCABundleConfigMap); err != nil {
		t.Fatalf("failed to create configmap %q: %v", serverCABundleConfigMapName, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, serverCABundleConfigMap); err != nil {
			t.Fatalf("failed to delete configmap %q: %v", serverCABundleConfigMapName, err)
		}
	})

	// create a TLS secret for server.
	svcTLSSecret1 := buildTLSSecret(svcTLSSecretName1, []byte(encodeCert(svcCert1)), []byte(encodeKey(svcKey1)))
	t.Logf("creating %s secret", svcTLSSecretName1)
	if err := kclient.Create(ctx, svcTLSSecret1); err != nil {
		t.Fatalf("failed to create secret %q: %v", svcTLSSecretName1, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, svcTLSSecret1); err != nil {
			t.Fatalf("failed to delete secret %q: %v", svcTLSSecretName1, err)
		}
	})

	// create a TLS secret for server.
	svcTLSSecret2 := buildTLSSecret(svcTLSSecretName2, []byte(encodeCert(svcCert2)), []byte(encodeKey(svcKey2)))
	t.Logf("creating %s secret", svcTLSSecretName2)
	if err := kclient.Create(ctx, svcTLSSecret2); err != nil {
		t.Fatalf("failed to create secret %q: %v", svcTLSSecretName2, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, svcTLSSecret2); err != nil {
			t.Fatalf("failed to delete secret %q: %v", svcTLSSecretName2, err)
		}
	})

	// create a test server for verifying defaultDestinationCA scenarios.
	svcDeployment1 := buildServerDeployment(svcDeploymentName1, svcLabels1, svcPort1, ingressOperatorImageName, svcTLSSecretName1.Name)
	t.Logf("creating %s deployment", svcDeploymentName1)
	if err := kclient.Create(ctx, svcDeployment1); err != nil {
		t.Fatalf("failed to create deployment %q: %v", svcDeploymentName1, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, svcDeployment1); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete deployment %q: %v", svcDeploymentName1, err)
		}
	})

	// wait till deployment is available.
	t.Logf("waiting for %s deployment to become ready", svcDeploymentName1)
	if err := waitForDeploymentComplete(t, kclient, svcDeployment1, 3*time.Minute); err != nil {
		t.Fatalf("timed out waiting for deployment %q to become ready: %v", svcDeployment1, err)
	}

	// create a test server for verifying defaultDestinationCA scenarios.
	svcDeployment2 := buildServerDeployment(svcDeploymentName2, svcLabels2, svcPort2, ingressOperatorImageName, svcTLSSecretName2.Name)
	t.Logf("creating %s deployment", svcDeploymentName2)
	if err := kclient.Create(ctx, svcDeployment2); err != nil {
		t.Fatalf("failed to create deployment %q: %v", svcDeploymentName2, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, svcDeployment2); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete deployment %q: %v", svcDeploymentName2, err)
		}
	})

	// wait till deployment is available.
	t.Logf("waiting for %s deployment to become ready", svcDeploymentName2)
	if err := waitForDeploymentComplete(t, kclient, svcDeployment2, 3*time.Minute); err != nil {
		t.Fatalf("timed out waiting for deployment %q to become ready: %v", svcDeploymentName2, err)
	}

	// create a service for the application server.
	svc1 := buildService(svcName1, svcLabels1, svcPort1)
	t.Logf("creating %s service", svcName1)
	if err := kclient.Create(ctx, svc1); err != nil {
		t.Fatalf("failed to create service %q: %v", svcName1, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, svc1); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete service %q: %v", svcName1, err)
		}
	})

	// create a service for the application server.
	svc2 := buildService(svcName2, svcLabels2, svcPort2)
	t.Logf("creating %s service", svcName2)
	if err := kclient.Create(ctx, svc2); err != nil {
		t.Fatalf("failed to create service %q: %v", svcName2, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, svc2); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete service %q: %v", svcName2, err)
		}
	})

	// create a route for the application server.
	route1 := buildReEncryptRoute(routeName1, svcName1.Name, svcLabels1, svcPort1, "/api")
	t.Logf("creating %s route", routeName1)
	if err := kclient.Create(ctx, route1); err != nil {
		t.Fatalf("failed to create route %q: %v", routeName1, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, route1); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete route %q: %v", routeName1, err)
		}
	})

	// create a route for the application server.
	route2 := buildReEncryptRoute(routeName2, svcName2.Name, svcLabels2, svcPort2, "/api")
	t.Logf("creating %s route", routeName2)
	if err := kclient.Create(ctx, route2); err != nil {
		t.Fatalf("failed to create route %q: %v", routeName2, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(ctx, route2); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete route %q: %v", routeName2, err)
		}
	})

	// check that the default ingress controller is ready
	t.Logf("waiting for %s ingress controller to be in desired state", defaultName)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("timed out waiting for %q ingress controller to be in desired state: %v", defaultName, err)
	}

	testCases := []struct {
		name                string
		routeName           types.NamespacedName
		updateAdminCABundle string
		statusCode          int
	}{
		{
			name:       fmt.Sprintf("access to %s success", serviceName1),
			routeName:  routeName1,
			statusCode: http.StatusOK,
		},
		{
			name:       fmt.Sprintf("access to %s fails, admin-ca-bundle not updated", serviceName2),
			routeName:  routeName2,
			statusCode: http.StatusServiceUnavailable,
		},
		{
			name:                fmt.Sprintf("access to %s success, admin-ca-bundle updated", serviceName2),
			routeName:           routeName2,
			updateAdminCABundle: encodeCert(svcCACert2),
			statusCode:          http.StatusOK,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.updateAdminCABundle != "" {
				caBundle := buildConfigMap(adminCABundleConfigMapName,
					map[string]string{
						"ca-bundle.crt": fmt.Sprintf("%s\n%s", encodeCert(svcCACert1), tc.updateAdminCABundle),
					})
				t.Logf("updating %s configmap", adminCABundleConfigMapName)
				if err := kclient.Update(ctx, caBundle); err != nil {
					t.Fatalf("failed to update configmap %q: %v", caBundle, err)
				}
				// grace time for configmap changes to propagate to pod
				t.Logf("grace wait after updating admin-ca-bundle with new CA for new changes to propagate to router pod")
				time.Sleep(time.Minute)
			}

			host, err := getRouteHostName(ctx, tc.routeName)
			if err != nil {
				t.Fatalf("failed to get route host name %q: %v", tc.routeName, err)
			}

			c, err := getHTTPClient(encodeCert(clientCert), encodeKey(clientKey), serverCA)
			if err != nil {
				t.Fatalf("failed to create http client: %v", err)
			}

			statusCode, err := queryServer(t, c, host, "/api", tc.statusCode)
			if err != nil {
				t.Errorf("failed to access server(%s): %v", host, err)
			}
			if statusCode != tc.statusCode {
				t.Errorf("access to server(%s) failed with code %d", host, statusCode)
			}
		})
	}
}

// generateCertificateSerialNumber generates and returns serial number required for
// certificate generation.
func generateCertificateSerialNumber() (*big.Int, error) {
	// generate serial number having 20 octets.
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 160)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	return serialNumber, err
}

// createCertificate generates and return certificate using the template provided.
func createCertificate(template, parent *x509.Certificate, pubKey *rsa.PublicKey, prvKey *rsa.PrivateKey) (*x509.Certificate, error) {
	derBytes, err := x509.CreateCertificate(rand.Reader, template, parent, pubKey, prvKey)
	if err != nil {
		return nil, err
	}

	certs, err := x509.ParseCertificates(derBytes)
	if err != nil {
		return nil, err
	}
	if len(certs) != 1 {
		return nil, fmt.Errorf("expected a single certificate from x509.ParseCertificates, got %d: %v", len(certs), certs)
	}

	return certs[0], nil
}

// generateCAKeyPair generates and returns a CA certificate and key.
func generateCAKeyPair(commonName string) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	serialNumber, err := generateCertificateSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	root := &x509.Certificate{
		Subject:               pkix.Name{CommonName: commonName},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		SerialNumber:          serialNumber,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	cert, err := createCertificate(root, root, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

// generateServerCertificate generates and returns a server certificate and key
// where the certificate is signed by the provided CA certificate.
func generateServerCertificate(caCert *x509.Certificate, caKey *rsa.PrivateKey, cn string, dnsDomains []string) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	serialNumber, err := generateCertificateSerialNumber()
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
		SerialNumber:          serialNumber,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsDomains,
	}

	cert, err := createCertificate(template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

// buildTLSSecret returns ConfigMap object with the provided data.
func buildConfigMap(name types.NamespacedName, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: data,
	}
}

// buildTLSSecret returns a TLS secret object with the provided certificate and key.
func buildTLSSecret(name types.NamespacedName, cert, key []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: map[string][]byte{
			corev1.TLSCertKey:       cert,
			corev1.TLSPrivateKeyKey: key,
		},
		Type: corev1.SecretTypeTLS,
	}
}

// buildServerDeployment returns deployment object with the provided TLS secret mounted as volume.
func buildServerDeployment(name types.NamespacedName, labels map[string]string, port int32, imageName, secretName string) *appsv1.Deployment {
	var (
		containerName          = "hello-world-server"
		serverKeyPairMountPath = "/etc/testServer/tls"
		serverCertificatePath  = filepath.Join(serverKeyPairMountPath, "tls.crt")
		serverPrivateKeyPath   = filepath.Join(serverKeyPairMountPath, "tls.key")
		volumeName             = "server-tls-key-pair"
	)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: nil,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            containerName,
							Image:           imageName,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{
								"ingress-operator",
								"serve-http2-test-server",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "TLS_CRT",
									Value: serverCertificatePath,
								},
								{
									Name:  "TLS_KEY",
									Value: serverPrivateKeyPath,
								},
								{
									Name:  "HTTPS_PORT",
									Value: fmt.Sprint(port),
								},
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "https",
									Protocol:      corev1.ProtocolTCP,
									ContainerPort: port,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      volumeName,
									MountPath: serverKeyPairMountPath,
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: volumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: secretName,
								},
							},
						},
					},
				},
			},
		},
	}
}

// buildService returns service object for the provided port.
func buildService(name types.NamespacedName, labels map[string]string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Protocol: corev1.ProtocolTCP,
				Port:     port,
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: port,
				},
			}},
			Selector: labels,
			Type:     corev1.ServiceTypeClusterIP,
		},
	}
}

// buildReEncryptRoute returns route object for the provided service.
func buildReEncryptRoute(name types.NamespacedName, svcName string, labels map[string]string, port int32, path string) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Labels:    labels,
		},
		Spec: routev1.RouteSpec{
			Path: path,
			Port: &routev1.RoutePort{
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: port,
				},
			},
			TLS: &routev1.TLSConfig{
				Termination: routev1.TLSTerminationReencrypt,
			},
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: svcName,
			},
		},
	}
}

// getRouteHostName returns the host name in the route object.
func getRouteHostName(ctx context.Context, name types.NamespacedName) (string, error) {
	route := &routev1.Route{}
	if err := kclient.Get(ctx, name, route); err != nil {
		return "", err
	}
	return route.Spec.Host, nil
}

func getHTTPClient(cert, key, caCert string) (*http.Client, error) {
	// load tls certificates
	clientTLSCert, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		return nil, fmt.Errorf("failed to load client key pair: %v", err)
	}

	// Configure the client to trust TLS server certs issued by a CA.
	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM([]byte(caCert)); !ok {
		return nil, fmt.Errorf("failed to add CA certificate to pool")
	}

	tlsConfig := &tls.Config{
		RootCAs:            certPool,
		Certificates:       []tls.Certificate{clientTLSCert},
		InsecureSkipVerify: true,
	}
	tr := &http.Transport{
		TLSClientConfig: tlsConfig,
	}
	c := &http.Client{
		Timeout:   1 * time.Minute,
		Transport: tr,
	}
	return c, nil
}

// queryServer is for querying the service through the route created for the same as an
// external client.
func queryServer(t *testing.T, httpClient *http.Client, host, path string, expectedStatusCode int) (int, error) {
	t.Helper()

	const (
		retries       = 10
		retryInterval = 20 * time.Second
	)

	var (
		resp       *http.Response
		statusCode int
		err        error
	)

	url := fmt.Sprintf("https://%s%s", host, path)
	for i := 1; i <= retries; i++ {
		resp, err = httpClient.Get(url)
		if err == nil && resp.StatusCode == expectedStatusCode {
			t.Logf("retry(%d): request to server successful: %d %s", i, resp.StatusCode, http.StatusText(resp.StatusCode))
			return resp.StatusCode, nil
		}
		if err != nil {
			t.Logf("retry(%d): failed to query server(%s): %v", i, url, err)
		}
		if resp != nil {
			t.Logf("retry(%d): request to server failed: %d %s", i, resp.StatusCode, http.StatusText(resp.StatusCode))
			statusCode = resp.StatusCode
		}
		t.Logf("retrying in %s", retryInterval)
		time.Sleep(retryInterval)
	}
	return statusCode, err
}
