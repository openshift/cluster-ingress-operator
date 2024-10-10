package ocpbugs48050

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Endpoint represents an API endpoint with its expected response.
type Endpoint struct {
	Path                   string
	ExpectedBody           string
	ExpectedStatus         int
	ExpectedErrorSubstring string // Set for endpoints expected to fail
}

// TestServer performs tests on various endpoints with different HTTP
// client configurations.
func TestServer(t *testing.T) {
	serverAddr := startTestServer(t)

	endpoints := []Endpoint{
		{
			Path:           "/single-te",
			ExpectedBody:   "/single-te",
			ExpectedStatus: http.StatusOK,
		},
		{
			Path:           "/healthz",
			ExpectedBody:   "/healthz",
			ExpectedStatus: http.StatusOK,
		},
		{
			Path:           "/not-found",
			ExpectedBody:   "404 page not found\n",
			ExpectedStatus: http.StatusNotFound,
		},
		{
			Path:                   "/duplicate-te",
			ExpectedErrorSubstring: `net/http: HTTP/1.x transport connection broken: too many transfer encodings: ["chunked" "chunked"]`,
			ExpectedStatus:         http.StatusInternalServerError,
		},
	}

	numConnections := 1000

	clientConfigs := []struct {
		name          string
		transportOpts func(*http.Transport)
	}{
		{
			name: "KeepAlivesDisabled",
			transportOpts: func(tr *http.Transport) {
				tr.DisableKeepAlives = true
			},
		},
		{
			name: "KeepAlivesEnabled",
			transportOpts: func(tr *http.Transport) {
				tr.DisableKeepAlives = false
				tr.MaxIdleConns = 1000
				tr.MaxIdleConnsPerHost = 1000
				tr.IdleConnTimeout = 90 * time.Second
			},
		},
	}

	for _, config := range clientConfigs {
		config := config // Capture range variable.
		t.Run(config.name, func(t *testing.T) {
			transport := &http.Transport{}
			config.transportOpts(transport)
			client := &http.Client{
				Transport: transport,
				Timeout:   10 * time.Second,
			}

			var wg sync.WaitGroup

			for _, endpoint := range endpoints {
				endpoint := endpoint // Capture range variable.
				wg.Add(1)
				go func(ep Endpoint) {
					defer wg.Done()

					var validateFunc func(t *testing.T, ep Endpoint, resp *http.Response, err error, body string) bool
					if ep.ExpectedErrorSubstring != "" {
						validateFunc = validateErrorResponse
					} else {
						validateFunc = validateSuccessResponse
					}

					var requestMatchCount int64

					makeRequests(t, client, serverAddr, ep, numConnections, &requestMatchCount, validateFunc)

					if atomic.LoadInt64(&requestMatchCount) != int64(numConnections) {
						if ep.ExpectedErrorSubstring != "" {
							t.Errorf("Endpoint %s: expected %d failed responses containing %q, got %d matching failures",
								ep.Path, numConnections, ep.ExpectedErrorSubstring, requestMatchCount)
						} else {
							t.Errorf("Endpoint %s: expected %d successful responses, got %d matching successes",
								ep.Path, numConnections, requestMatchCount)
						}
					}
				}(endpoint)
			}

			wg.Wait()
		})
	}
}

// makeRequests uses the provided HTTP client to make multiple
// requests concurrently and counts the number of responses that match
// expectations.
func makeRequests(t *testing.T, client *http.Client, serverAddr string, endpoint Endpoint, numRequests int, matchCount *int64, validateFunc func(t *testing.T, ep Endpoint, resp *http.Response, err error, body string) bool) {
	var wg sync.WaitGroup

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			url := fmt.Sprintf("http://%s%s", serverAddr, endpoint.Path)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				t.Errorf("Request creation failed for %s: %v", endpoint.Path, err)
				return
			}

			resp, err := client.Do(req)
			var body string
			if resp != nil {
				bodyBytes, readErr := io.ReadAll(resp.Body)
				if readErr != nil {
					t.Errorf("Reading response body failed for %s: %v", endpoint.Path, readErr)
					body = ""
				} else {
					body = string(bodyBytes)
				}
			} else {
				body = ""
			}

			if validateFunc(t, endpoint, resp, err, body) {
				atomic.AddInt64(matchCount, 1)
			}
		}(i)
	}

	wg.Wait()
}

func validateSuccessResponse(t *testing.T, ep Endpoint, resp *http.Response, err error, body string) bool {
	if err != nil {
		t.Errorf("[%s] Expected no error, got error: %v", ep.Path, err)
		return false
	}

	valid := true

	if resp.StatusCode != ep.ExpectedStatus {
		t.Errorf("[%s] Expected status code %d, got %d", ep.Path, ep.ExpectedStatus, resp.StatusCode)
		valid = false
	}

	if body != ep.ExpectedBody {
		t.Errorf("[%s] Expected body %q, got %q", ep.Path, ep.ExpectedBody, body)
		valid = false
	}

	return valid
}

func validateErrorResponse(t *testing.T, ep Endpoint, resp *http.Response, err error, _ string) bool {
	valid := true

	if resp != nil {
		t.Errorf("[%s] Unexpected response", ep.Path)
		return false
	}

	if !strings.Contains(err.Error(), ep.ExpectedErrorSubstring) {
		t.Errorf("[%s] Expected error to contain %q, got %v", ep.Path, ep.ExpectedErrorSubstring, err)
		valid = false
	}

	return valid
}

// startTestServer starts the HTTP server and returns its address.
func startTestServer(t *testing.T) string {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}

	serverAddr := listener.Addr().String()

	httpServer := newHTTPServer()
	httpServer.Handle("/healthz", http.HandlerFunc(healthzHandler))
	httpServer.Handle("/single-te", http.HandlerFunc(singleTransferEncodingHandler))
	httpServer.Handle("/duplicate-te", http.HandlerFunc(duplicateTransferEncodingHandler))
	httpServer.Handle("/not-found", http.HandlerFunc(http.NotFound))

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				// Listener closed or other error.
				break
			}
			handler := newConnectionHandler(httpServer, &devNullLogger{})
			go handler.processConnection(newNetConnWrapper(conn))
		}
	}()

	t.Cleanup(func() {
		listener.Close()
	})

	time.Sleep(500 * time.Millisecond)

	return serverAddr
}
