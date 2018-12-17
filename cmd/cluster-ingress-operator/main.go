package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	awsdns "github.com/openshift/cluster-ingress-operator/pkg/dns/aws"
	"github.com/openshift/cluster-ingress-operator/pkg/operator"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	"github.com/openshift/cluster-ingress-operator/pkg/util"

	"github.com/operator-framework/operator-sdk/pkg/k8sutil"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
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
	operatorNamespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		logrus.Fatalf("Failed to get watch namespace: %v", err)
	}
	routerImage := os.Getenv("IMAGE")
	if len(routerImage) == 0 {
		logrus.Fatalf("IMAGE environment variable is required")
	}

	// Retrieve the untyped cluster config (AKA the install config).
	// TODO: Use of this should be completely replaced by config API types.
	cm := &corev1.ConfigMap{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Namespace: util.ClusterConfigNamespace, Name: util.ClusterConfigName}, cm)
	if err != nil {
		logrus.Fatalf("failed to get clusterconfig: %v", err)
	}
	installConfig, err := util.UnmarshalInstallConfig(cm)
	if err != nil {
		logrus.Fatalf("failed to read clusterconfig: %v", err)
	}

	// Retrieve the typed cluster config.
	ingressConfig := &configv1.Ingress{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, ingressConfig)
	if err != nil {
		logrus.Fatalf("failed to get ingress 'cluster': %v", err)
	}
	if len(ingressConfig.Spec.Domain) == 0 {
		logrus.Warnln("cluster ingress configuration has an empty domain; default ClusterIngress will have empty ingressDomain")
	}

	// Retrieve the typed cluster version config.
	clusterVersionConfig := &configv1.ClusterVersion{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "version"}, clusterVersionConfig)
	if err != nil {
		logrus.Fatalf("failed to get clusterversion 'version': %v", err)
	}

	// Set up the DNS manager.
	dnsManager, err := createDNSManager(kubeClient, installConfig, ingressConfig, clusterVersionConfig)
	if err != nil {
		logrus.Fatalf("failed to create DNS manager: %v", err)
	}

	// Set up and start the operator.
	operatorConfig := operatorconfig.Config{
		Namespace:            operatorNamespace,
		RouterImage:          routerImage,
		DefaultIngressDomain: ingressConfig.Spec.Domain,
	}
	op, err := operator.New(operatorConfig, installConfig, dnsManager, kubeConfig)
	if err != nil {
		logrus.Fatalf("failed to create operator: %v", err)
	}
	if err := op.Start(signals.SetupSignalHandler()); err != nil {
		logrus.Fatal(err)
	}
}

// createDNSManager creates a DNS manager compatible with the given cluster
// configuration.
func createDNSManager(cl client.Client, ic *util.InstallConfig, ingressConfig *configv1.Ingress, clusterVersionConfig *configv1.ClusterVersion) (dns.Manager, error) {
	var dnsManager dns.Manager
	switch {
	case ic.Platform.AWS != nil:
		awsCreds := &corev1.Secret{}
		err := cl.Get(context.TODO(), types.NamespacedName{Namespace: metav1.NamespaceSystem, Name: "aws-creds"}, awsCreds)
		if err != nil {
			return nil, fmt.Errorf("failed to get aws creds from %s/%s: %v", awsCreds.Namespace, awsCreds.Name, err)
		}
		manager, err := awsdns.NewManager(awsdns.Config{
			AccessID:   string(awsCreds.Data["aws_access_key_id"]),
			AccessKey:  string(awsCreds.Data["aws_secret_access_key"]),
			Region:     ic.Platform.AWS.Region,
			BaseDomain: strings.TrimSuffix(ic.BaseDomain, ".") + ".",
			ClusterID:  string(clusterVersionConfig.Spec.ClusterID),
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
