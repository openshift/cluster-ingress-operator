package azure

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

// metadataJSON is a minimal but valid response for the ARM metadata endpoint
// (<ARMEndpoint>/metadata/endpoints?api-version=1.0). audiences must be
// non-empty: EnvironmentFromURL indexes audiences[0].
const metadataJSON = `{
    "galleryEndpoint": "https://gallery.ussec.example/",
    "graphEndpoint": "https://graph.ussec.example/",
    "portalEndpoint": "https://portal.ussec.example/",
    "authentication": {
        "loginEndpoint": "https://login.ussec.example/",
        "audiences": ["https://management.ussec.example/"]
    }
}`

func TestResolveEnvironment(t *testing.T) {
	// Fake ARM metadata server so EnvironmentFromURL never touches the network.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(metadataJSON))
	}))
	defer srv.Close()

	tests := []struct {
		name        string
		config      Config
		wantErr     string // substring; "" means expect success
		wantRMEndpt string // expected ResourceManagerEndpoint on success
		wantMetaHit bool   // whether the metadata URL should have been called
	}{
		{
			name: "AzureUSSecCloud resolves from the ARM endpoint URL",
			config: Config{
				Environment: string(configv1.AzureUSSecCloud),
				ARMEndpoint: srv.URL,
			},
			wantRMEndpt: srv.URL, // EnvironmentFromURL defaults RM endpoint to the ARM URL
			wantMetaHit: true,
		},
		{
			name: "AzureUSSecCloud with empty ARM endpoint takes the URL branch and errors",
			config: Config{
				Environment: string(configv1.AzureUSSecCloud),
				ARMEndpoint: "",
			},
			// This exact message proves the URL branch was taken: the name branch
			// would instead say "no cloud environment matching the name".
			wantErr: "Metadata resource manager endpoint is empty",
		},
		{
			name: "AzureStackCloud shares the URL branch with IL6",
			config: Config{
				Environment: string(configv1.AzureStackCloud),
				ARMEndpoint: "",
			},
			wantErr: "Metadata resource manager endpoint is empty",
		},
		{
			name: "public cloud resolves by name, ignoring ARM endpoint",
			config: Config{
				Environment: string(configv1.AzurePublicCloud),
				ARMEndpoint: srv.URL, // must be ignored
			},
			wantRMEndpt: "https://management.azure.com/",
			wantMetaHit: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath = ""
			env, err := resolveEnvironment(tc.config)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if env.ResourceManagerEndpoint != tc.wantRMEndpt {
				t.Errorf("ResourceManagerEndpoint = %q, want %q",
					env.ResourceManagerEndpoint, tc.wantRMEndpt)
			}
			if tc.wantMetaHit && !strings.HasSuffix(gotPath, "/metadata/endpoints") {
				t.Errorf("expected metadata endpoint to be queried, got path %q", gotPath)
			}
			if !tc.wantMetaHit && gotPath != "" {
				t.Errorf("metadata endpoint should not have been queried, but got %q", gotPath)
			}
		})
	}
}
