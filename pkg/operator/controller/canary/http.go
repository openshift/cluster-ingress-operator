package canary

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	routev1 "github.com/openshift/api/route/v1"

	"github.com/tcnksm/go-httpstat"
)

const (
	echoServerPortAckHeader = "x-request-port"
)

// probeRouteEndpoint probes the given route's host
// and returns an error when applicable.
func probeRouteEndpoint(route *routev1.Route) error {
	if len(route.Spec.Host) == 0 {
		return fmt.Errorf("route.Spec.Host is empty, cannot test route")
	}

	// Create HTTP request
	request, err := http.NewRequest("GET", "http://"+route.Spec.Host, nil)
	if err != nil {
		return fmt.Errorf("error creating canary HTTP request %v: %v", request, err)
	}

	// Create HTTP result
	// for request stats tracking.
	result := &httpstat.Result{}

	// Get request context
	ctx := httpstat.WithHTTPStat(request.Context(), result)
	request = request.WithContext(ctx)

	// Send the HTTP request
	timeout, _ := time.ParseDuration("10s")
	client := &http.Client{
		Timeout: timeout,
		// The canary route uses edge termination and the
		// default router certificate may be self signed, so
		// skip certificate verification here. See
		// https://bugzilla.redhat.com/show_bug.cgi?id=1932401.
		// TODO: Add the router's certificate to the HTTP client
		// so we can enable TLS verification.
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	response, err := client.Do(request)

	if err != nil {
		// Check if err is a DNS error
		dnsErr := &net.DNSError{}
		if errors.As(err, &dnsErr) {
			// Handle DNS error
			CanaryRouteDNSError.WithLabelValues(route.Spec.Host, dnsErr.Server).Inc()
			return fmt.Errorf("error sending canary HTTP request: DNS error: %v", err)
		}
		// Check if err is a timeout error
		if os.IsTimeout(err) {
			// Handle timeout error
			return fmt.Errorf("error sending canary HTTP Request: Timeout: %v", err)
		}
		return fmt.Errorf("error sending canary HTTP request to %q: %v", route.Spec.Host, err)
	}

	// Close response body even if read fails
	defer response.Body.Close()

	// Read response body
	bodyBytes, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("error reading canary response body: %v", err)
	}
	body := string(bodyBytes)
	t := time.Now()
	// Mark request as finished
	result.End(t)
	totalTime := result.Total(t)

	// Verify body contents
	if len(body) == 0 {
		return fmt.Errorf("expected canary response body to not be empty")
	}

	expectedBodyContents := "Hello OpenShift!"
	if !strings.Contains(body, expectedBodyContents) {
		return fmt.Errorf("expected canary request body to contain %q", expectedBodyContents)
	}

	// Verify that the request was received on the correct port
	recPort := response.Header.Get(echoServerPortAckHeader)
	if len(recPort) == 0 {
		return fmt.Errorf("expected %q header in canary response to have a nonempty value", echoServerPortAckHeader)
	}
	routePortStr := route.Spec.Port.TargetPort.String()
	if routePortStr != recPort {
		// router wedged, register in metrics counter
		CanaryEndpointWrongPortEcho.Inc()
		return fmt.Errorf("canary request received on port %s, but route specifies %v", recPort, routePortStr)
	}

	// Check status code
	switch status := response.StatusCode; status {
	case 200:
		// Register total time in metrics (use milliseconds)
		CanaryRequestTime.WithLabelValues(route.Spec.Host).Observe(float64(totalTime.Milliseconds()))
	case 408:
		return fmt.Errorf("status code %d: request timed out", status)
	case 503:
		return fmt.Errorf("status code %d: Canary route not available via router", status)
	// TODO (sgreene):
	// Add more specific status code checks, if any are missing.
	// Also, use HTTP status code constants, if available.
	default:
		return fmt.Errorf("unexpected status code: %d", status)
	}

	return nil
}
