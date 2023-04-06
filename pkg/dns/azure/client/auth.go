package client

import (
	"errors"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/jongio/azidext/go/azidext"
)

func getAuthorizerForResource(config Config) (autorest.Authorizer, error) {
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
	if config.AzureWorkloadIdentityEnabled && strings.TrimSpace(config.ClientSecret) == "" {
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

	scope := endpointToScope(config.Environment.TokenAudience)

	// Use an adapter so azidentity in the Azure SDK can be used as
	// Authorizer when calling the Azure Management Packages, which we
	// currently use. Once the Azure SDK clients (found in /sdk) move to
	// stable, we can update our clients and they will be able to use the
	// creds directly without the authorizer. The schedule is here:
	// https://azure.github.io/azure-sdk/releases/latest/index.html#go
	authorizer := azidext.NewTokenCredentialAdapter(cred, []string{scope})

	return authorizer, nil
}

func endpointToScope(endpoint string) string {
	scope := endpoint
	if !strings.HasSuffix(scope, "/.default") {
		scope += "/.default"
	}
	return scope
}
