package ocpbugs48050

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type customResponseWriter struct {
	conn              connection
	connectionHandler *connectionHandler
	headers           http.Header
	logger            logger
	request           *http.Request
	responseLog       ResponseLog
	statusCode        int
	writingBody       bool
	wroteHeader       bool
}

// Type assertion to ensure CustomResponseWriter implements
// http.ResponseWriter.
var _ http.ResponseWriter = (*customResponseWriter)(nil)

// newCustomResponseWriter creates a new instance of CustomResponseWriter.
func newCustomResponseWriter(conn connection, req *http.Request, logger logger, handler *connectionHandler) *customResponseWriter {
	return &customResponseWriter{
		conn:              conn,
		connectionHandler: handler,
		headers:           make(http.Header),
		logger:            logger,
		request:           req,
		responseLog: ResponseLog{
			Headers: []string{},
			Chunks:  []ChunkLog{},
		},
	}
}

// Header returns the response headers.
func (crw *customResponseWriter) Header() http.Header {
	if crw.headers == nil {
		crw.headers = make(http.Header)
	}
	return crw.headers
}

func (crw *customResponseWriter) WriteHeader(statusCode int) {
	if crw.wroteHeader {
		return
	}

	crw.wroteHeader = true
	crw.statusCode = statusCode

	statusLine := fmt.Sprintf("HTTP/1.1 %d %s\r\n", crw.statusCode, http.StatusText(crw.statusCode))
	if _, err := crw.writeAndLog(statusLine); err != nil {
		return
	}

	crw.logger.Logf(strings.TrimSpace(statusLine))
	crw.responseLog.StatusLine = strings.TrimSpace(statusLine)

	if !crw.haveContentLengthHeader() && !crw.haveTransferEncodingHeader() {
		crw.Header().Set("Transfer-Encoding", "chunked")
	}

	keys := make([]string, 0, len(crw.headers))
	for k := range crw.headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, val := range crw.headers[k] {
			headerLine := fmt.Sprintf("%s: %s\r\n", k, val)
			if _, err := crw.writeAndLog(headerLine); err != nil {
				return
			}

			crw.logger.Logf(strings.TrimSpace(headerLine))
			crw.responseLog.Headers = append(crw.responseLog.Headers, strings.TrimSpace(headerLine))
		}
	}

	// Write the blank line to indicate the end of headers.
	if _, err := crw.writeAndLog("\r\n"); err != nil {
		return
	}

	crw.logger.Logf("")
}

func (crw *customResponseWriter) Write(data []byte) (int, error) {
	if !crw.wroteHeader {
		crw.WriteHeader(http.StatusOK)
	}

	if crw.request.Method == http.MethodHead {
		return 0, nil
	}

	if crw.haveTransferEncodingHeader() {
		chunkSize := fmt.Sprintf("%x", len(data))
		chunk := chunkSize + "\r\n" + string(data) + "\r\n"

		if n, err := crw.writeAndLog(chunk); err != nil {
			return n, err
		}

		crw.logger.Logf("Chunk-Size: %s", chunkSize)
		crw.logger.Logf("Chunk-Data: %s", strings.TrimSpace(string(data)))

		crw.responseLog.Chunks = append(crw.responseLog.Chunks, ChunkLog{
			Size: chunkSize,
			Data: string(data),
		})

		return len(data), nil
	}

	n, err := crw.writeAndLog(string(data))
	if err != nil {
		return n, err
	}

	crw.logger.Logf("Body: %s", strings.TrimSpace(string(data)))
	crw.responseLog.Body += string(data)

	return n, nil
}

func (crw *customResponseWriter) writeAndLog(content string) (int, error) {
	n, err := crw.conn.Write([]byte(content))
	if err != nil {
		crw.recordWriteError(err, n, content)
		return n, err
	}
	return n, nil
}

func (crw *customResponseWriter) recordWriteError(err error, bytesWritten int, content string) {
	stackTrace := formatStackTrace(getStackFrames(4))
	errorMessage := fmt.Sprintf("Writing %q failed: %v. Bytes Written: %d. Stack trace: %s", content, err, bytesWritten, strings.Join(stackTrace, " -> "))
	crw.logger.Logf(errorMessage)

	value, ok := crw.connectionHandler.responseLogs.Load(crw.conn.ID())
	if !ok {
		crw.logger.Logf("No log entry found for connection: %v", crw.conn.ID())
		return
	}

	logEntry := value.(*ConnectionLog)
	logEntry.IOError = errorMessage
	crw.connectionHandler.responseLogs.Store(crw.conn.ID(), logEntry)
}

func (crw *customResponseWriter) recordResponse() {
	value, ok := crw.connectionHandler.responseLogs.Load(crw.conn.ID())
	if !ok {
		crw.logger.Logf("No log entry found for connection: %v", crw.conn.ID())
		return
	}

	logEntry := value.(*ConnectionLog)
	logEntry.Response = crw.responseLog
	logEntry.Complete = true
	crw.connectionHandler.responseLogs.Store(crw.conn.ID(), logEntry)
}

func (crw *customResponseWriter) recordError(err error, content string) {
	value, ok := crw.connectionHandler.responseLogs.Load(crw.conn.ID())
	if !ok {
		log.Fatalf("No log entry found for connection: %v", crw.conn.ID())
	}

	logEntry := value.(*ConnectionLog)
	if logEntry.IOError == "" {
		logEntry.IOError = fmt.Sprintf("Write error: %v while sending content: %q", err, content)
	}

	crw.connectionHandler.responseLogs.Store(crw.conn.ID(), logEntry)
}

func (crw *customResponseWriter) finaliseResponse() (int, error) {
	if !crw.wroteHeader {
		crw.WriteHeader(http.StatusOK)
		return 0, nil
	}

	if crw.request.Method == http.MethodHead {
		return 0, nil
	}

	if !crw.haveTransferEncodingHeader() {
		return 0, nil
	}

	return crw.finaliseChunking()
}

func (crw *customResponseWriter) finaliseChunking() (int, error) {
	finalChunk := "0\r\n\r\n"
	n, err := crw.writeAndLog(finalChunk)
	if err != nil {
		return n, err
	}

	crw.logger.Logf("Chunk-Size: 0x0")
	crw.logger.Logf("End-Of-Chunked-Transfer")

	crw.responseLog.Chunks = append(crw.responseLog.Chunks, ChunkLog{
		Size: "0",
		Data: "",
	})

	return n, nil
}

func (crw *customResponseWriter) haveTransferEncodingHeader() bool {
	for _, v := range crw.headers.Values("Transfer-Encoding") {
		encodings := strings.Split(v, ",")
		for _, encoding := range encodings {
			if strings.TrimSpace(encoding) == "chunked" {
				return true
			}
		}
	}
	return false
}

func (crw *customResponseWriter) haveContentLengthHeader() bool {
	values, ok := crw.headers["Content-Length"]
	if !ok {
		return false
	}

	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			if _, err := strconv.ParseUint(v, 10, 64); err == nil {
				return true
			}
		}
	}
	return false
}

func (crw *customResponseWriter) httpError(errorMsg string, statusCode int) {
	crw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	crw.Header().Set("X-Content-Type-Options", "nosniff")
	crw.WriteHeader(statusCode)
	crw.Write([]byte(errorMsg))
}

// getStackFrames returns a slice of runtime.Frame, skipping the
// specified number of frames.
func getStackFrames(skip int) []runtime.Frame {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(skip, pcs)
	frames := runtime.CallersFrames(pcs[:n])

	var stackFrames []runtime.Frame

	for {
		frame, more := frames.Next()
		stackFrames = append(stackFrames, frame)
		if !more {
			break
		}
	}

	return stackFrames
}

// formatStackTrace converts a slice of runtime.Frame into a slice of
// formatted strings. Each string represents a stack frame with the
// function name, file name, and line number.
func formatStackTrace(frames []runtime.Frame) []string {
	var formattedFrames []string

	for _, frame := range frames {
		file := filepath.Base(frame.File)
		formattedFrames = append(formattedFrames, fmt.Sprintf("%s (%s:%d)", frame.Function, file, frame.Line))
	}

	return formattedFrames
}
