package client

import (
	"errors"
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
	// Managed Identity Override for ARO HCP
	managedIdentityClientID := os.Getenv("ARO_HCP_MI_CLIENT_ID")
	if managedIdentityClientID != "" {
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
			return nil, fmt.Errorf(`failed to read certificate file "%s": %v`, certPath, err)
		}

		certs, key, err := azidentity.ParseCertificates(certData, []byte{})
		if err != nil {
			return nil, fmt.Errorf(`failed to parse certificate data "%s": %v`, certPath, err)
		}

		// Set up a watch on our config file; if it changes, we should exit -
		// (we don't have the ability to dynamically reload config changes).
		stopCh := make(chan struct{})
		err = interrupt.New(func(s os.Signal) {
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

		cred, err = azidentity.NewClientCertificateCredential(tenantID, managedIdentityClientID, certs, key, options)
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
