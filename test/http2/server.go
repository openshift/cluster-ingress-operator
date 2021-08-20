package http2

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const (
	defaultHTTPPort  = "8080"
	defaultHTTPSPort = "8443"
	defaultTLSCrt    = "/etc/serving-cert/tls.crt"
	defaultTLSKey    = "/etc/serving-cert/tls.key"
)

func lookupEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

func Serve() {
	crtFile := lookupEnv("TLS_CRT", defaultTLSCrt)
	keyFile := lookupEnv("TLS_KEY", defaultTLSKey)

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, req.Proto)
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "ready")
	})

	go func() {
		port := lookupEnv("HTTP_PORT", defaultHTTPPort)
		log.Printf("Listening on port %v\n", port)

		// h2s is a server that handles cleartext HTTP/2.
		h2s := &http2.Server{}
		// h1s is a server that handles cleartext HTTP/1 but with a
		// handler that recognizes cleartext HTTP/2 connections and
		// redirects them to h2s.
		h1s := &http.Server{
			Addr:    ":" + port,
			Handler: h2c.NewHandler(http.DefaultServeMux, h2s),
		}

		if err := h1s.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	go func() {
		port := lookupEnv("HTTPS_PORT", defaultHTTPSPort)
		log.Printf("Listening securely on port %v\n", port)

		if err := http.ListenAndServeTLS(":"+port, crtFile, keyFile, nil); err != nil {
			log.Fatal(err)
		}
	}()

	select {}
}
