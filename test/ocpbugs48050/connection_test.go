package ocpbugs48050

import (
	"bytes"
	"net"
	"time"
)

// MockConnection is a mock implementation of the Connection
// interface.
type MockConnection struct {
	readBuffer  *bytes.Buffer
	writeBuffer *bytes.Buffer
	id          ConnectionID
}

// Type assertion to ensure MockConnection implements Connection.
var _ connection = (*MockConnection)(nil)

func NewMockConnection() *MockConnection {
	return &MockConnection{
		readBuffer:  new(bytes.Buffer),
		writeBuffer: new(bytes.Buffer),
		id:          ConnectionID(time.Now().UnixNano()),
	}
}

func (m *MockConnection) ID() ConnectionID {
	return m.id
}

func (m *MockConnection) Read(p []byte) (n int, err error) {
	return m.readBuffer.Read(p)
}

func (m *MockConnection) Write(p []byte) (n int, err error) {
	return m.writeBuffer.Write(p)
}

func (m *MockConnection) Close() error {
	return nil
}

func (m *MockConnection) RemoteAddr() net.Addr {
	return &net.TCPAddr{
		IP:   []byte{127, 0, 0, 1},
		Port: 12345,
	}
}

func (m *MockConnection) LocalAddr() net.Addr {
	return &net.TCPAddr{
		IP:   []byte{127, 0, 0, 1},
		Port: 8080,
	}
}

// WriteRequest writes a raw HTTP request to the mock connection's
// read buffer.
func (m *MockConnection) WriteRequest(request string) {
	m.readBuffer.WriteString(request)
}

// ReadResponse retrieves the raw HTTP response from the mock
// connection's write buffer.
func (m *MockConnection) ReadResponse() string {
	return m.writeBuffer.String()
}
