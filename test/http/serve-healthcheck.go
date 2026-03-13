package http

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	canarycontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/canary"
	"github.com/openshift/library-go/pkg/crypto"
)

func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	response := os.Getenv("RESPONSE")
	if len(response) == 0 {
		response = canarycontroller.CanaryHealthcheckResponse
	}

	// Echo back the port the request was received on
	// via a "request-port" header.
	addr := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		w.Header().Set("x-request-port", strconv.Itoa(tcpAddr.Port))
	}

	_, err := fmt.Fprintln(w, response)
	if err == nil {
		fmt.Println("Serving canary healthcheck request")
	} else {
		fmt.Printf("Could not serve canary healthcheck: %v\n", err)
	}
}

// tlsConfigFromEnv builds a tls.Config from the TLS_CIPHERS and
// TLS_MIN_VERSION environment variables.  When both are unset the returned
// config uses the library-go secure defaults.
// If env vars are set but invalid, logs warnings and falls back to defaults.
func tlsConfigFromEnv() *tls.Config {
	cfg := &tls.Config{}

	if cipherEnv := os.Getenv("TLS_CIPHERS"); len(cipherEnv) > 0 {
		raw := strings.Split(cipherEnv, ",")
		openSSLNames := make([]string, 0, len(raw))
		for _, name := range raw {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				openSSLNames = append(openSSLNames, trimmed)
			}
		}
		ianaNames := crypto.OpenSSLToIANACipherSuites(openSSLNames)
		if len(ianaNames) > 0 {
			var cipherSuites []uint16
			for _, name := range ianaNames {
				id, err := crypto.CipherSuite(name)
				if err != nil {
					fmt.Printf("Warning: skipping unknown cipher suite %q: %v\n", name, err)
					continue
				}
				cipherSuites = append(cipherSuites, id)
			}
			if len(cipherSuites) > 0 {
				cfg.CipherSuites = cipherSuites
			} else {
				fmt.Printf("Warning: TLS_CIPHERS env var is set (%s) but no cipher suites could be resolved; using defaults\n", cipherEnv)
			}
		} else {
			fmt.Printf("Warning: TLS_CIPHERS env var is set (%s) but contains no valid cipher suites; using defaults\n", cipherEnv)
		}
	}

	if minVersion := os.Getenv("TLS_MIN_VERSION"); len(minVersion) > 0 {
		v, err := crypto.TLSVersion(minVersion)
		if err != nil {
			fmt.Printf("Warning: TLS_MIN_VERSION env var is set (%s) but invalid (%v); using defaults\n", minVersion, err)
		} else {
			cfg.MinVersion = v
		}
	}

	// Apply secure defaults for any fields not set by the env vars.
	return crypto.SecureTLSConfig(cfg)
}

func listenAndServeTLS(port, certFile, keyFile string, tlsCfg *tls.Config) {
	fmt.Printf("serving TLS on %s\n", port)
	server := &http.Server{
		Addr:      ":" + port,
		TLSConfig: tlsCfg,
	}
	err := server.ListenAndServeTLS(certFile, keyFile)
	if err != nil {
		panic("ListenAndServeTLS: " + err.Error())
	}
}

func NewServeHealthCheckCommand() *cobra.Command {
	var command = &cobra.Command{
		Use:   canarycontroller.CanaryHealthcheckCommand,
		Short: "Certify canary server health by echoing a response",
		Long:  canarycontroller.CanaryHealthcheckCommand + ` echoes a response when queried, thus certifying health of the canary service.`,
		Run: func(cmd *cobra.Command, args []string) {
			serveHealthCheck()
		},
	}

	return command
}

func serveHealthCheck() {
	http.HandleFunc("/", healthCheckHandler)

	tlsCertFile := os.Getenv("TLS_CERT")
	tlsKeyFile := os.Getenv("TLS_KEY")
	tlsCfg := tlsConfigFromEnv()

	port := os.Getenv("PORT")
	if len(port) == 0 {
		port = "8443"
	}
	go listenAndServeTLS(port, tlsCertFile, tlsKeyFile, tlsCfg.Clone())

	port = os.Getenv("SECOND_PORT")
	if len(port) == 0 {
		port = "8888"
	}
	go listenAndServeTLS(port, tlsCertFile, tlsKeyFile, tlsCfg.Clone())

	select {}
}
