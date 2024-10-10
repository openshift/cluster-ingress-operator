package ocpbugs48050

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ConnectionID uint64

type connection interface {
	io.ReadWriteCloser

	ID() ConnectionID
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

type ConnectionLog struct {
	ConnID      ConnectionID `json:"conn_id"`
	Response    ResponseLog  `json:"response,omitempty"`
	IOError     string       `json:"io_error,omitempty"`
	LocalAddr   string       `json:"local_addr"`
	PeerAddr    string       `json:"peer_addr"`
	RequestLine string       `json:"request_line"`
	Timestamp   time.Time    `json:"timestamp"`
	Complete    bool         `json:"finished"`
}

type ResponseLog struct {
	StatusLine string     `json:"status_line"`
	Headers    []string   `json:"headers,omitempty"`
	Body       string     `json:"body,omitempty"`
	Chunks     []ChunkLog `json:"chunks,omitempty"`
}

type ChunkLog struct {
	Size string `json:"size"`
	Data string `json:"data"`
}

var responseLogs sync.Map

type connectionHandler struct {
	server       *httpServer
	logger       logger
	responseLogs *sync.Map
}

type netConnWrapper struct {
	conn net.Conn
	id   ConnectionID
}

func newNetConnWrapper(conn net.Conn) *netConnWrapper {
	return &netConnWrapper{
		conn: conn,
		id:   nextConnID(),
	}
}

func (w *netConnWrapper) ID() ConnectionID {
	return w.id
}

func (w *netConnWrapper) Read(p []byte) (n int, err error) {
	return w.conn.Read(p)
}

func (w *netConnWrapper) Write(p []byte) (n int, err error) {
	return w.conn.Write(p)
}

func (w *netConnWrapper) Close() error {
	return w.conn.Close()
}

func (w *netConnWrapper) RemoteAddr() net.Addr {
	return w.conn.RemoteAddr()
}

func (w *netConnWrapper) LocalAddr() net.Addr {
	return w.conn.LocalAddr()
}

func newConnectionHandler(server *httpServer, logger logger) *connectionHandler {
	return &connectionHandler{
		server:       server,
		logger:       logger,
		responseLogs: &responseLogs,
	}
}

func (h *connectionHandler) processConnection(conn connection) error {
	defer conn.Close()

	isKeepAliveDisabled := func(req *http.Request, respHeaders http.Header) bool {
		// Check the client request for "Connection: close".
		if strings.EqualFold(req.Header.Get("Connection"), "close") {
			return true
		}

		// Check the server response for "Connection: close".
		if strings.EqualFold(respHeaders.Get("Connection"), "close") {
			return true
		}

		// Handle HTTP/1.0 (which doesn't support keep-alive
		// by default).
		if req.ProtoMajor == 1 && req.ProtoMinor == 0 {
			return !strings.EqualFold(req.Header.Get("Connection"), "keep-alive")
		}

		return false
	}

	h.logRequestResponse(conn)

	for {
		req, err := customRequestReader(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("error reading request: %w", err)
		}

		h.updateRequestInfo(conn, req)
		crw := newCustomResponseWriter(conn, req, h.logger, h)
		h.server.processRequest(crw, req)

		if _, err := crw.finaliseResponse(); err != nil {
			return fmt.Errorf("error writing response: %w", err)
		}

		crw.recordResponse()
		discardRequestBody(req)

		if isKeepAliveDisabled(req, crw.Header()) {
			break
		}
	}

	return nil
}

func (h *connectionHandler) logRequestResponse(conn connection) {
	if _, ok := h.responseLogs.Load(conn.ID()); ok {
		log.Fatalf("Connection log entry already exists for connection ID: %v", conn.ID())
	}

	h.responseLogs.Store(conn.ID(), &ConnectionLog{
		ConnID:    conn.ID(),
		LocalAddr: conn.LocalAddr().String(),
		PeerAddr:  conn.RemoteAddr().String(),
		Timestamp: time.Now(),
	})
}

func (h *connectionHandler) updateRequestInfo(conn connection, req *http.Request) {
	value, ok := h.responseLogs.Load(conn.ID())
	if !ok {
		log.Fatalf("No log entry found for connection ID: %v", conn.ID())
	}

	logEntry := value.(*ConnectionLog)
	logEntry.RequestLine = fmt.Sprintf("%s %s %s", req.Method, req.URL.Path, req.Proto)
	h.responseLogs.Store(conn.ID(), logEntry)
}

func discardRequestBody(req *http.Request) {
	if req.Body != nil {
		io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
}

func newConnIDGenerator() func() ConnectionID {
	var connCounter uint64
	return func() ConnectionID {
		return ConnectionID(atomic.AddUint64(&connCounter, 1))
	}
}

var nextConnID = newConnIDGenerator()
