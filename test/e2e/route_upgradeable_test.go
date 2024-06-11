//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	routeupgradeable "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/route-upgradeable"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	waitForAdminGateInterval = 3 * time.Second
	waitForAdminGateTimeout  = 3 * time.Minute
)

// TestRouteNotUpgradeableWithSha1 tests the route-upgradeable control loop by creating a route with a SHA1 certificate,
// expecting the Ingress Operator to create an admin gate, and update the route to SHA256 certificate to resolve the
// issue.
//
// This is a serial test because adding an admin gate could potentially interfere with other upgrade-related tests.
// Also, other tests create and delete router deployments, which would interfere with the status of the route that
// this test creates and updates.
func TestRouteNotUpgradeableWithSha1(t *testing.T) {
	// Make sure admin gate doesn't exist before starting test.
	if err := waitForAdminGate(t, routeupgradeable.RouteConfigAdminGate415Key, false, waitForAdminGateInterval, waitForAdminGateTimeout); err != nil {
		t.Fatalf("failed to observe initial admin gate value: %v", err)
	}

	// Generate SHA1 certificates
	ca, caKey, err := generateCA()
	if err != nil {
		t.Fatalf("failed to generate client CA certificate: %v", err)
	}
	certSha1, keySha1, err := generateCertificate(ca, caKey, "test", x509.SHA1WithRSA)
	if err != nil {
		t.Fatalf("failed to generate SHA1 certificate: %v", err)
	}

	t.Log("creating SHA1 route to add admin gate")
	routeSha1Name := types.NamespacedName{Name: "route-sha1", Namespace: operatorcontroller.DefaultOperandNamespace}
	routeSha1 := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      routeSha1Name.Name,
			Namespace: routeSha1Name.Namespace,
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: "foo",
			},
			TLS: &routev1.TLSConfig{
				Termination: routev1.TLSTerminationEdge,
				Certificate: encodeCert(certSha1),
				Key:         encodeKey(keySha1),
			},
		},
	}
	if err := kclient.Create(context.Background(), routeSha1); err != nil {
		t.Fatalf("failed to create route %s: %v", routeSha1Name, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(context.Background(), routeSha1); err != nil {
			t.Fatalf("failed to delete route %s: %v", routeSha1Name, err)
		}
	})

	// Poll for the admin gate, and wait until it is True.
	if err := waitForAdminGate(t, routeupgradeable.RouteConfigAdminGate415Key, true, waitForAdminGateInterval, waitForAdminGateTimeout); err != nil {
		t.Fatalf("admin gate observation error: %v", err)
	}

	// Now, resolve the route's certificate problem by replacing with supported SHA256 certs.
	t.Log("resolving SHA1 route issue to remove admin gate")
	certSha256, keySha256, err := generateCertificate(ca, caKey, "test", x509.SHA256WithRSA)
	if err != nil {
		t.Fatalf("failed to generate SHA256 certificate: %v", err)
	}

	if err := kclient.Get(context.Background(), routeSha1Name, routeSha1); err != nil {
		t.Fatalf("failed to get route %s: %v", routeSha1Name, err)
	}
	routeSha1.Spec.TLS.Certificate = encodeCert(certSha256)
	routeSha1.Spec.TLS.Key = encodeKey(keySha256)
	if err := kclient.Update(context.Background(), routeSha1); err != nil {
		t.Fatalf("failed to update route %s: %v", routeSha1Name, err)
	}
	if err := waitForAdminGate(t, routeupgradeable.RouteConfigAdminGate415Key, false, waitForAdminGateInterval, waitForAdminGateTimeout); err != nil {
		t.Fatalf("admin gate observation error: %v", err)
	}
}
