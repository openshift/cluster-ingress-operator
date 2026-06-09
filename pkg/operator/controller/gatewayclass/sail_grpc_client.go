package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	pb "github.com/istio-ecosystem/sail-operator/api/sidecar/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// SailOptions specifies the desired state for the istiod installation.
// This mirrors install.Options from the sail-operator library but uses
// raw JSON for values so CIO doesn't need the generated Values types.
type SailOptions struct {
	Namespace      string
	Version        string
	Revision       string
	Values         map[string]any
	ManageCRDs     bool
	IncludeAllCRDs bool
}

// SailCRDManagementState represents the state of CRD management.
type SailCRDManagementState string

const (
	SailCRDStateUnknown  SailCRDManagementState = "Unknown"
	SailCRDStateReady    SailCRDManagementState = "Ready"
	SailCRDStateNotReady SailCRDManagementState = "NotReady"
	SailCRDStateError    SailCRDManagementState = "Error"
)

// SailCRDInfo contains information about a managed CRD.
type SailCRDInfo struct {
	Name    string
	Managed bool
	Ready   bool
}

// SailStatus contains the current state of the installation.
type SailStatus struct {
	Generation uint64
	CRDState   SailCRDManagementState
	CRDMessage string
	CRDs       []SailCRDInfo
	Installed  bool
	Version    string
	Error      error
}

// OLMCRDCheckFunc checks whether an OLM-managed CRD should be overwritten.
// It receives the CRD name and labels from the sidecar's callback.
type OLMCRDCheckFunc func(ctx context.Context, crdName string, crdLabels map[string]string) bool

// SailGRPCInstaller implements SailLibraryInstaller by communicating
// with the sail-sidecar process over gRPC on a Unix domain socket.
type SailGRPCInstaller struct {
	socketPath   string
	olmCheckFunc OLMCRDCheckFunc

	mu       sync.Mutex
	conn     *grpc.ClientConn
	client   pb.SailLibraryClient
	stream   pb.SailLibrary_SessionClient
	notifyCh chan struct{}

	statusMu      sync.RWMutex
	currentStatus SailStatus

	lastOpts *SailOptions
}

// NewSailGRPCInstaller creates a new gRPC-backed installer.
func NewSailGRPCInstaller(socketPath string, olmCheck OLMCRDCheckFunc) *SailGRPCInstaller {
	return &SailGRPCInstaller{
		socketPath:   socketPath,
		olmCheckFunc: olmCheck,
		notifyCh:     make(chan struct{}, 1),
	}
}

// Start connects to the sidecar and opens a bidirectional Session stream.
// The returned channel receives a notification each time a reconciliation
// completes in the sidecar.
func (s *SailGRPCInstaller) Start(ctx context.Context) (<-chan struct{}, error) {
	conn, err := s.dialWithRetry(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to sail sidecar: %w", err)
	}
	s.conn = conn
	s.client = pb.NewSailLibraryClient(conn)

	stream, err := s.client.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open session stream: %w", err)
	}
	s.stream = stream

	go s.readLoop(ctx)

	return s.notifyCh, nil
}

func (s *SailGRPCInstaller) dialWithRetry(ctx context.Context) (*grpc.ClientConn, error) {
	target := "unix://" + s.socketPath
	var conn *grpc.ClientConn
	var err error

	for attempt := 0; attempt < 30; attempt++ {
		conn, err = grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			return conn, nil
		}
		log.Info("waiting for sail sidecar socket", "attempt", attempt+1, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return nil, fmt.Errorf("gave up connecting to sail sidecar after retries: %w", err)
}

// readLoop processes messages from the sidecar on the session stream.
// On stream errors, it attempts to reconnect with exponential backoff
// and replays the last Apply if one was sent.
func (s *SailGRPCInstaller) readLoop(ctx context.Context) {
	for {
		msg, err := s.stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error(err, "session stream read error, attempting reconnection")
			if s.reconnect(ctx) {
				continue
			}
			return
		}

		switch m := msg.Msg.(type) {
		case *pb.ServerMessage_StatusUpdate:
			s.handleStatusUpdate(m.StatusUpdate)

		case *pb.ServerMessage_OlmCrdRequest:
			s.handleOLMRequest(ctx, m.OlmCrdRequest)

		case *pb.ServerMessage_ApplyResponse:
			if m.ApplyResponse.Error != "" {
				log.Info("sidecar apply error", "error", m.ApplyResponse.Error)
			}

		case *pb.ServerMessage_UninstallResponse:
			if m.UninstallResponse.Error != "" {
				log.Info("sidecar uninstall error", "error", m.UninstallResponse.Error)
			}
		}
	}
}

// reconnect attempts to re-establish the gRPC session after a stream error.
// Returns true if reconnection succeeded.
func (s *SailGRPCInstaller) reconnect(ctx context.Context) bool {
	backoff := time.Second
	for attempt := 0; attempt < 10; attempt++ {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}

		stream, err := s.client.Session(ctx)
		if err != nil {
			log.Info("reconnection attempt failed", "attempt", attempt+1, "error", err)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}

		s.mu.Lock()
		s.stream = stream
		lastOpts := s.lastOpts
		s.mu.Unlock()

		log.Info("reconnected to sail sidecar")

		// Replay last Apply so the sidecar converges to desired state
		if lastOpts != nil {
			if err := s.Apply(*lastOpts); err != nil {
				log.Error(err, "failed to replay Apply after reconnection")
			}
		}
		return true
	}

	log.Info("gave up reconnecting to sail sidecar")
	return false
}

func (s *SailGRPCInstaller) handleStatusUpdate(update *pb.StatusUpdate) {
	if update == nil || update.Status == nil {
		return
	}
	status := protoToSailStatus(update.Status)
	s.statusMu.Lock()
	s.currentStatus = status
	s.statusMu.Unlock()

	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func (s *SailGRPCInstaller) handleOLMRequest(ctx context.Context, req *pb.OverwriteOLMCRDRequest) {
	overwrite := false
	if s.olmCheckFunc != nil {
		overwrite = s.olmCheckFunc(ctx, req.CrdName, req.CrdLabels)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		return
	}
	_ = s.stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_OlmCrdResponse{
			OlmCrdResponse: &pb.OverwriteOLMCRDResponse{
				RequestId: req.RequestId,
				Overwrite: overwrite,
			},
		},
	})
}

// Apply sends the desired installation state to the sidecar.
func (s *SailGRPCInstaller) Apply(opts SailOptions) error {
	valuesJSON, err := json.Marshal(opts.Values)
	if err != nil {
		return fmt.Errorf("failed to marshal values: %w", err)
	}

	s.mu.Lock()
	s.lastOpts = &opts
	stream := s.stream
	s.mu.Unlock()

	if stream == nil {
		return fmt.Errorf("session stream not connected")
	}

	return stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Apply{
			Apply: &pb.ApplyRequest{
				Namespace:      opts.Namespace,
				Version:        opts.Version,
				Revision:       opts.Revision,
				ValuesJson:     valuesJSON,
				ManageCrds:     opts.ManageCRDs,
				IncludeAllCrds: opts.IncludeAllCRDs,
			},
		},
	})
}

// Uninstall sends an uninstall request to the sidecar.
func (s *SailGRPCInstaller) Uninstall(ctx context.Context, namespace, revision string) error {
	s.mu.Lock()
	stream := s.stream
	s.mu.Unlock()

	if stream == nil {
		return fmt.Errorf("session stream not connected")
	}

	return stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Uninstall{
			Uninstall: &pb.UninstallRequest{
				Namespace: namespace,
				Revision:  revision,
			},
		},
	})
}

// Status returns the latest status received from the sidecar.
func (s *SailGRPCInstaller) Status() SailStatus {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.currentStatus
}

// Enqueue triggers a reconciliation in the sidecar without changing state.
func (s *SailGRPCInstaller) Enqueue() {
	s.mu.Lock()
	stream := s.stream
	s.mu.Unlock()

	if stream == nil {
		return
	}
	_ = stream.Send(&pb.ClientMessage{
		Msg: &pb.ClientMessage_Enqueue{Enqueue: &pb.EnqueueRequest{}},
	})
}

func protoToSailStatus(resp *pb.StatusResponse) SailStatus {
	status := SailStatus{
		Generation: resp.Generation,
		CRDState:   SailCRDManagementState(resp.CrdState),
		CRDMessage: resp.CrdMessage,
		Installed:  resp.Installed,
		Version:    resp.Version,
	}
	if resp.Error != "" {
		status.Error = fmt.Errorf("%s", resp.Error)
	}
	for _, c := range resp.Crds {
		status.CRDs = append(status.CRDs, SailCRDInfo{
			Name:    c.Name,
			Managed: c.Managed,
			Ready:   c.Ready,
		})
	}
	return status
}
