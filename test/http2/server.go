package http2

import (
	"fmt"
	"log"
	"net/http"
	"os"
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

func respondWithPodInfo(w http.ResponseWriter, response string) {
	w.Header().Set("x-pod-name", lookupEnv("POD_NAME", "unknown-pod"))
	w.Header().Set("x-pod-namespace", lookupEnv("POD_NAMESPACE", "unknown-namespace"))
	fmt.Fprint(w, response)
}

func Serve() {
	enableHTTP := lookupEnv("HTTP2_TEST_SERVER_ENABLE_HTTP_LISTENER", "true") != "false"
	enableHTTPS := lookupEnv("HTTP2_TEST_SERVER_ENABLE_HTTPS_LISTENER", "true") != "false"

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		respondWithPodInfo(w, req.Proto)
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		respondWithPodInfo(w, "ready")
	})

	var httpStarted, httpsStarted bool

	if enableHTTP {
		go func() {
			port := lookupEnv("HTTP_PORT", defaultHTTPPort)
			log.Printf("Listening on port %v\n", port)
			if err := http.ListenAndServe(":"+port, nil); err != nil {
				log.Fatal(err)
			}
		}()
		httpStarted = true
	}

	if enableHTTPS {
		go func() {
			crtFile := lookupEnv("TLS_CRT", defaultTLSCrt)
			keyFile := lookupEnv("TLS_KEY", defaultTLSKey)
			port := lookupEnv("HTTPS_PORT", defaultHTTPSPort)
			log.Printf("Listening securely on port %v\n", port)
			if err := http.ListenAndServeTLS(":"+port, crtFile, keyFile, nil); err != nil {
				log.Fatal(err)
			}
		}()
		httpsStarted = true
	}

	if !httpStarted && !httpsStarted {
		log.Fatal("No HTTP or HTTPS listeners were started - check environment variables HTTP2_TEST_SERVER_ENABLE_HTTP_LISTENER and HTTP2_TEST_SERVER_ENABLE_HTTPS_LISTENER")
	}

	select {}
}
