package ocpbugs48050

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Custom reader that injects an error at a specific point.
type errorReader struct {
	data       []byte
	readErrAt  int // Position to trigger the error
	bytesRead  int
	errToThrow error
}

func (er *errorReader) Read(p []byte) (int, error) {
	if er.bytesRead >= er.readErrAt {
		return 0, er.errToThrow
	}
	n := copy(p, er.data[er.bytesRead:])
	if er.bytesRead+n > er.readErrAt {
		n = er.readErrAt - er.bytesRead
	}
	er.bytesRead += n
	if er.bytesRead >= er.readErrAt {
		return n, er.errToThrow
	}
	return n, nil
}

// Helper function to simulate reading a full HTTP response from a string.
func readResponse(t *testing.T, rawResponse string) *rawHTTPResponse {
	t.Helper()
	reader := bufio.NewReader(strings.NewReader(rawResponse))
	resp, err := parseRawHTTPResponse(reader)
	if err != nil {
		t.Fatalf("parseRawHTTPResponse() error: %v", err)
	}
	return resp
}

func TestParseRawHTTPResponse_Success(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"D\r\n" + // Chunk size in hex (13 bytes)
		"Hello, world!\r\n" + // Chunk data
		"0\r\n\r\n" // End of chunks

	resp := readResponse(t, rawResponse)

	if resp.Proto != "HTTP/1.1" {
		t.Errorf("expected protocol 'HTTP/1.1', got '%s'", resp.Proto)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected status code 200, got %d", resp.StatusCode)
	}
	if resp.Status != "OK" {
		t.Errorf("expected status 'OK', got '%s'", resp.Status)
	}

	expectedBody := "Hello, world!"
	if string(resp.Body) != expectedBody {
		t.Errorf("expected body '%s', got '%s'", expectedBody, string(resp.Body))
	}
}

func TestParseRawHTTPResponse_ChunkedSuccess(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"7\r\n" +
		"Mozilla\r\n" +
		"9\r\n" +
		"Developer\r\n" +
		"7\r\n" +
		"Network\r\n" +
		"0\r\n\r\n"

	resp := readResponse(t, rawResponse)

	expectedBody := "MozillaDeveloperNetwork"
	if string(resp.Body) != expectedBody {
		t.Errorf("expected body '%s', got '%s'", expectedBody, string(resp.Body))
	}
}

func TestParseRawHTTPResponse_MalformedStatusLine(t *testing.T) {
	rawResponse := "HTTP/1.1 OK\r\n" +
		"Content-Length: 5\r\n\r\n" +
		"Hello"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "malformed HTTP response status line") {
		t.Fatalf("expected malformed HTTP status line error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_InvalidStatusCode(t *testing.T) {
	rawResponse := "HTTP/1.1 abc OK\r\n" +
		"Content-Length: 5\r\n\r\n" +
		"Hello"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "malformed HTTP status code") {
		t.Fatalf("expected malformed HTTP status code error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_MalformedHeader(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Content-Length 5\r\n\r\n" + // Missing colon in header
		"Hello"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "malformed HTTP header line") {
		t.Fatalf("expected malformed HTTP header line error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_InvalidChunkSize(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"ZZ\r\n" + // Invalid chunk size (ZZ is not a valid hex number)
		"Mozilla\r\n" +
		"0\r\n\r\n"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "parsing chunk size") {
		t.Fatalf("expected parsing chunk size error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ChunkDataReadError(t *testing.T) {
	// The response simulates an incomplete chunked body.
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"7\r\n" + // Declares a chunk of 7 bytes
		"Mozill" // Only 6 bytes provided, no trailing CRLF or further data

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "chunk data size mismatch") {
		t.Fatalf("expected chunk data size mismatch error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_Trailers(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"Trailer: Expires\r\n\r\n" +
		"7\r\n" +
		"Mozilla\r\n" +
		"0\r\n" +
		"Expires: Wed, 21 Oct 2021 07:28:00 GMT\r\n\r\n"

	resp := readResponse(t, rawResponse)

	if len(resp.Trailers) != 1 || resp.Trailers[0][0] != "Expires" {
		t.Errorf("expected trailer 'Expires', got: %+v", resp.Trailers)
	}
}

func TestParseRawHTTPResponse_ReadingStatusLineError(t *testing.T) {
	// Simulate an incomplete status line (connection closes
	// before it's complete).
	rawResponse := "HTTP/1.1"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "reading status line") {
		t.Fatalf("expected reading status line error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ParsingTrailersError(t *testing.T) {
	// Simulate an incomplete trailer parsing (connection closes or invalid trailer format)
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"Trailer: Expires\r\n\r\n" +
		"7\r\n" +
		"Mozilla\r\n" +
		"0\r\n" + // End of chunked body, but the trailer is incomplete
		"Expires: "

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "parsing trailers") {
		t.Fatalf("expected parsing trailers error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ReadingHeaderLineError(t *testing.T) {
	// Simulate an incomplete header (connection closes or invalid format)
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Content-Len"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "reading header line") {
		t.Fatalf("expected reading header line error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ChunkDataSizeMismatchError(t *testing.T) {
	// Simulate a chunk size mismatch (7 bytes expected, but only 4 provided)
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"7\r\n" + // Declares a chunk of 7 bytes
		"Mozi" // Only 4 bytes provided instead of 7
		// Missing the rest of the chunk data and CRLF

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "chunk data size mismatch") {
		t.Fatalf("expected chunk data size mismatch error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_InvalidChunkTrailingCRLFError(t *testing.T) {
	// Simulate an invalid CRLF after the chunk data
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"7\r\n" + // Declares a chunk of 7 bytes
		"Mozilla" + // Provides exactly 7 bytes
		"X\r\n" + // Invalid CRLF (should be \r\n, but we use X instead)
		"0\r\n\r\n" // End of chunked transfer

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "invalid chunk trailing CRLF") {
		t.Fatalf("expected invalid chunk trailing CRLF error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ReadingChunkSizeError(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n"

	// Simulate an error when reading the chunk size
	er := &errorReader{
		data:       []byte(rawResponse),
		readErrAt:  len(rawResponse), // Error right after headers
		errToThrow: fmt.Errorf("simulated read error"),
	}
	reader := bufio.NewReader(er)

	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "reading chunk size") {
		t.Fatalf("expected reading chunk size error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ReadingChunkDataError(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"7\r\n" +
		"Mozill" // 6 bytes, but we will simulate an error before the 7th byte

	// Simulate an error after reading 6 bytes of chunk data
	er := &errorReader{
		data:       []byte(rawResponse),
		readErrAt:  len(rawResponse), // Error occurs when reading the 7th byte
		errToThrow: fmt.Errorf("simulated read error"),
	}
	er.data = append(er.data, []byte("a")...) // Append the 7th byte to simulate data presence
	reader := bufio.NewReader(er)

	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "reading chunk data") {
		t.Fatalf("expected reading chunk data error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ReadingChunkTrailingCRLFError(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"7\r\n" +
		"Mozilla" // 7 bytes of chunk data

	// Simulate an error when reading the trailing CRLF
	er := &errorReader{
		data:       []byte(rawResponse),
		readErrAt:  len(rawResponse), // Error occurs right before CRLF
		errToThrow: fmt.Errorf("simulated read error"),
	}
	// Normally, CRLF would be here, but we'll simulate an error before it's read
	reader := bufio.NewReader(er)

	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "reading chunk trailing CRLF") {
		t.Fatalf("expected reading chunk trailing CRLF error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_UnexpectedEOFReadingChunkSize(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n"
		// No chunk size line; EOF occurs here

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "unexpected end of chunked body") {
		t.Fatalf("expected unexpected end of chunked body error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ChunkedBodyWithEmptyLines(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		"\r\n" + // Empty line to be rejected
		"7\r\n" +
		"Mozilla\r\n" +
		"\r\n" + // Another empty line
		"9\r\n" +
		"Developer\r\n" +
		"0\r\n\r\n"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "unexpected empty line in chunked body") {
		t.Fatalf("expected error due to empty line in chunked body, got: %v", err)
	}
}

func TestParseRawHTTPResponse_ReadingChunkedBodyError(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"D\r\n" + // Chunk size in hex (13 bytes)
		"Hello, world!" // Chunk data (missing CRLF to simulate error)

	// Simulate an error after reading part of the chunk data
	er := &errorReader{
		data:       []byte(rawResponse),
		readErrAt:  len(rawResponse), // Error occurs when reading the CRLF after chunk data
		errToThrow: fmt.Errorf("simulated read error"),
	}
	reader := bufio.NewReader(er)

	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "reading chunk trailing CRLF") {
		t.Fatalf("expected reading chunk trailing CRLF error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_UnsupportedTransferEncoding(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Transfer-Encoding: gzip\r\n\r\n" + // Transfer-Encoding is gzip, not chunked
		"some body data"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "unsupported Transfer-Encoding") {
		t.Fatalf("expected unsupported Transfer-Encoding error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_MissingTransferEncoding(t *testing.T) {
	rawResponse := "HTTP/1.1 200 OK\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"some body data"

	reader := bufio.NewReader(strings.NewReader(rawResponse))
	_, err := parseRawHTTPResponse(reader)
	if err == nil || !strings.Contains(err.Error(), "expected chunked Transfer-Encoding") {
		t.Fatalf("expected expected chunked Transfer-Encoding error, got: %v", err)
	}
}

func TestParseRawHTTPResponse_EndpointHandlers(t *testing.T) {
	type testCase struct {
		Name                      string
		RequestPath               string
		ExpectedStatus            int
		ExpectedTransferEncodings []string
		ExpectedBody              string
		ExpectParseError          bool
	}

	testCases := []testCase{
		{
			Name:                      "Healthz Endpoint",
			RequestPath:               "/healthz",
			ExpectedStatus:            http.StatusOK,
			ExpectedTransferEncodings: []string{"chunked"},
			ExpectedBody:              "/healthz",
			ExpectParseError:          false,
		},
		{
			Name:                      "Single-TE Endpoint",
			RequestPath:               "/single-te",
			ExpectedStatus:            http.StatusOK,
			ExpectedTransferEncodings: []string{"chunked"},
			ExpectedBody:              "/single-te",
			ExpectParseError:          false,
		},
		{
			Name:                      "Duplicate-TE Endpoint",
			RequestPath:               "/duplicate-te",
			ExpectedStatus:            http.StatusOK,
			ExpectedTransferEncodings: []string{"chunked", "chunked"},
			ExpectedBody:              "/duplicate-te",
			ExpectParseError:          false,
		},
	}

	createMockConnection := func(testCase testCase) *MockConnection {
		mockConn := NewMockConnection()
		request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n", testCase.RequestPath)
		mockConn.WriteRequest(request)
		return mockConn
	}

	processRequest := func(server *httpServer, mockConn *MockConnection) error {
		handler := newConnectionHandler(server, &devNullLogger{})
		return handler.processConnection(mockConn)
	}

	verifyResponse := func(t *testing.T, testCase testCase, mockConn *MockConnection) {
		t.Helper()
		responseData := mockConn.ReadResponse()
		reader := bufio.NewReader(strings.NewReader(responseData))

		httpResponse, err := parseRawHTTPResponse(reader)
		if err != nil {
			if testCase.ExpectParseError {
				t.Logf("[%s] Parsing failed as expected: %v", testCase.Name, err)
				return
			}
			t.Fatalf("[%s] Failed to parse HTTP response: %v", testCase.Name, err)
		}

		if testCase.ExpectParseError {
			t.Errorf("[%s] Expected parsing to fail, but it succeeded", testCase.Name)
			return
		}

		if httpResponse.StatusCode != testCase.ExpectedStatus {
			t.Errorf("[%s] Expected status code %d, got %d", testCase.Name, testCase.ExpectedStatus, httpResponse.StatusCode)
		}

		var actualTEHeaders []string
		for _, header := range httpResponse.Headers {
			if strings.EqualFold(header[0], "Transfer-Encoding") {
				actualTEHeaders = append(actualTEHeaders, header[1])
			}
		}

		if len(actualTEHeaders) != len(testCase.ExpectedTransferEncodings) {
			t.Errorf("[%s] Expected %d Transfer-Encoding header(s), got %d", testCase.Name, len(testCase.ExpectedTransferEncodings), len(actualTEHeaders))
		} else {
			for i, expectedTE := range testCase.ExpectedTransferEncodings {
				if actualTEHeaders[i] != expectedTE {
					t.Errorf("[%s] Expected Transfer-Encoding header #%d to be %q, got %q", testCase.Name, i+1, expectedTE, actualTEHeaders[i])
				}
			}
		}

		if string(httpResponse.Body) != testCase.ExpectedBody {
			t.Errorf("[%s] Expected body %q, got %q", testCase.Name, testCase.ExpectedBody, string(httpResponse.Body))
		}
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			httpServer := newHTTPServer()

			handlers := map[string]http.HandlerFunc{
				"/healthz":      healthzHandler,
				"/single-te":    singleTransferEncodingHandler,
				"/duplicate-te": duplicateTransferEncodingHandler,
			}

			handler, _ := handlers[tc.RequestPath]
			httpServer.Handle(tc.RequestPath, handler)
			mockConn := createMockConnection(tc)

			if err := processRequest(httpServer, mockConn); err != nil {
				t.Fatalf("[%s] ProcessConnection failed: %v", tc.Name, err)
			}

			verifyResponse(t, tc, mockConn)
		})
	}
}
