package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"

	"github.com/fsnotify/fsnotify"
	"github.com/jongio/azidext/go/azidext"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/util/interrupt"
)

const (
	azureClientCertPath      = "AZURE_CLIENT_CERTIFICATE_PATH"
	azureClientID            = "AZURE_CLIENT_ID"
	azureClientSecret        = "AZURE_CLIENT_SECRET"
	azureClientSendCertChain = "AZURE_CLIENT_SEND_CERTIFICATE_CHAIN"
	azureFederatedTokenFile  = "AZURE_FEDERATED_TOKEN_FILE"
	azureTenantID            = "AZURE_TENANT_ID"
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

	// For each of the following configuration values, if the environment variable is already set, use it over any
	// configuration value: AZURE_TENANT_ID, AZURE_CLIENT_ID, and AZURE_CLIENT_SECRET. Otherwise, set the environment
	// variable to the configuration value. The Azure SDK for Go function, NewDefaultAzureCredential, checks environment
	// variables first.
	if os.Getenv(azureTenantID) == "" {
		if err := os.Setenv(azureTenantID, strings.TrimSpace(config.TenantID)); err != nil {
			return nil, fmt.Errorf("failed to set environment variable %s: %w", azureTenantID, err)
		}
	}
	if os.Getenv(azureClientID) == "" {
		if err := os.Setenv(azureClientID, strings.TrimSpace(config.ClientID)); err != nil {
			return nil, fmt.Errorf("failed to set environment variable %s: %w", azureClientID, err)
		}
	}
	if os.Getenv(azureClientSecret) == "" {
		if err := os.Setenv(azureClientSecret, strings.TrimSpace(config.ClientSecret)); err != nil {
			return nil, fmt.Errorf("failed to set environment variable %s: %w", azureClientSecret, err)
		}
	}
	if os.Getenv(azureFederatedTokenFile) == "" {
		if err := os.Setenv(azureFederatedTokenFile, strings.TrimSpace(config.FederatedTokenFile)); err != nil {
			return nil, fmt.Errorf("failed to set environment variable %s: %w", azureFederatedTokenFile, err)
		}
	}
	if config.AzureWorkloadIdentityEnabled {
		if os.Getenv(azureClientSecret) == "" {
			if os.Getenv(azureFederatedTokenFile) == "" {
				if err := os.Setenv(azureFederatedTokenFile, "/var/run/secrets/openshift/serviceaccount/token"); err != nil {
					return nil, fmt.Errorf("failed to set environment variable %s: %w", azureFederatedTokenFile, err)
				}
			}
		}
	}

	options := &azidentity.DefaultAzureCredentialOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: cloudConfig,
		},
	}

	cred, err := azureCreds(options)
	if err != nil {
		return nil, err
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

// azureCreds uses the Azure SDK for Go's standard default credential chain. It attempts to authenticate with each of
// these credential types, in the following order, stopping when one provides a token:
//   - [EnvironmentCredential]
//   - [WorkloadIdentityCredential], if environment variable configuration is set by the Azure workload
//     identity webhook. Use [WorkloadIdentityCredential] directly when not using the webhook or needing
//     more control over its configuration.
//   - [ManagedIdentityCredential]
//   - [AzureCLICredential]
func azureCreds(options *azidentity.DefaultAzureCredentialOptions) (*azidentity.DefaultAzureCredential, error) {
	if err := os.Setenv(azureClientSendCertChain, "true"); err != nil {
		return nil, fmt.Errorf("could not set %s", azureClientSendCertChain)
	}

	if certPath := os.Getenv(azureClientCertPath); certPath != "" {
		// Set up a watch on our config file; if it changes, we should exit -
		// (we don't have the ability to dynamically reload config changes).
		stopCh := make(chan struct{})
		err := interrupt.New(func(s os.Signal) {
			_, _ = fmt.Fprintf(os.Stderr, "interrupt: Gracefully shutting down ...\n")
			close(stopCh)
		}).Run(func() error {
			if err := watchForChanges(certPath, stopCh); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return azidentity.NewDefaultAzureCredential(options)
}

// watchForChanges closes stopCh if the configuration file changed.
func watchForChanges(configPath string, stopCh chan struct{}) error {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	// Watch all symlinks for changes
	p := configPath
	maxDepth := 100
	for depth := 0; depth < maxDepth; depth++ {
		if err := watcher.Add(p); err != nil {
			return err
		}
		klog.V(2).Infof("Watching config file %s for changes", p)

		stat, err := os.Lstat(p)
		if err != nil {
			return err
		}

		// configmaps are usually symlinks
		if stat.Mode()&os.ModeSymlink > 0 {
			p, err = filepath.EvalSymlinks(p)
			if err != nil {
				return err
			}
		} else {
			break
		}
	}

	go func() {
		for {
			select {
			case <-stopCh:
			case event, ok := <-watcher.Events:
				if !ok {
					klog.Errorf("failed to watch config file %s", p)
					return
				}
				klog.V(2).Infof("Configuration file %s changed, exiting...", event.Name)
				close(stopCh)
				return
			case err, ok := <-watcher.Errors:
				if !ok {
					klog.Errorf("failed to watch config file %s", p)
					return
				}
				klog.Errorf("fsnotify error: %v", err)
			}
		}
	}()
	return nil
}
