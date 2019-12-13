package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/ghodss/yaml"
	"github.com/spf13/cobra"
	"gopkg.in/fsnotify.v1"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	awsdns "github.com/openshift/cluster-ingress-operator/pkg/dns/aws"
	azuredns "github.com/openshift/cluster-ingress-operator/pkg/dns/azure"
	gcpdns "github.com/openshift/cluster-ingress-operator/pkg/dns/gcp"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	statuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/status"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

const (
	// cloudCredentialsSecretName is the name of the secret in the
	// operator's namespace that will hold the credentials that the operator
	// will use to authenticate with the cloud API.
	cloudCredentialsSecretName = "cloud-credentials"
	// defaultTrustedCABundle is the fully qualified path of the trusted CA bundle
	// that is mounted from configmap openshift-ingress-operator/trusted-ca.
	defaultTrustedCABundle = "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
)

func NewStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the operator",
		Long:  `starts launches the operator in the foreground.`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := start(); err != nil {
				log.Error(err, "error starting")
				os.Exit(1)
			}
		},
	}
}

func start() error {
	metrics.DefaultBindAddress = ":60000"

	// Get a kube client.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		log.Error(err, "failed to get kube config")
		os.Exit(1)
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		log.Error(err, "failed to create kube client")
		os.Exit(1)
	}

	// Collect operator configuration.
	operatorNamespace := os.Getenv("WATCH_NAMESPACE")
	if len(operatorNamespace) == 0 {
		operatorNamespace = manifests.DefaultOperatorNamespace
	}
	log.Info("using operator namespace", "namespace", operatorNamespace)

	ingressControllerImage := os.Getenv("IMAGE")
	if len(ingressControllerImage) == 0 {
		log.Error(fmt.Errorf("missing environment variable"), "'IMAGE' environment variable must be set")
		os.Exit(1)
	}
	releaseVersion := os.Getenv("RELEASE_VERSION")
	if len(releaseVersion) == 0 {
		releaseVersion = statuscontroller.UnknownVersionValue
		log.Info("RELEASE_VERSION environment variable missing", "release version", statuscontroller.UnknownVersionValue)
	}

	// Retrieve the cluster infrastructure config.
	infraConfig := &configv1.Infrastructure{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig)
	if err != nil {
		log.Error(err, "failed to get infrastructure 'config'")
		os.Exit(1)
	}

	dnsConfig := &configv1.DNS{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig)
	if err != nil {
		log.Error(err, "failed to get dns 'cluster'")
		os.Exit(1)
	}

	platformStatus, err := getPlatformStatus(kubeClient, infraConfig)
	if err != nil {
		log.Error(err, "failed to get platform status")
		os.Exit(1)
	}

	operatorConfig := operatorconfig.Config{
		OperatorReleaseVersion: releaseVersion,
		Namespace:              operatorNamespace,
		IngressControllerImage: ingressControllerImage,
	}

	// Set up the DNS manager.
	dnsProvider, err := createDNSProvider(kubeClient, operatorConfig, dnsConfig, platformStatus)
	if err != nil {
		log.Error(err, "failed to create DNS manager")
		os.Exit(1)
	}

	// Set up the channels for the watcher and operator.
	stop := make(chan struct{})
	signal := signals.SetupSignalHandler()

	// Set up and start the file watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error(err, "failed to create watcher")
		os.Exit(1)
	}
	defer watcher.Close()
	if err := watcher.Add(defaultTrustedCABundle); err != nil {
		log.Error(err, "failed to add file to watcher", "filename", defaultTrustedCABundle)
		os.Exit(1)
	}
	log.Info("watching file", "filename", defaultTrustedCABundle)
	orig, err := ioutil.ReadFile(defaultTrustedCABundle)
	if err != nil {
		log.Error(err, "failed to read watched file", "filename", defaultTrustedCABundle)
		os.Exit(1)
	}
	go func() {
		for {
			select {
			case <-signal:
				close(stop)
				return
			case _, ok := <-watcher.Events:
				if !ok {
					log.Info("file watch events channel closed")
					close(stop)
					return
				}
				latest, err := ioutil.ReadFile(defaultTrustedCABundle)
				if err != nil {
					log.Error(err, "failed to read watched file", "filename", defaultTrustedCABundle)
					close(stop)
					return
				}
				if !bytes.Equal(orig, latest) {
					log.Info("watched file changed, stopping operator", "filename", defaultTrustedCABundle)
					close(stop)
					return
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					log.Info("file watch error channel closed")
					close(stop)
					return
				}
				log.Error(err, "file watch error")
			}
		}
	}()

	// Set up and start the operator.
	op, err := operator.New(operatorConfig, dnsProvider, kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create operator: %v", err)
	}
	return op.Start(stop)
}

// createDNSManager creates a DNS manager compatible with the given cluster
// configuration.
func createDNSProvider(cl client.Client, operatorConfig operatorconfig.Config, dnsConfig *configv1.DNS, platformStatus *configv1.PlatformStatus) (dns.Provider, error) {
	var dnsProvider dns.Provider
	userAgent := fmt.Sprintf("OpenShift/%s (ingress-operator)", operatorConfig.OperatorReleaseVersion)

	switch platformStatus.Type {
	case configv1.AWSPlatformType:
		creds := &corev1.Secret{}
		err := cl.Get(context.TODO(), types.NamespacedName{Namespace: operatorConfig.Namespace, Name: cloudCredentialsSecretName}, creds)
		if err != nil {
			return nil, fmt.Errorf("failed to get cloud credentials from secret %s/%s: %v", creds.Namespace, creds.Name, err)
		}
		provider, err := awsdns.NewProvider(awsdns.Config{
			AccessID:  string(creds.Data["aws_access_key_id"]),
			AccessKey: string(creds.Data["aws_secret_access_key"]),
			DNS:       dnsConfig,
			Region:    platformStatus.AWS.Region,
		}, operatorConfig.OperatorReleaseVersion)
		if err != nil {
			return nil, fmt.Errorf("failed to create AWS DNS manager: %v", err)
		}
		dnsProvider = provider
	case configv1.AzurePlatformType:
		creds := &corev1.Secret{}
		err := cl.Get(context.TODO(), types.NamespacedName{Namespace: operatorConfig.Namespace, Name: cloudCredentialsSecretName}, creds)
		if err != nil {
			return nil, fmt.Errorf("failed to get cloud credentials from secret %s/%s: %v", creds.Namespace, creds.Name, err)
		}
		provider, err := azuredns.NewProvider(azuredns.Config{
			Environment:    "AzurePublicCloud",
			ClientID:       string(creds.Data["azure_client_id"]),
			ClientSecret:   string(creds.Data["azure_client_secret"]),
			TenantID:       string(creds.Data["azure_tenant_id"]),
			SubscriptionID: string(creds.Data["azure_subscription_id"]),
			DNS:            dnsConfig,
		}, operatorConfig.OperatorReleaseVersion)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure DNS manager: %v", err)
		}
		dnsProvider = provider
	case configv1.GCPPlatformType:
		creds := &corev1.Secret{}
		err := cl.Get(context.TODO(), types.NamespacedName{Namespace: operatorConfig.Namespace, Name: cloudCredentialsSecretName}, creds)
		if err != nil {
			return nil, fmt.Errorf("failed to get cloud credentials from secret %s/%s: %v", creds.Namespace, creds.Name, err)
		}
		provider, err := gcpdns.New(gcpdns.Config{
			Project:         platformStatus.GCP.ProjectID,
			CredentialsJSON: creds.Data["service_account.json"],
			UserAgent:       userAgent,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create GCP DNS provider: %v", err)
		}
		dnsProvider = provider
	default:
		dnsProvider = &dns.FakeProvider{}
	}
	return dnsProvider, nil
}

// getPlatformStatus provides a backwards-compatible way to look up platform status. AWS is the
// special case. 4.1 clusters on AWS expose the region config only through install-config. New AWS clusters
// and all other 4.2+ platforms are configured via platform status.
func getPlatformStatus(client client.Client, infra *configv1.Infrastructure) (*configv1.PlatformStatus, error) {
	if status := infra.Status.PlatformStatus; status != nil {
		// Only AWS needs backwards compatibility with install-config
		if status.Type != configv1.AWSPlatformType {
			return status, nil
		}

		// Check whether the cluster config is already migrated
		if status.AWS != nil && len(status.AWS.Region) > 0 {
			return status, nil
		}
	}

	// Otherwise build a platform status from the deprecated install-config
	type installConfig struct {
		Platform struct {
			AWS struct {
				Region string `json:"region"`
			} `json:"aws"`
		} `json:"platform"`
	}
	clusterConfigName := types.NamespacedName{Namespace: "kube-system", Name: "cluster-config-v1"}
	clusterConfig := &corev1.ConfigMap{}
	if err := client.Get(context.TODO(), clusterConfigName, clusterConfig); err != nil {
		return nil, fmt.Errorf("failed to get configmap %s: %v", clusterConfigName, err)
	}
	data, ok := clusterConfig.Data["install-config"]
	if !ok {
		return nil, fmt.Errorf("missing install-config in configmap")
	}
	var ic installConfig
	if err := yaml.Unmarshal([]byte(data), &ic); err != nil {
		return nil, fmt.Errorf("invalid install-config: %v\njson:\n%s", err, data)
	}
	return &configv1.PlatformStatus{
		Type: infra.Status.Platform,
		AWS: &configv1.AWSPlatformStatus{
			Region: ic.Platform.AWS.Region,
		},
	}, nil
}
