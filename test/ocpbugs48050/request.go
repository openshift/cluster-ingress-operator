package ocpbugs48050

import (
	"bufio"
	"net/http"
)

// customRequestReader parses the HTTP request from the connection.
func customRequestReader(conn connection) (*http.Request, error) {
	reader := bufio.NewReader(conn)

	req, err := http.ReadRequest(reader)
	if err != nil {
		return nil, err
	}

	req.RemoteAddr = conn.RemoteAddr().String()

	return req, nil
}
