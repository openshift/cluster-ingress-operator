package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	awsdns "github.com/openshift/cluster-ingress-operator/pkg/dns/aws"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator"
	stub "github.com/openshift/cluster-ingress-operator/pkg/stub"
	"github.com/openshift/cluster-ingress-operator/pkg/util"

	"github.com/operator-framework/operator-sdk/pkg/k8sclient"
	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"
	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"

	configv1 "github.com/openshift/api/config/v1"
	cvoclientset "github.com/openshift/cluster-version-operator/pkg/generated/clientset/versioned"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()

	sdk.ExposeMetricsPort()

	resource := "ingress.openshift.io/v1alpha1"
	kind := "ClusterIngress"
	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		logrus.Fatalf("Failed to get watch namespace: %v", err)
	}

	handler, err := createHandler(namespace)
	if err != nil {
		logrus.Fatalf("couldn't create handler: %v", err)
	}

	if err := handler.EnsureDefaultClusterIngress(); err != nil {
		logrus.Fatalf("failed to ensure default cluster ingress: %v", err)
	}

	resyncPeriod := 10 * time.Minute
	logrus.Infof("Watching %s, %s, %s, %d", resource, kind, namespace, resyncPeriod)
	sdk.Watch(resource, kind, namespace, resyncPeriod)
	// TODO Use a named constant for the router's namespace or get the
	// namespace from config.
	sdk.Watch("apps/v1", "Deployment", "openshift-ingress", resyncPeriod)
	sdk.Watch("v1", "Service", "openshift-ingress", resyncPeriod)
	sdk.Handle(handler)
	sdk.Run(context.TODO())
}

func createHandler(namespace string) (*stub.Handler, error) {
	cvoClient, err := cvoclientset.NewForConfig(k8sclient.GetKubeConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create CVO client: %v", err)
	}

	ic, err := util.GetInstallConfig(k8sclient.GetKubeClient())
	if err != nil {
		return nil, fmt.Errorf("failed to get installconfig: %v", err)
	}

	var dnsManager dns.Manager
	switch {
	case ic.Platform.AWS != nil:
		awsCreds := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "aws-creds",
				Namespace: metav1.NamespaceSystem,
			},
		}
		err := sdk.Get(awsCreds)
		if err != nil {
			return nil, fmt.Errorf("failed to get aws creds from %s/%s: %v", awsCreds.Namespace, awsCreds.Name, err)
		}
		manager, err := awsdns.NewManager(awsdns.Config{
			AccessID:   string(awsCreds.Data["aws_access_key_id"]),
			AccessKey:  string(awsCreds.Data["aws_secret_access_key"]),
			Region:     ic.Platform.AWS.Region,
			BaseDomain: strings.TrimSuffix(ic.BaseDomain, ".") + ".",
			ClusterID:  ic.ClusterID,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create AWS DNS manager: %v", err)
		}
		dnsManager = manager
	default:
		dnsManager = &dns.NoopManager{}
	}

	ingressConfig := &configv1.Ingress{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Ingress",
			APIVersion: "config.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
			// not namespaced
		},
	}
	if err := sdk.Get(ingressConfig); err != nil {
		return nil, fmt.Errorf("failed to get ingressconfig: %v", err)
	}
	if len(ingressConfig.Spec.Domain) == 0 {
		logrus.Warnln("cluster ingress configuration has an empty domain; default ClusterIngress will have empty ingressDomain")
	}

	routerImage := os.Getenv("IMAGE")
	if len(routerImage) == 0 {
		logrus.Fatalf("IMAGE environment variable is required")
	}

	operatorConfig := operator.Config{
		RouterImage:          routerImage,
		DefaultIngressDomain: ingressConfig.Spec.Domain,
	}

	return &stub.Handler{
		InstallConfig:   ic,
		CvoClient:       cvoClient,
		Namespace:       namespace,
		ManifestFactory: manifests.NewFactory(operatorConfig),
		DNSManager:      dnsManager,
	}, nil
}
