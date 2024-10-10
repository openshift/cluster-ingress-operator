package ocpbugs48050

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type rawHTTPResponse struct {
	Proto      string
	StatusCode int
	Status     string
	Headers    [][2]string // A slice of key-value pairs to preserve order and duplicates.
	Trailers   [][2]string // A slice of key-value pairs to preserve order and duplicates.
	Body       []byte
}

func parseRawHTTPResponse(reader *bufio.Reader) (*rawHTTPResponse, error) {
	resp := &rawHTTPResponse{}

	// Parse the status line
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading status line: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")

	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed HTTP response status line: %s", line)
	}

	resp.Proto = parts[0]
	resp.StatusCode, err = strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("malformed HTTP status code %s: %w", parts[1], err)
	}
	resp.Status = parts[2]

	// Parse headers
	resp.Headers, err = parseHeadersOrTrailers(reader)
	if err != nil {
		return nil, fmt.Errorf("parsing headers: %w", err)
	}

	// Determine if the response is chunked
	isChunked := false
	for _, h := range resp.Headers {
		if strings.EqualFold(h[0], "Transfer-Encoding") {
			// Split the Transfer-Encoding header value into encodings
			encodings := strings.Split(h[1], ",")
			for i := range encodings {
				encodings[i] = strings.TrimSpace(strings.ToLower(encodings[i]))
			}
			if len(encodings) > 0 && encodings[len(encodings)-1] == "chunked" {
				isChunked = true
			} else {
				return nil, fmt.Errorf("unsupported Transfer-Encoding: %s", h[1])
			}
			break
		}
	}

	if !isChunked {
		return nil, fmt.Errorf("expected chunked Transfer-Encoding")
	}

	// Read body
	resp.Body, err = readChunkedBody(reader)
	if err != nil {
		return nil, fmt.Errorf("reading chunked body: %w", err)
	}

	// Parse trailers
	resp.Trailers, err = parseHeadersOrTrailers(reader)
	if err != nil {
		return nil, fmt.Errorf("parsing trailers: %w", err)
	}

	return resp, nil
}

func parseHeadersOrTrailers(reader *bufio.Reader) ([][2]string, error) {
	var headers [][2]string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading header line: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // End of headers or trailers
		}
		if i := strings.IndexByte(line, ':'); i == -1 {
			return nil, fmt.Errorf("malformed HTTP header line: %s", line)
		} else {
			key := strings.TrimSpace(line[:i])
			value := strings.TrimSpace(line[i+1:])
			headers = append(headers, [2]string{key, value})
		}
	}
	return headers, nil
}

func readChunkedBody(reader *bufio.Reader) ([]byte, error) {
	var body []byte
	for {
		// Read the chunk size line
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("reading chunk size: unexpected end of chunked body")
			}
			return nil, fmt.Errorf("reading chunk size: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return nil, fmt.Errorf("unexpected empty line in chunked body")
		}

		// Parse the chunk size
		chunkSize, err := strconv.ParseInt(line, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing chunk size: %w", err)
		}
		if chunkSize == 0 {
			break // End of chunked data
		}

		// Read the chunk data
		chunk := make([]byte, chunkSize)
		n, err := io.ReadFull(reader, chunk)
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("reading chunk data: chunk data size mismatch: expected %d bytes, got %d", chunkSize, n)
			}
			return nil, fmt.Errorf("reading chunk data: %w", err)
		}
		body = append(body, chunk...)

		// Read and validate the trailing CRLF
		crlf := make([]byte, 2)
		n, err = io.ReadFull(reader, crlf)
		if err != nil {
			return nil, fmt.Errorf("reading chunk trailing CRLF: %w", err)
		}
		if string(crlf) != "\r\n" {
			return nil, fmt.Errorf("invalid chunk trailing CRLF: %q", crlf)
		}
	}
	return body, nil
}
