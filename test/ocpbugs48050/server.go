package ocpbugs48050

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
)

type duplicateTEResponseWriter struct {
	bufRW         *bufio.ReadWriter
	conn          net.Conn
	header        http.Header
	headerWritten bool
	status        int
}

var _ http.ResponseWriter = (*duplicateTEResponseWriter)(nil)

func (w *duplicateTEResponseWriter) Header() http.Header {
	return w.header
}

func (w *duplicateTEResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *duplicateTEResponseWriter) Write(body []byte) (int, error) {
	if !w.headerWritten {
		fmt.Fprintf(w.bufRW, "HTTP/1.1 %d %s\r\n", w.status, http.StatusText(w.status))

		for key, values := range w.header {
			for _, value := range values {
				fmt.Fprintf(w.bufRW, "%s: %s\r\n", key, value)
			}
		}

		fmt.Fprintf(w.bufRW, "\r\n")
		w.headerWritten = true
	}

	fmt.Fprintf(w.bufRW, "%x\r\n", len(body))
	n, err := w.bufRW.Write(body)
	if err != nil {
		return n, err
	}

	fmt.Fprintf(w.bufRW, "\r\n")

	return n, nil
}

// Close the response with the final 0-length chunk.
func (w *duplicateTEResponseWriter) Close() error {
	_, err := fmt.Fprintf(w.bufRW, "0\r\n\r\n")
	return err
}

// Hijack method to take control of the connection.
func (w *duplicateTEResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.bufRW, nil
}

func duplicateTEHandler(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	conn, bufRW, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	customWriter := &duplicateTEResponseWriter{
		conn:   conn,
		bufRW:  bufRW,
		header: make(http.Header),
		status: http.StatusOK,
	}

	customWriter.Header().Set("Transfer-Encoding", "chunked")
	customWriter.Header().Add("Transfer-Encoding", "chunked")

	customWriter.WriteHeader(http.StatusOK)
	customWriter.Write([]byte("This response contains multiple Transfer-Encoding headers.\n"))
	customWriter.Close()

	customWriter.bufRW.Flush()
}

func Serve() {
	httpPort := os.Getenv("HTTP_PORT")
	httpsPort := os.Getenv("HTTPS_PORT")

	if httpPort == "" || httpsPort == "" {
		log.Fatalf("Environment variables HTTP_PORT and HTTPS_PORT must be set")
	}

	http.HandleFunc("/single-te", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		fmt.Fprint(w, "/single-te")
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "/healthz")
	})

	http.HandleFunc("/duplicate-te", duplicateTEHandler)

	go func() {
		log.Fatal(http.ListenAndServe("[::]:"+httpPort, nil))
	}()

	go func() {
		log.Fatal(http.ListenAndServeTLS("[::]:"+httpsPort, "/etc/serving-cert/tls.crt", "/etc/serving-cert/tls.key", nil))
	}()

	select {}
}
