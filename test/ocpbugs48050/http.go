package ocpbugs48050

import (
	"net/http"
)

type httpServer struct {
	mux             *http.ServeMux
	notFoundHandler http.Handler
}

func newHTTPServer() *httpServer {
	return &httpServer{
		mux:             http.NewServeMux(),
		notFoundHandler: http.NotFoundHandler(),
	}
}

func (s *httpServer) Handle(path string, handler http.Handler) {
	s.mux.Handle(path, handler)
}

// processRequest processes an HTTP request and writes the response
// using the provided ResponseWriter.
func (s *httpServer) processRequest(w http.ResponseWriter, r *http.Request) {
	handler, pattern := s.mux.Handler(r)
	if pattern == "" {
		s.notFoundHandler.ServeHTTP(w, r)
	} else {
		handler.ServeHTTP(w, r)
	}
}
