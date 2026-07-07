package operator

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"k8s.io/client-go/util/cert"
)

func TestValidateMetricsTLSFiles(t *testing.T) {
	testCases := []struct {
		name     string
		certFile string
		keyFile  string
		wantErr  bool
	}{
		{name: "both empty", certFile: "", keyFile: "", wantErr: false},
		{name: "both set", certFile: "/tmp/tls.crt", keyFile: "/tmp/tls.key", wantErr: false},
		{name: "cert only", certFile: "/tmp/tls.crt", keyFile: "", wantErr: true},
		{name: "key only", certFile: "", keyFile: "/tmp/tls.key", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMetricsTLSFiles(tc.certFile, tc.keyFile)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateMetricsTLSFiles() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestStartMetricsListener_TLS(t *testing.T) {
	certPEM, keyPEM, err := cert.GenerateSelfSignedCertKey("127.0.0.1", nil, nil)
	if err != nil {
		t.Fatalf("failed to generate test certificate: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("failed to write cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	spec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	tlsConfig, err := operatorcontroller.TLSConfigFromProfile(spec)
	if err != nil {
		t.Fatalf("failed to build TLS config: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve listener address: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("failed to close reserved listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		StartMetricsListener(addr, certFile, keyFile, tlsConfig, ctx)
		close(stopped)
	}()

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				MinVersion:         tlsConfig.MinVersion,
			},
		},
	}

	var lastErr error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		resp, err := client.Get("https://" + addr + "/metrics")
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatalf("failed to read /metrics response: %v", readErr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected status 200, got %d", resp.StatusCode)
			}
			if len(body) == 0 {
				t.Fatal("expected non-empty /metrics response body")
			}
			cancel()
			<-stopped
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-stopped
	t.Fatalf("failed to reach TLS metrics endpoint: %v", lastErr)
}
