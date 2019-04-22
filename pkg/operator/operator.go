package operator

import (
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	certcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/certificate"
	certpublishercontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/certificate-publisher"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var (
	log = logf.Logger.WithName("init")
)

const (
	// DefaultIngressController is the name of the default IngressController
	// instance.
	DefaultIngressController = "default"
)

func init() {
	// Setup controller-runtime logging
	logf.SetRuntimeLogger(log)
}

// Operator is the scaffolding for the ingress operator. It sets up dependencies
// and defines the topology of the operator and its managed components, wiring
// them together. Operator knows what namespace the operator lives in, and what
// specific resoure types in other namespaces should produce operator events.
type Operator struct {
	manifestFactory *manifests.Factory
	client          client.Client

	manager manager.Manager
	caches  []cache.Cache

	namespace string
}

// New creates (but does not start) a new operator from configuration.
func New(config operatorconfig.Config, dnsManager dns.Manager, kubeConfig *rest.Config) (*Operator, error) {
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %v", err)
	}

	scheme := operatorclient.GetScheme()
	// Set up an operator manager for the operator namespace.
	operatorManager, err := manager.New(kubeConfig, manager.Options{
		Namespace: config.Namespace,
		Scheme:    scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create operator manager: %v", err)
	}

	// Create and register the operator controller with the operator manager.
	operatorController, err := operatorcontroller.New(operatorManager, operatorcontroller.Config{
		KubeConfig:             kubeConfig,
		Namespace:              config.Namespace,
		ManifestFactory:        &manifests.Factory{},
		DNSManager:             dnsManager,
		RouterImage:            config.RouterImage,
		OperatorReleaseVersion: config.OperatorReleaseVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create operator controller: %v", err)
	}

	// Create additional controller event sources from informers in the managed
	// namespace. Any new managed resources outside the operator's namespace
	// should be added here.
	mapper, err := apiutil.NewDiscoveryRESTMapper(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get API Group-Resources")
	}
	operandCache, err := cache.New(kubeConfig, cache.Options{Namespace: "openshift-ingress", Scheme: scheme, Mapper: mapper})
	if err != nil {
		return nil, fmt.Errorf("failed to create openshift-ingress cache: %v", err)
	}
	// Any types added to the list here will only queue a ingresscontroller if the
	// resource has the expected label associating the resource with a
	// ingresscontroller.
	for _, o := range []runtime.Object{
		&appsv1.Deployment{},
		&corev1.Service{},
	} {
		// TODO: may not be necessary to copy, but erring on the side of caution for
		// now given we're in a loop.
		obj := o.DeepCopyObject()
		informer, err := operandCache.GetInformer(obj)
		if err != nil {
			return nil, fmt.Errorf("failed to get informer for %v: %v", obj, err)
		}
		err = operatorController.Watch(&source.Informer{Informer: informer}, &handler.EnqueueRequestsFromMapFunc{
			ToRequests: handler.ToRequestsFunc(func(a handler.MapObject) []reconcile.Request {
				labels := a.Meta.GetLabels()
				if ingressName, ok := labels[manifests.OwningIngressControllerLabel]; ok {
					log.Info("queueing ingress", "name", ingressName, "related", a.Meta.GetSelfLink())
					return []reconcile.Request{
						{
							NamespacedName: types.NamespacedName{
								Namespace: config.Namespace,
								Name:      ingressName,
							},
						},
					}
				} else {
					return []reconcile.Request{}
				}
			}),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create watch for %v: %v", obj, err)
		}
	}

	// Set up the certificate controller
	if _, err := certcontroller.New(operatorManager, kubeClient, config.Namespace); err != nil {
		return nil, fmt.Errorf("failed to create cacert controller: %v", err)
	}

	// Set up the certificate-publisher controller
	if _, err := certpublishercontroller.New(operatorManager, operandCache, kubeClient, config.Namespace, "openshift-ingress"); err != nil {
		return nil, fmt.Errorf("failed to create certificate-publisher controller: %v", err)
	}

	return &Operator{
		manager: operatorManager,
		caches:  []cache.Cache{operandCache},

		// TODO: These are only needed for the default ingress controller stuff, which
		// should be refactored away.
		manifestFactory: &manifests.Factory{},
		client:          kubeClient,
		namespace:       config.Namespace,
	}, nil
}

// Start creates the default IngressController and then starts the operator
// synchronously until a message is received on the stop channel.
// TODO: Move the default IngressController logic elsewhere.
func (o *Operator) Start(stop <-chan struct{}) error {
	errChan := make(chan error)

	// Start secondary caches.
	for _, cache := range o.caches {
		go func() {
			if err := cache.Start(stop); err != nil {
				errChan <- err
			}
		}()
		log.Info("waiting for cache to sync")
		if !cache.WaitForCacheSync(stop) {
			return fmt.Errorf("failed to sync cache")
		}
		log.Info("cache synced")
	}

	// Secondary caches are all synced, so start the manager.
	go func() {
		errChan <- o.manager.Start(stop)
	}()

	// Wait for the manager to exit or a secondary cache to fail.
	select {
	case <-stop:
		return nil
	case err := <-errChan:
		return err
	}
}
