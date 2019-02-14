package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	awsdns "github.com/openshift/cluster-ingress-operator/pkg/dns/aws"
	"github.com/openshift/cluster-ingress-operator/pkg/operator"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

const (
	// cloudCredentialsSecretName is the name of the secret in the
	// operator's namespace that will hold the credentials that the operator
	// will use to authenticate with the cloud API.
	cloudCredentialsSecretName = "cloud-credentials"
)

func main() {
	// Get a kube client.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		logrus.Fatalf("failed to get kube config: %v", err)
	}
	kubeClient, err := operator.Client(kubeConfig)
	if err != nil {
		logrus.Fatalf("failed to create kube client: %v", err)
	}

	// Collect operator configuration.
	operatorNamespace, ok := os.LookupEnv("WATCH_NAMESPACE")
	if !ok {
		logrus.Fatalf("WATCH_NAMESPACE environment variable is required")
	}
	routerImage := os.Getenv("IMAGE")
	if len(routerImage) == 0 {
		logrus.Fatalf("IMAGE environment variable is required")
	}

	// Retrieve the cluster infrastructure config.
	infraConfig := &configv1.Infrastructure{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig)
	if err != nil {
		logrus.Fatalf("failed to get infrastructure 'config': %v", err)
	}

	// Retrieve the cluster ingress config.
	ingressConfig := &configv1.Ingress{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, ingressConfig)
	if err != nil {
		logrus.Fatalf("failed to get ingress 'cluster': %v", err)
	}
	if len(ingressConfig.Spec.Domain) == 0 {
		logrus.Warnln("cluster ingress configuration has an empty domain; default ClusterIngress will have empty ingressDomain")
	}

	dnsConfig := &configv1.DNS{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig)
	if err != nil {
		logrus.Fatalf("failed to get dns 'cluster': %v", err)
	}

	// Set up the DNS manager.
	dnsManager, err := createDNSManager(kubeClient, operatorNamespace, infraConfig, dnsConfig)
	if err != nil {
		logrus.Fatalf("failed to create DNS manager: %v", err)
	}

	// Set up and start the operator.
	operatorConfig := operatorconfig.Config{
		Namespace:            operatorNamespace,
		RouterImage:          routerImage,
		DefaultIngressDomain: ingressConfig.Spec.Domain,
		Platform:             infraConfig.Status.Platform,
	}
	op, err := operator.New(operatorConfig, dnsManager, kubeConfig)
	if err != nil {
		logrus.Fatalf("failed to create operator: %v", err)
	}
	if err := op.Start(signals.SetupSignalHandler()); err != nil {
		logrus.Fatal(err)
	}
}

// createDNSManager creates a DNS manager compatible with the given cluster
// configuration.
func createDNSManager(cl client.Client, namespace string, infraConfig *configv1.Infrastructure, dnsConfig *configv1.DNS) (dns.Manager, error) {
	var dnsManager dns.Manager
	switch infraConfig.Status.Platform {
	case configv1.AWSPlatform:
		awsCreds := &corev1.Secret{}
		err := cl.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: cloudCredentialsSecretName}, awsCreds)
		if err != nil {
			return nil, fmt.Errorf("failed to get aws creds from %s/%s: %v", awsCreds.Namespace, awsCreds.Name, err)
		}
		manager, err := awsdns.NewManager(awsdns.Config{
			AccessID:  string(awsCreds.Data["aws_access_key_id"]),
			AccessKey: string(awsCreds.Data["aws_secret_access_key"]),
			DNS:       dnsConfig,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create AWS DNS manager: %v", err)
		}
		dnsManager = manager
	default:
		dnsManager = &dns.NoopManager{}
	}
	return dnsManager, nil
}
