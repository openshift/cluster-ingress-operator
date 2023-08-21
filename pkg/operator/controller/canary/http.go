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
func (r *reconciler) probeRouteEndpoint(route *routev1.Route) error {
	routeHost := getRouteHost(route)
	if len(routeHost) == 0 {
		return fmt.Errorf("route host is empty, cannot test route")
	}

	// Create HTTP request
	// Use https now that the canary route uses edge termination.
	// Some clusters that expose the default ingress controller
	// via an external load balancer drop all traffic on port 80,
	// in which case redirecting insecure traffic is not possible.
	// See https://bugzilla.redhat.com/show_bug.cgi?id=1934773.
	request, err := http.NewRequest("GET", "https://"+routeHost, nil)
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
	timeout := 10 * time.Second
	client := &http.Client{
		Timeout: timeout,
		// The canary route uses edge termination and the
		// default router certificate may be self signed, so
		// skip certificate verification here. See
		// https://bugzilla.redhat.com/show_bug.cgi?id=1932401.
		// TODO: Add the router's certificate to the HTTP client
		// so we can enable TLS verification.
		Transport: &http.Transport{
			// Use the cluster-wide proxy if it is available in the
			// pod's environment.
			Proxy:             http.ProxyFromEnvironment,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true, // BZ#2037447
		},
	}
	response, err := client.Do(request)

	if err != nil {
		// Check if err is a DNS error.
		dnsErr := &net.DNSError{}
		if errors.As(err, &dnsErr) {
			// Handle DNS error
			CanaryRouteDNSError.WithLabelValues(routeHost, dnsErr.Server).Inc()
			return fmt.Errorf("error sending canary HTTP request: DNS error: %v", err)
		}
		// Check if err is a timeout error.
		if os.IsTimeout(err) {
			// Handle timeout error.
			return fmt.Errorf("error sending canary HTTP Request: Timeout: %v", err)
		}
		// Check if err is a TLS alert.
		opErr := &net.OpError{}
		if errors.As(err, &opErr) {
			if opErr.Op == "remote error" {
				// If the operation is "remote error," it may be a TLS alert, as described in RFC 8446 section 6.
				// crypto/tls doesn't expose its TLS alert type, but we can check the error string. If the alert is
				// "certificate required", it means the canary lacks the client certificate necessary to complete its
				// check. In that case, verify that the router is configured to require mTLS, and if so, assume the
				// router is working as intended.
				if opErr.Err.Error() == "tls: certificate required" {
					if mtlsRequired, mtlsErr := r.isMTLSRequired(); mtlsErr != nil {
						// Log the error from isMTLSRequired(), but continue the function so the actual request error is
						// returned.
						log.Error(mtlsErr, "Failed to verify mTLS status of default ingress controller")
					} else if mtlsRequired {
						// Since mTLS is required in the ingress config and our probe was rejected due to a missing
						// certificate, consider this a successful probe.
						return nil
					}
				}
			}
		}
		return fmt.Errorf("error sending canary HTTP request to %q: %v", routeHost, err)
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

	if !strings.Contains(body, CanaryHealthcheckResponse) {
		return fmt.Errorf("expected canary request body to contain %q", CanaryHealthcheckResponse)
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
	case http.StatusOK:
		// Register total time in metrics (use milliseconds)
		CanaryRequestTime.WithLabelValues(routeHost).Observe(float64(totalTime.Milliseconds()))
	case http.StatusRequestTimeout:
		return fmt.Errorf("status code %d: request timed out", status)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("status code %d: Canary route not available via router", status)
	case http.StatusBadGateway:
		return fmt.Errorf("status code %d: bad gateway", status)
	case http.StatusInternalServerError:
		return fmt.Errorf("status code %d: server error", status)
	case http.StatusTooManyRequests:
		return fmt.Errorf("status code %d: too many requests", status)
	default:
		return fmt.Errorf("unexpected status code: %d", status)
	}

	return nil
}
