package main

import (
	"context"
	"runtime"
	"time"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	stub "github.com/openshift/cluster-ingress-operator/pkg/stub"
	"github.com/openshift/cluster-ingress-operator/pkg/util"

	"github.com/operator-framework/operator-sdk/pkg/k8sclient"
	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"
	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"

	cvoclientset "github.com/openshift/cluster-version-operator/pkg/generated/clientset/versioned"

	"github.com/sirupsen/logrus"

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
	kubeClient := k8sclient.GetKubeClient()

	ic, err := util.GetInstallConfig(kubeClient)
	if err != nil {
		logrus.Fatalf("could't get installconfig: %v", err)
	}

	cvoClient, err := cvoclientset.NewForConfig(k8sclient.GetKubeConfig())
	if err != nil {
		logrus.Fatalf("Failed to get cvoClient: %v", err)
	}

	handler := &stub.Handler{
		CvoClient:       cvoClient,
		InstallConfig:   ic,
		Namespace:       namespace,
		ManifestFactory: manifests.NewFactory(),
	}

	if err := handler.EnsureDefaultClusterIngress(); err != nil {
		logrus.Fatalf("failed to ensure default cluster ingress: %v", err)
	}
	resyncPeriod := 10 * time.Minute
	logrus.Infof("Watching %s, %s, %s, %d", resource, kind, namespace, resyncPeriod)
	sdk.Watch(resource, kind, namespace, resyncPeriod)
	// TODO Use a named constant for the router's namespace or get the
	// namespace from config.
	sdk.Watch("apps/v1", "DaemonSet", "openshift-ingress", resyncPeriod)
	sdk.Handle(handler)
	sdk.Run(context.TODO())
}
