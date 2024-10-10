package ocpbugs48050

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"os"
)

type listenerConfig struct {
	Address  string
	UseTLS   bool
	CertFile string
	KeyFile  string
}

func createListener(config listenerConfig) (net.Listener, error) {
	if config.UseTLS {
		cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
		return tls.Listen("tcp", config.Address, tlsConfig)
	}
	return net.Listen("tcp", config.Address)
}

func handleConnection(conn net.Conn, server *httpServer) {
	wrappedConn := newNetConnWrapper(conn)
	handler := newConnectionHandler(server, &stdoutLogger{})
	if err := handler.processConnection(wrappedConn); err != nil {
		log.Printf("Connection error: %v", err)
	} else {
		log.Printf("Connection closed normally: %v", conn.RemoteAddr())
	}
}

func startListener(config listenerConfig, server *httpServer) {
	listener, err := createListener(config)
	if err != nil {
		log.Fatalf("Failed to create listener on %s: %v", config.Address, err)
	}
	defer listener.Close()

	log.Printf("Accepting connections on %s (TLS: %v)", config.Address, config.UseTLS)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go handleConnection(conn, server)
	}

}

func Serve() {
	httpPort := os.Getenv("HTTP_PORT")
	httpsPort := os.Getenv("HTTPS_PORT")

	if httpPort == "" || httpsPort == "" {
		log.Fatalf("Environment variables HTTP_PORT and HTTPS_PORT must be set")
	}

	httpServer := newHTTPServer()

	httpServer.Handle("/healthz", http.HandlerFunc(healthzHandler))
	httpServer.Handle("/access-logs", http.HandlerFunc(accessLogsHandler))
	httpServer.Handle("/single-te", http.HandlerFunc(singleTransferEncodingHandler))
	httpServer.Handle("/duplicate-te", http.HandlerFunc(duplicateTransferEncodingHandler))
	httpServer.Handle("/discovery", http.HandlerFunc(discoveryHandler))

	for _, config := range []listenerConfig{{
		Address: "[::]:" + httpPort,
		UseTLS:  false,
	}, {
		Address:  "[::]:" + httpsPort,
		UseTLS:   true,
		CertFile: "/etc/serving-cert/tls.crt",
		KeyFile:  "/etc/serving-cert/tls.key",
	}} {
		go startListener(config, httpServer)
	}

	select {}
}
