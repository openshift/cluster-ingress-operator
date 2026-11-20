package gcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gdnsv1 "google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
)

var (
	DefaultProject = "defaultProject"
)

func Test_Replace_TypeTransitions(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		oldTypes  []string
		newType   string
		keepTypes []string
		wantError bool
	}{
		{name: "CNAME to A", oldTypes: []string{"CNAME"}, newType: "A"},
		{name: "CNAME to AAAA", oldTypes: []string{"CNAME"}, newType: "AAAA"},
		{name: "addresses to CNAME", oldTypes: []string{"A", "AAAA"}, newType: "CNAME"},
		{name: "replace A preserves AAAA and TXT", oldTypes: []string{"A", "AAAA", "TXT"}, newType: "A", keepTypes: []string{"AAAA", "TXT"}},
		{name: "replace AAAA preserves A and TXT", oldTypes: []string{"A", "AAAA", "TXT"}, newType: "AAAA", keepTypes: []string{"A", "TXT"}},
		{name: "CNAME does not remove unrelated TXT", oldTypes: []string{"A", "TXT"}, newType: "CNAME", wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			name := "test.example.com."
			records := map[string]*gdnsv1.ResourceRecordSet{}
			var initial []*gdnsv1.ResourceRecordSet
			oldTargets := map[string]string{"A": "192.0.2.1", "AAAA": "2001:db8::1", "CNAME": "old.example.com.", "TXT": `"keep"`}
			for _, recordType := range testCase.oldTypes {
				record := &gdnsv1.ResourceRecordSet{Name: name, Type: recordType, Ttl: 60, Rrdatas: []string{oldTargets[recordType]}}
				records[record.Name+record.Type] = record
				initial = append(initial, record)
			}
			other := &gdnsv1.ResourceRecordSet{Name: "other.example.com.", Type: "CNAME", Ttl: 60, Rrdatas: []string{"other-target."}}
			records[other.Name+other.Type] = other
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodGet {
					var matches []*gdnsv1.ResourceRecordSet
					for _, record := range initial {
						if record.Name == request.URL.Query().Get("name") && (request.URL.Query().Get("type") == "" || record.Type == request.URL.Query().Get("type")) {
							matches = append(matches, record)
						}
					}
					response := gdnsv1.ResourceRecordSetsListResponse{Rrsets: matches}
					if request.URL.Query().Get("pageToken") != "" {
						response.Rrsets = matches[1:]
					} else if len(matches) > 1 {
						response.Rrsets = matches[:1]
						response.NextPageToken = "next"
					}
					assert.NoError(t, json.NewEncoder(writer).Encode(response))
					return
				}
				posts++
				var change gdnsv1.Change
				if err := json.NewDecoder(request.Body).Decode(&change); err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				updated := map[string]*gdnsv1.ResourceRecordSet{}
				for key, record := range records {
					updated[key] = record
				}
				for _, deletion := range change.Deletions {
					key := deletion.Name + deletion.Type
					if !reflect.DeepEqual(updated[key], deletion) {
						writer.WriteHeader(http.StatusConflict)
						return
					}
					delete(updated, key)
				}
				for _, addition := range change.Additions {
					if updated[addition.Name+addition.Type] != nil {
						writer.WriteHeader(http.StatusConflict)
						return
					}
					updated[addition.Name+addition.Type] = addition
				}
				for _, record := range updated {
					if record.Type != "CNAME" && updated[record.Name+"CNAME"] != nil {
						writer.WriteHeader(http.StatusConflict)
						return
					}
				}
				records = updated
				assert.NoError(t, json.NewEncoder(writer).Encode(gdnsv1.Change{}))
			}))
			defer server.Close()
			provider := newTestProvider(t, server)
			newTargets := map[string]string{"A": "192.0.2.2", "AAAA": "2001:db8::2", "CNAME": "new.example.com."}
			desired := &iov1.DNSRecord{Spec: iov1.DNSRecordSpec{DNSName: name, RecordType: iov1.DNSRecordType(testCase.newType), RecordTTL: 300, Targets: []string{newTargets[testCase.newType]}}}
			err := provider.Replace(desired, configv1.DNSZone{ID: "test-zone"})
			if testCase.wantError {
				require.Error(t, err)
				for _, record := range initial {
					assert.Equal(t, record, records[record.Name+record.Type])
				}
			} else {
				require.NoError(t, err)
				expected := map[string]*gdnsv1.ResourceRecordSet{other.Name + other.Type: other, name + testCase.newType: resourceRecordSet(desired)}
				for _, recordType := range testCase.keepTypes {
					for _, record := range initial {
						if record.Type == recordType {
							expected[name+recordType] = record
						}
					}
				}
				assert.Equal(t, expected, records)
			}
			assert.Equal(t, other, records[other.Name+other.Type])
			assert.Equal(t, 1, posts)
		})
	}
}

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

func Test_Replace_AtomicChange(t *testing.T) {
	// Verify that Replace sends a single Change containing both
	// deletions (old record) and additions (new record).
	var capturedBody []byte
	var postCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			if err := json.NewEncoder(w).Encode(&gdnsv1.ResourceRecordSetsListResponse{
				Rrsets: []*gdnsv1.ResourceRecordSet{{
					Name: "test.example.com.", Type: "A",
					Ttl: 300, Rrdatas: []string{"10.0.0.1"},
				}},
			}); err != nil {
				t.Errorf("failed to encode list response: %v", err)
			}
		case "POST":
			postCount++
			capturedBody, _ = io.ReadAll(r.Body)
			if err := json.NewEncoder(w).Encode(&gdnsv1.Change{Status: "pending"}); err != nil {
				t.Errorf("failed to encode change response: %v", err)
			}
		}
	}))
	defer server.Close()

	p := newTestProvider(t, server)
	record := &iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName: "test.example.com.", Targets: []string{"10.0.0.2"},
			RecordType: iov1.ARecordType, RecordTTL: 300,
		},
	}
	err := p.Replace(record, configv1.DNSZone{ID: "test-zone"})
	assert.NoError(t, err)
	assert.Equal(t, 1, postCount,
		"Replace must send exactly one POST (atomic change)")

	var change gdnsv1.Change
	assert.NoError(t, json.Unmarshal(capturedBody, &change))
	assert.Len(t, change.Deletions, 1, "expected 1 deletion")
	assert.Equal(t, "10.0.0.1", change.Deletions[0].Rrdatas[0])
	assert.Len(t, change.Additions, 1, "expected 1 addition")
	assert.Equal(t, "10.0.0.2", change.Additions[0].Rrdatas[0])
}
