//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingress "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestRouterCompressionParsing(t *testing.T) {
	t.Parallel()
	// Test compression policies for the ingress config
	mimeTypesNormative := []operatorv1.CompressionMIMEType{"text/html", "application/json", "x-custom/allow-custom"}
	compressionPolicyNormative := operatorv1.HTTPCompressionPolicy{MimeTypes: mimeTypesNormative}

	mimeTypesEmpty := []operatorv1.CompressionMIMEType{}
	compressionPolicyEmpty := operatorv1.HTTPCompressionPolicy{MimeTypes: mimeTypesEmpty}

	mimeTypesNeedQuotes := []operatorv1.CompressionMIMEType{`text/html; v="keepquoted"`, `x-custom/allow-custom; specialChar='`}
	compressionPolicyNeedQuotes := operatorv1.HTTPCompressionPolicy{MimeTypes: mimeTypesNeedQuotes}

	mimeTypesErrors := []operatorv1.CompressionMIMEType{"text/", "x- /value", "//"}
	compressionPolicyErrors := operatorv1.HTTPCompressionPolicy{MimeTypes: mimeTypesErrors}

	testParsing(t, "http-compression-1", compressionPolicyNormative, "error testing normal compression policy: %v")
	testParsing(t, "http-compression-2", compressionPolicyEmpty, "error testing empty compression policy: %v")
	testParsing(t, "http-compression-3", compressionPolicyNeedQuotes, "error testing compression policy that needs quotes: %v")

	pc4, err := createPrivateController(t, "http-compression-4", dnsConfig.Spec.BaseDomain)
	if err != nil {
		t.Fatalf("error getting private controller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, pc4)

	if err := testCompressionPolicy(t, "http-compression-4", compressionPolicyErrors); err == nil {
		t.Errorf("compression policy with errors should have failed but didn't")
	}
}

func testParsing(t *testing.T, name string, policy operatorv1.HTTPCompressionPolicy, errorMsg string) {
	t.Helper()

	pc, err := createPrivateController(t, name, dnsConfig.Spec.BaseDomain)
	if err != nil {
		t.Fatal(err)
	}
	defer assertIngressControllerDeleted(t, kclient, pc)

	if err := testCompressionPolicy(t, name, policy); err != nil {
		t.Errorf(errorMsg, err)
	}
}

func createPrivateController(t *testing.T, privateName string, privateDomain string) (*operatorv1.IngressController, error) {
	t.Helper()

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: privateName}
	domain := icName.Name + "." + privateDomain

	ic := newPrivateController(icName, domain)
	// Create a new private Ingress Controller (deletion handled by caller)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		return ic, fmt.Errorf("error creating private ingresscontroller %s: %v", privateName, err)
	}
	return ic, nil
}

// testCompressionPolicy updates the given ingresscontroller's spec.httpCompressionPolicy with the given
// compressionPolicy, and reports an error if it the compression policy was not accepted or not added to the router
// deployment environment.
func testCompressionPolicy(t *testing.T, name string, compressionPolicy operatorv1.HTTPCompressionPolicy) error {
	t.Helper()
	namespacedName := types.NamespacedName{Namespace: operatorNamespace, Name: name}
	routerDeploymentNamespacedName := types.NamespacedName{Namespace: controller.DefaultOperandNamespace, Name: "router-" + name}

	if err := wait.PollImmediate(10*time.Second, 2*time.Minute, func() (bool, error) {
		ic, err := getIngressController(t, kclient, namespacedName, 5*time.Second)
		if err != nil {
			t.Logf("failed to get ingress controller: %v, retrying...", err)
			return false, nil
		}
		compressionPolicy.DeepCopyInto(&ic.Spec.HTTPCompression)

		if err := kclient.Update(context.TODO(), ic); err != nil {
			if errors.IsConflict(err) { // it has been modified, so it is ok to retry
				t.Logf("failed to update ingress controller, retrying...")
				return false, nil
			}
			// Return an error if validation failed
			return true, err
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to update ingress controller: %v", err)
	}

	// Now wait for it to become available
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, namespacedName, conditions...); err != nil {
		return fmt.Errorf("failed to observe expected conditions: %v", err)
	}

	// Get the router deployment
	deployment, err := getDeployment(t, kclient, routerDeploymentNamespacedName, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to get deployment: %v", err)
	}

	// Check if the MIME type environment variable has been updated
	mimeTypes := ingress.GetMIMETypes(compressionPolicy.MimeTypes)
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 2*time.Minute, "ROUTER_COMPRESSION_MIME", strings.Join(mimeTypes, " ")); err != nil {
		return fmt.Errorf("expected deployment to have mimeTypes %s: %v", mimeTypes, err)
	}

	return nil
}

func TestRouterCompressionOperation(t *testing.T) {
	// Get the default ingress controller
	ic, err := getIngressController(t, kclient, defaultName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	// Configure the default ingress controller to apply compression to the canary route's
	// MIME type, which is "text/plain; charset=utf-8"
	mimeType := []operatorv1.CompressionMIMEType{"text/plain; charset=utf-8"}
	if err := testCompressionPolicy(t, defaultName.Name, operatorv1.HTTPCompressionPolicy{MimeTypes: mimeType}); err != nil {
		t.Fatalf("failed to apply the required MIME type for test: %v", err)
	}

	// Cleanup the ic.Spec.CompressionPolicy
	defer func() {

		mimeType := []operatorv1.CompressionMIMEType{}
		compressionPolicy := operatorv1.HTTPCompressionPolicy{MimeTypes: mimeType}
		// Remove the mimeType that was added
		if err := wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
			ic, err := getIngressController(t, kclient, defaultName, 1*time.Minute)
			if err != nil {
				t.Logf("failed to get ingress controller: %v, retrying...", err)
				return false, nil
			}

			compressionPolicy.DeepCopyInto(&ic.Spec.HTTPCompression)

			if err := kclient.Update(context.TODO(), ic); err != nil {
				t.Logf("failed to cleanup ingress controller: %v, retrying...", err)
				return false, nil
			}
			return true, nil
		}); err != nil {
			t.Fatalf("failed to cleanup ingress controller: %v", err)
		}
	}()

	// Wait until the new compressionPolicy is active in the router deployment
	if err := waitForDeploymentCompleteWithOldPodTermination(t, kclient, controller.RouterDeploymentName(ic), 3*time.Minute); err != nil {
		t.Fatalf("failed to observe deployment completion: %v", err)
	}

	// Create the http client to check the Content-Encoding header
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// get the canary route
	r := &routev1.Route{}
	var routeName = types.NamespacedName{Namespace: "openshift-ingress-canary", Name: "canary"}
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), routeName, r); err != nil {
			t.Logf("couldn't get route %s: %v, retrying...", routeName, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("failed to get route: %v", err)
	}

	routeHost := getRouteHost(t, r, ic.Name)

	// curl to canary, without the Accept-Encoding header set to gzip
	if err := testContentEncoding(t, client, routeHost, false, ""); err != nil {
		t.Error(err)
	}

	// curl to canary, WITH the Accept-Encoding header set to gzip
	if err := testContentEncoding(t, client, routeHost, true, "gzip"); err != nil {
		t.Error(err)
	}
}

// testContentEncoding makes a call to the provided route, adds a gzip content header if addHeader is true, and
// compares the returned Content-Encoding header to the given expectedContentEncoding.  If expectedContentEncoding
// is the same as the returned Content-Encoding header, then the test succeeds.  Otherwise it fails.
func testContentEncoding(t *testing.T, client *http.Client, routeHost string, addHeader bool, expectedContentEncoding string) error {
	t.Helper()

	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		header, code, err := getHttpHeaders(client, routeHost, addHeader)

		if err != nil {
			t.Logf("GET %s failed: %v, retrying...", routeHost, err)
			return false, nil
		}
		if code != http.StatusOK {
			t.Logf("GET %s failed: status %v, expected %v, retrying...", routeHost, code, http.StatusOK)
			return false, nil // retry on 503 as pod/service may not be ready
		}

		contentEncoding := header.Get("Content-Encoding")
		if contentEncoding != expectedContentEncoding {
			return false, fmt.Errorf("compression error: expected %q, got %q for %s route", expectedContentEncoding, contentEncoding, routeHost)
		}
		return true, nil
	}); err != nil {
		return err
	}
	return nil
}

// getHttpHeaders returns the HTTP Headers, and first adds the request header "Accept-Encoding: gzip" if requested.
func getHttpHeaders(client *http.Client, routeHost string, addHeader bool) (http.Header, int, error) {
	// Create the HTTPS request
	request, err := http.NewRequest("GET", "https://"+routeHost, nil)
	if err != nil {
		return nil, -1, fmt.Errorf("New request failed: %v", err)
	}

	// Give the instruction to compress
	if addHeader {
		request.Header.Add("Accept-Encoding", "gzip")
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, -1, fmt.Errorf("GET %s failed: %v", routeHost, err)
	}
	// Close response body
	defer response.Body.Close()

	return response.Header, response.StatusCode, nil
}
