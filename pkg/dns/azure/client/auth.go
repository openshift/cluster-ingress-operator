package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/msi-dataplane/pkg/dataplane"
	"github.com/openshift/cluster-ingress-operator/pkg/util/filewatcher"
)

var watchCertificateFileOnce sync.Once

func getAzureCredentials(config Config) (azcore.TokenCredential, error) {
	cloudConfig := ParseCloudEnvironment(config)

	// Fallback to using tenant ID from env variable if not set.
	if strings.TrimSpace(config.TenantID) == "" {
		config.TenantID = os.Getenv("AZURE_TENANT_ID")
		if strings.TrimSpace(config.TenantID) == "" {
			return nil, errors.New("empty tenant ID")
		}
	}

	// Fallback to using client ID from env variable if not set.
	if strings.TrimSpace(config.ClientID) == "" {
		config.ClientID = os.Getenv("AZURE_CLIENT_ID")
		if strings.TrimSpace(config.ClientID) == "" {
			return nil, errors.New("empty client ID")
		}
	}

	// Fallback to using client secret from env variable if not set.
	if strings.TrimSpace(config.ClientSecret) == "" {
		config.ClientSecret = os.Getenv("AZURE_CLIENT_SECRET")
		// Skip validation; fallback to token (below) if env variable is also not set.
	}

	// Fallback to using federated token file from env variable if not set.
	if strings.TrimSpace(config.FederatedTokenFile) == "" {
		config.FederatedTokenFile = os.Getenv("AZURE_FEDERATED_TOKEN_FILE")
		if strings.TrimSpace(config.FederatedTokenFile) == "" {
			// Default to a generic token file location.
			config.FederatedTokenFile = "/var/run/secrets/openshift/serviceaccount/token"
		}
	}

	var cred azcore.TokenCredential
	// UserAssignedIdentityCredentials for managed Azure HCP
	userAssignedIdentityCredentialsFilePath := os.Getenv("MANAGED_AZURE_HCP_CREDENTIALS_FILE_PATH")
	managedIdentityClientID := os.Getenv("ARO_HCP_MI_CLIENT_ID")
	if managedIdentityClientID != "" {
		// Managed Identity Override for ARO HCP. In ARO HCP, we ignore the values provided for AZURE_TENANT_ID and
		// AZURE_CLIENT_ID and use ARO_HCP_TENANT_ID and ARO_HCP_MI_CLIENT_ID instead.
		options := &azidentity.ClientCertificateCredentialOptions{
			ClientOptions: azcore.ClientOptions{
				Cloud: cloudConfig,
			},
			SendCertificateChain: true,
		}

		tenantID := os.Getenv("ARO_HCP_TENANT_ID")
		certPath := os.Getenv("ARO_HCP_CLIENT_CERTIFICATE_PATH")

		certData, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read certificate file %q: %w", certPath, err)
		}

		certs, key, err := azidentity.ParseCertificates(certData, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate data %q: %w", certPath, err)
		}

		// Watch the certificate for changes; if the certificate changes, the pod will be restarted.
		// This starts only one occurrence of the file watcher, which watches the file, certPath.
		var fileWatcherError error
		watchCertificateFileOnce.Do(func() {
			if err = filewatcher.WatchFileForChanges(certPath); err != nil {
				fileWatcherError = err
			}
		})
		if fileWatcherError != nil {
			return nil, fmt.Errorf("failed to watch certificate file %q: %w", certPath, fileWatcherError)
		}

		cred, err = azidentity.NewClientCertificateCredential(tenantID, managedIdentityClientID, certs, key, options)
		if err != nil {
			return nil, err
		}
	} else if userAssignedIdentityCredentialsFilePath != "" {
		options := azcore.ClientOptions{
			Cloud: cloudConfig,
		}
		var err error
		cred, err = dataplane.NewUserAssignedIdentityCredential(context.Background(), userAssignedIdentityCredentialsFilePath, dataplane.WithClientOpts(options))
		if err != nil {
			return nil, err
		}
	} else if config.AzureWorkloadIdentityEnabled && strings.TrimSpace(config.ClientSecret) == "" {
		options := azidentity.WorkloadIdentityCredentialOptions{
			ClientOptions: azcore.ClientOptions{
				Cloud: cloudConfig,
			},
			ClientID:      config.ClientID,
			TenantID:      config.TenantID,
			TokenFilePath: config.FederatedTokenFile,
		}
		var err error
		cred, err = azidentity.NewWorkloadIdentityCredential(&options)
		if err != nil {
			return nil, err
		}
	} else {
		options := azidentity.ClientSecretCredentialOptions{
			ClientOptions: azcore.ClientOptions{
				Cloud: cloudConfig,
			},
		}
		var err error
		cred, err = azidentity.NewClientSecretCredential(config.TenantID, config.ClientID, config.ClientSecret, &options)
		if err != nil {
			return nil, err
		}
	}
	return cred, nil
}

func endpointToScope(endpoint string) string {
	scope := endpoint
	if !strings.HasSuffix(scope, "/.default") {
		scope += "/.default"
	}
	return scope
}

func ParseCloudEnvironment(config Config) cloud.Configuration {
	var cloudConfig cloud.Configuration
	switch config.Environment {
	case azure.ChinaCloud:
		cloudConfig = cloud.AzureChina
	// GermanCloud was closed on Oct 29, 2021
	// https://learn.microsoft.com/en-us/azure/active-directory/develop/authentication-national-cloud
	// case azure.GermanCloud:
	// return nil, nil
	case azure.USGovernmentCloud:
		cloudConfig = cloud.AzureGovernment
	case azure.PublicCloud:
		cloudConfig = cloud.AzurePublic
	default: // AzureStackCloud
		cloudConfig = cloud.Configuration{
			ActiveDirectoryAuthorityHost: config.Environment.ActiveDirectoryEndpoint,
			Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {
					Audience: config.Environment.TokenAudience,
					Endpoint: config.Environment.ResourceManagerEndpoint,
				},
			},
		}
	}
	return cloudConfig
}
