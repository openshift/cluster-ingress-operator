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

type StartOptions struct {
	// When this file changes, the operator will shut down. This is useful for simple
	// reloading when things like a certificate changes.
	ShutdownFile string
	// MetricsListenAddr is the address on which to expose the metrics endpoint.
	MetricsListenAddr string
	// OperatorNamespace is the namespace the operator should watch for
	// ingresscontroller resources.
	OperatorNamespace string
	// IngressControllerImage is the pullspec of the ingress controller image to
	// be managed.
	IngressControllerImage string
	// ReleaseVersion is the cluster version which the operator will converge to.
	ReleaseVersion string
}

func NewStartCommand() *cobra.Command {
	var options StartOptions

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the operator",
		Long:  `starts launches the operator in the foreground.`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := start(&options); err != nil {
				log.Error(err, "error starting")
				os.Exit(1)
			}
		},
	}

	cmd.Flags().StringVarP(&options.OperatorNamespace, "namespace", "n", manifests.DefaultOperatorNamespace, "namespace the operator is deployed to (required)")
	cmd.Flags().StringVarP(&options.IngressControllerImage, "image", "i", "", "image of the ingress controller the operator will manage (required)")
	cmd.Flags().StringVarP(&options.ReleaseVersion, "release-version", "", statuscontroller.UnknownVersionValue, "the release version the operator should converge to (required)")
	cmd.Flags().StringVarP(&options.MetricsListenAddr, "metrics-listen-addr", "", ":60000", "metrics endpoint listen address (required)")
	cmd.Flags().StringVarP(&options.ShutdownFile, "shutdown-file", "s", defaultTrustedCABundle, "if provided, shut down the operator when this file changes")

	if err := cmd.MarkFlagRequired("namespace"); err != nil {
		panic(err)
	}
	if err := cmd.MarkFlagRequired("image"); err != nil {
		panic(err)
	}

	return cmd
}

func start(opts *StartOptions) error {
	metrics.DefaultBindAddress = opts.MetricsListenAddr

	// Get a kube client.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kube config: %v", err)
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kube client: %v", err)
	}

	log.Info("using operator namespace", "namespace", opts.OperatorNamespace)

	if opts.ReleaseVersion == statuscontroller.UnknownVersionValue {
		log.Info("Warning: no release version is specified", "release version", statuscontroller.UnknownVersionValue)
	}

	// Retrieve the cluster infrastructure config.
	infraConfig := &configv1.Infrastructure{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig)
	if err != nil {
		return fmt.Errorf("failed to get infrastructure 'config': %v", err)
	}

	dnsConfig := &configv1.DNS{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig)
	if err != nil {
		return fmt.Errorf("failed to get dns 'cluster': %v", err)
	}

	platformStatus, err := getPlatformStatus(kubeClient, infraConfig)
	if err != nil {
		return fmt.Errorf("failed to get platform status: %v", err)
	}

	operatorConfig := operatorconfig.Config{
		OperatorReleaseVersion: opts.ReleaseVersion,
		Namespace:              opts.OperatorNamespace,
		IngressControllerImage: opts.IngressControllerImage,
	}

	// Set up the DNS manager.
	dnsProvider, err := createDNSProvider(kubeClient, operatorConfig, dnsConfig, platformStatus)
	if err != nil {
		return fmt.Errorf("failed to create DNS manager: %v", err)
	}

	// Set up the channels for the watcher and operator.
	stop := make(chan struct{})
	signal := signals.SetupSignalHandler()

	// Set up and start the file watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %v", err)
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			log.V(1).Info("warning: watcher close returned an error: %v", err)
		}
	}()

	var orig []byte
	if len(opts.ShutdownFile) > 0 {
		if err := watcher.Add(opts.ShutdownFile); err != nil {
			return fmt.Errorf("failed to add file %q to watcher: %v", opts.ShutdownFile, err)
		}
		log.Info("watching file", "filename", opts.ShutdownFile)
		orig, err = ioutil.ReadFile(opts.ShutdownFile)
		if err != nil {
			return fmt.Errorf("failed to read watcher file %q: %v", opts.ShutdownFile, err)
		}
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
				latest, err := ioutil.ReadFile(opts.ShutdownFile)
				if err != nil {
					log.Error(err, "failed to read watched file", "filename", opts.ShutdownFile)
					close(stop)
					return
				}
				if !bytes.Equal(orig, latest) {
					log.Info("watched file changed, stopping operator", "filename", opts.ShutdownFile)
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
