package http

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	canarycontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/canary"
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

func listenAndServeTLS(port, certFile, keyFile string) {
	fmt.Printf("serving TLS on %s\n", port)
	err := http.ListenAndServeTLS(":"+port, certFile, keyFile, nil)
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

	port := os.Getenv("PORT")
	if len(port) == 0 {
		port = "8443"
	}
	go listenAndServeTLS(port, tlsCertFile, tlsKeyFile)

	port = os.Getenv("SECOND_PORT")
	if len(port) == 0 {
		port = "8888"
	}
	go listenAndServeTLS(port, tlsCertFile, tlsKeyFile)

	select {}
}
