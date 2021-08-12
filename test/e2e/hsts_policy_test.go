// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/utils/pointer"
)

func TestHstsPolicyWorks(t *testing.T) {
	// Setup a Required HSTS Policy for the ingress config
	maxAgePolicy := configv1.MaxAgePolicy{LargestMaxAge: pointer.Int32Ptr(99999), SmallestMaxAge: pointer.Int32Ptr(0)}
	domainPatterns := []string{}
	hstsPolicy := configv1.RequiredHSTSPolicy{
		DomainPatterns:          domainPatterns, // this policy will only validate routes with hosts in the DomainPatterns
		PreloadPolicy:           configv1.RequirePreloadPolicy,
		IncludeSubDomainsPolicy: configv1.RequireIncludeSubDomains,
		MaxAge:                  maxAgePolicy,
	}

	ing := &configv1.Ingress{}
	appsDomain := ""

	// Update the ingress config with the new HSTS policy, and include its apps domain
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		// Get the ingress config
		if err := kclient.Get(context.TODO(), clusterConfigName, ing); err != nil {
			t.Logf("Get ingress config failed: %v, retrying...", err)
			return false, nil
		}
		// Update the hsts.DomainPatterns with the ingress spec domain
		if !domainExists(ing.Spec.Domain, hstsPolicy.DomainPatterns) {
			hstsPolicy.DomainPatterns = append(hstsPolicy.DomainPatterns, "*."+ing.Spec.Domain)
			appsDomain = ing.Spec.Domain
		}
		// Update the ingress config with the new policy
		ing.Spec.RequiredHSTSPolicies = append(ing.Spec.RequiredHSTSPolicies, hstsPolicy)
		// Update the ingress config
		if err := kclient.Update(context.TODO(), ing); err != nil {
			t.Logf("failed to update ingress config: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}
	defer func() {
		// Remove the HSTS policies from the ingress config
		ing.Spec.RequiredHSTSPolicies = nil
		if err := kclient.Update(context.TODO(), ing); err != nil {
			t.Fatalf("failed to restore ingress config: %v", err)
		}
	}()

	p := ing.Spec.RequiredHSTSPolicies[0]
	t.Logf("created a RequiredHSTSPolicy with DomainPatterns: %v,\n preload policy: %s,\n includeSubDomains policy: %s,\n largest age: %d,\n smallest age: %d\n", p.DomainPatterns, p.PreloadPolicy, p.IncludeSubDomainsPolicy, *p.MaxAge.LargestMaxAge, *p.MaxAge.SmallestMaxAge)

	// Use the same namespace for route, service, and pod
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hsts-policy-namespace",
		},
	}
	if err := kclient.Create(context.TODO(), ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	defer func() {
		// this will cleanup all components in this namespace
		if err := kclient.Delete(context.TODO(), ns); err != nil {
			t.Fatalf("failed to delete test namespace %s: %v", ns.Name, err)
		}
	}()

	// Create pod
	echoPod := buildEchoPod("hsts-policy-echo", ns.Name)
	if err := kclient.Create(context.TODO(), echoPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", ns, echoPod.Name, err)
	}

	// Create service
	echoService := buildEchoService(echoPod.Name, ns.Name, echoPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), echoService); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
	}

	// Create a route that should fail the HSTS policy validation
	t.Logf("creating an invalid route at %s", time.Now().Format(time.StampMilli))
	invalidRoute := buildRouteWithHSTS("invalid-route", echoPod.Namespace, echoService.Name, "invalid-route."+appsDomain, "max-age=99999999")
	if err := kclient.Create(context.TODO(), invalidRoute); err == nil {
		t.Fatalf("failed to reject an invalid route %s/%s, max-age 99999999", invalidRoute.Namespace, invalidRoute.Name)
	} else {
		t.Logf("rejected an invalid route at %s: %s/%s with annotation %s: %v", time.Now().Format(time.StampMilli), invalidRoute.Namespace, invalidRoute.Name, invalidRoute.Annotations, err)
	}

	// Create a route that should pass the HSTS policy validation
	exampleHeader := "max-age=0;preload;includesubdomains"
	t.Logf("creating a valid route at %s", time.Now().Format(time.StampMilli))
	validRoute := buildRouteWithHSTS("valid-route", echoPod.Namespace, echoService.Name, "valid-route."+appsDomain, exampleHeader)
	if err := kclient.Create(context.TODO(), validRoute); err != nil {
		t.Fatalf("failed to create a valid route %s/%s: %v", validRoute.Namespace, validRoute.Name, err)
	} else {
		t.Logf("created a valid route at %s: %s/%s with annotation %s", time.Now().Format(time.StampMilli), validRoute.Namespace, validRoute.Name, validRoute.Annotations)
	}

	// Create the http client to check the header
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// Wait for route service to respond, and when it does, check for the header that should be there
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		resultHeader, statusCode, err := getHttpHeader(client, validRoute)
		if err != nil {
			t.Logf("GET %s failed: %v, retrying...", validRoute.Spec.Host, err)
			return false, nil
		}
		if statusCode != http.StatusOK {
			t.Logf("GET %s failed: status %v, expected %v, retrying...", validRoute.Spec.Host, statusCode, http.StatusOK)
			return false, nil // retry on 503 as pod/service may not be ready
		}
		header := resultHeader.Get("strict-transport-security")
		if header != exampleHeader {
			return false, fmt.Errorf("expected [%s], got [%s]", exampleHeader, header)
		}
		t.Logf("request to %s got correct HSTS header: [%s]", validRoute.Spec.Host, header)
		return true, nil
	}); err != nil {
		t.Fatalf("failed to find header [%s]: %v", exampleHeader, err)
	}
}

func domainExists(appsDomain string, patterns []string) bool {
	for _, domain := range patterns {
		if domain == appsDomain {
			return true
		}
	}
	return false
}

// Send HTTPS Request to a route and return the header
func getHttpHeader(client *http.Client, route *routev1.Route) (http.Header, int, error) {
	// Send the HTTPS request
	response, err := client.Get("https://" + route.Spec.Host)
	if err != nil {
		return nil, 0, fmt.Errorf("GET %s failed: %v", route.Spec.Host, err)
	}

	// Close response body
	defer response.Body.Close()

	return response.Header, response.StatusCode, nil
}
