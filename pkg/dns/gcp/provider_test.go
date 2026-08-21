package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/stretchr/testify/assert"
	gdnsv1 "google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
)

var (
	DefaultProject = "defaultProject"
)

func Test_ParseZone(t *testing.T) {
	cases := []struct {
		name         string
		providedZone string
		expectedID   string
		expectedZone string
		errStr       string
	}{{
		name:         "Valid Zone ID With Default Project",
		providedZone: "validZone",
		expectedID:   "defaultProject",
		expectedZone: "validZone",
	}, {
		name:         "Valid Embedded Zone and Project",
		providedZone: "projects/validProject/managedZones/validZone",
		expectedZone: "validZone",
		expectedID:   "validProject",
	}, {
		name:         "Invalid Too Many Values",
		providedZone: "projects/validProject/managedZones/validZone/extras",
		errStr:       "invalid managedZone: projects/validProject/managedZones/validZone/extras",
	}, {
		name:         "Invalid Too Few Values",
		providedZone: "projects/validProject/validZone",
		errStr:       "invalid managedZone: projects/validProject/validZone",
	}, {
		name:         "Invalid Zone String Projects",
		providedZone: "project/validProject/managedZones/validZone",
		errStr:       "invalid managedZone: project/validProject/managedZones/validZone",
	}, {
		name:         "Invalid Zone String Zone",
		providedZone: "projects/validProject/managedZone/validZone",
		errStr:       "invalid managedZone: projects/validProject/managedZone/validZone",
	},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, zoneID, err := ParseZone("defaultProject", tc.providedZone)
			if err != nil {
				assert.Equal(t, tc.errStr, err.Error())
			} else {
				assert.Equal(t, tc.expectedID, project)
				assert.Equal(t, tc.expectedZone, zoneID)
			}
		})
	}
}

// newTestProvider creates a Provider backed by the given httptest server.
func newTestProvider(t *testing.T, server *httptest.Server) *Provider {
	t.Helper()
	svc, err := gdnsv1.NewService(t.Context(),
		option.WithoutAuthentication(),
		option.WithEndpoint(server.URL),
	)
	assert.NoError(t, err)
	return &Provider{
		config:     Config{Project: "test-project"},
		dnsService: svc,
	}
}

func Test_Ensure_ConflictCallsReplace(t *testing.T) {
	// First Ensure call returns 409 (record exists). The provider
	// should then call Replace, which lists the existing record and
	// submits a Change with both Deletions and Additions.
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		switch {
		case n == 1:
			// First call: Ensure's Changes.Create → 409 Conflict.
			w.WriteHeader(http.StatusConflict)
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"code":    409,
					"message": "already exists",
				},
			}); err != nil {
				t.Errorf("failed to encode conflict response: %v", err)
			}
		case r.Method == "GET":
			// Replace lists existing records.
			if err := json.NewEncoder(w).Encode(&gdnsv1.ResourceRecordSetsListResponse{
				Rrsets: []*gdnsv1.ResourceRecordSet{{
					Name:    "test.example.com.",
					Type:    "A",
					Ttl:     300,
					Rrdatas: []string{"10.0.0.1"},
				}},
			}); err != nil {
				t.Errorf("failed to encode list response: %v", err)
			}
		default:
			// Replace's Changes.Create (atomic change).
			if err := json.NewEncoder(w).Encode(&gdnsv1.Change{
				Status: "pending",
			}); err != nil {
				t.Errorf("failed to encode change response: %v", err)
			}
		}
	}))
	defer server.Close()

	p := newTestProvider(t, server)
	record := &iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "test.example.com.",
			Targets:    []string{"10.0.0.2"},
			RecordType: iov1.ARecordType,
			RecordTTL:  300,
		},
	}
	zone := configv1.DNSZone{ID: "test-zone"}

	err := p.Ensure(record, zone)
	assert.NoError(t, err, "Ensure should succeed via Replace fallback on 409")
	assert.GreaterOrEqual(t, int(callCount.Load()), 2,
		"expected at least 2 API calls: Ensure (409) then Replace")
}
