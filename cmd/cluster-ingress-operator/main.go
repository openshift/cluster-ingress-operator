package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/openshift/cluster-ingress-operator/pkg/apis"
	clusteringresscontroller "github.com/openshift/cluster-ingress-operator/pkg/controller/clusteringress"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	awsdns "github.com/openshift/cluster-ingress-operator/pkg/dns/aws"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator"
	"github.com/openshift/cluster-ingress-operator/pkg/util"

	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/sirupsen/logrus"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/util/workqueue"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	printVersion()

	// Set up dependencies required for the operator to do work.

	operatorNamespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		logrus.Fatalf("Failed to get watch namespace: %v", err)
	}
	kubeConfig, err := config.GetConfig()
	if err != nil {
		logrus.Fatalf("failed to get a kube config: %v", err)
	}
	routerImage := os.Getenv("IMAGE")
	if len(routerImage) == 0 {
		logrus.Fatalf("IMAGE environment variable is required")
	}

	// Set up a controller-runtime manager for the operator.
	mgr, err := manager.New(kubeConfig, manager.Options{Namespace: operatorNamespace})
	if err != nil {
		logrus.Fatalf("failed to create operator manager: %v", err)
	}
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		logrus.Fatalf("failed to set up scheme: %v", err)
	}
	if err := configv1.Install(mgr.GetScheme()); err != nil {
		logrus.Fatalf("failed to set up scheme: %v", err)
	}

	// Set up a controller-runtime manager for the managed component which will
	// own controllers that propagate events into the operator manager's
	// controllers.
	componentManager, err := manager.New(kubeConfig, manager.Options{Namespace: "openshift-ingress"})
	if err := apis.AddToScheme(componentManager.GetScheme()); err != nil {
		logrus.Fatalf("failed to set up scheme: %v", err)
	}
	if err := configv1.Install(componentManager.GetScheme()); err != nil {
		logrus.Fatalf("failed to set up scheme: %v", err)
	}

	// TODO: Huge hack; the manager unconditionally uses a client with overly
	// rigid semantics so either we set up a client from scratch here or cheat and
	// reach in to get the live client the manager already made.
	// https://github.com/kubernetes-sigs/controller-runtime/issues/180
	kubeClient := mgr.GetClient().(client.DelegatingClient).Reader.(*client.DelegatingReader).ClientReader.(client.Client)

	// Retrieve the untyped cluster config (AKA the install config).
	// TODO: Use of this should be completely replaced by config API types.
	cm := &corev1.ConfigMap{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Namespace: util.ClusterConfigNamespace, Name: util.ClusterConfigName}, cm)
	if err != nil {
		logrus.Fatalf("couldn't get clusterconfig: %v", err)
	}
	installConfig, err := util.UnmarshalInstallConfig(cm)
	if err != nil {
		logrus.Fatalf("failed to read clusterconfig: %v", err)
	}

	// Retrieve the typed cluster config.
	ingressConfig := &configv1.Ingress{}
	err = kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, ingressConfig)
	if err != nil {
		logrus.Fatalf("failed to get ingressconfig 'cluster': %v", err)
	}
	if len(ingressConfig.Spec.Domain) == 0 {
		logrus.Warnln("cluster ingress configuration has an empty domain; default ClusterIngress will have empty ingressDomain")
	}

	// Set up the manifest factory.
	operatorConfig := operator.Config{
		RouterImage:          routerImage,
		DefaultIngressDomain: ingressConfig.Spec.Domain,
	}
	mf := manifests.NewFactory(operatorConfig)

	// Set up the DNS manager.
	dnsManager, err := createDNSManager(kubeClient, installConfig, ingressConfig)
	if err != nil {
		logrus.Fatalf("failed to create DNS manager: %v", err)
	}

	// Ensure the default cluster ingress exists.
	// TODO: Should this be handled in a reconciler?
	if err := ensureDefaultClusterIngress(mf, installConfig, kubeClient); err != nil {
		logrus.Fatalf("failed to ensure default cluster ingress: %v", err)
	}

	// Set up and start controllers.

	// Set up a shared event source to enable event forwarding from the component
	// controller to the operator cobtroller.
	stop := signals.SetupSignalHandler()

	componentEvents := make(chan event.GenericEvent)
	channelSource := &source.Channel{Source: componentEvents}
	channelSource.InjectStopChannel(stop)

	// Create and register the operator controller with the operator manager.
	reconciler := &clusteringresscontroller.Reconciler{
		Client:          kubeClient,
		InstallConfig:   installConfig,
		Namespace:       operatorNamespace,
		ManifestFactory: mf,
		DNSManager:      dnsManager,
	}
	operatorController, err := clusteringresscontroller.New(mgr, reconciler)
	if err != nil {
		logrus.Fatalf("failed to create operator controller: %v", err)
	}
	if err := operatorController.Watch(channelSource, &handler.EnqueueRequestForObject{}); err != nil {
		logrus.Fatalf("failed to create watch for operator controller: %v", err)
	}

	// Create and register the component controller with the component manager.
	feh := &ForwardingEventHandler{Events: componentEvents}
	componentController, err := controller.New("ingress-component-controller", componentManager, controller.Options{
		Reconciler: reconcile.Func(func(reconcile.Request) (reconcile.Result, error) { return reconcile.Result{}, nil }),
	})
	if err != nil {
		logrus.Fatalf("failed to create component controller: %v", err)
	}
	if err := componentController.Watch(&source.Kind{Type: &appsv1.Deployment{}}, feh); err != nil {
		logrus.Fatalf("failed to create watch for component controller: %v", err)
	}
	if err := componentController.Watch(&source.Kind{Type: &corev1.Service{}}, feh); err != nil {
		logrus.Fatalf("failed to create watch for component controller: %v", err)
	}

	// Start all the managers/controllers.
	go func() {
		if err := componentManager.Start(stop); err != nil {
			logrus.Fatalf("component manager returned an error: %v", err)
		}
	}()
	if err := mgr.Start(stop); err != nil {
		logrus.Fatalf("operator manager returned an error: %v", err)
	}
}

func createDNSManager(cl client.Client, ic *util.InstallConfig, ingressConfig *configv1.Ingress) (dns.Manager, error) {
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
			ClusterID:  ic.ClusterID,
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

// EnsureDefaultClusterIngress ensures that a default ClusterIngress exists.
// TODO: Should this be another controller?
func ensureDefaultClusterIngress(mf *manifests.Factory, ic *util.InstallConfig, cl client.Client) error {
	ci, err := mf.DefaultClusterIngress()
	if err != nil {
		return err
	}
	err = cl.Create(context.TODO(), ci)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if err == nil {
		logrus.Infof("created default clusteringress %s/%s", ci.Namespace, ci.Name)
	}
	return nil
}

type ForwardingEventHandler struct {
	Events chan event.GenericEvent
}

func (h *ForwardingEventHandler) Create(e event.CreateEvent, _ workqueue.RateLimitingInterface) {
	h.Events <- event.GenericEvent{Meta: e.Meta, Object: e.Object}
}
func (h *ForwardingEventHandler) Update(e event.UpdateEvent, _ workqueue.RateLimitingInterface) {
	h.Events <- event.GenericEvent{Meta: e.MetaNew, Object: e.ObjectNew}
}
func (h *ForwardingEventHandler) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	h.Events <- event.GenericEvent{Meta: e.Meta, Object: e.Object}
}
func (h *ForwardingEventHandler) Generic(e event.GenericEvent, _ workqueue.RateLimitingInterface) {
	h.Events <- e
}
