package operator

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	certcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/certificate"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	kscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/api/errors"
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
	// scheme contains all the API types necessary for the operator's dynamic
	// clients to work. Any new non-core types must be added here.
	//
	// NOTE: The discovery mechanism used by the client won't automatically refresh,
	// so only add types here that are _guaranteed_ to exist before the operator
	// starts.
	scheme *runtime.Scheme
	log    = logf.Logger.WithName("init")
)

func init() {
	// Setup controller-runtime logging
	logf.SetRuntimeLogger(log)

	scheme = kscheme.Scheme
	if err := operatorv1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := configv1.Install(scheme); err != nil {
		panic(err)
	}
}

// Operator is the scaffolding for the ingress operator. It sets up dependencies
// and defines the toplogy of the operator and its managed components, wiring
// them together. Operator knows what namespace the operator lives in, and what
// specific resoure types in other namespaces should produce operator events.
type Operator struct {
	manifestFactory *manifests.Factory
	client          client.Client

	manager manager.Manager
	caches  []cache.Cache
}

// New creates (but does not start) a new operator from configuration.
func New(config operatorconfig.Config, dnsManager dns.Manager, kubeConfig *rest.Config) (*Operator, error) {
	kubeClient, err := Client(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("could't create kube client: %v", err)
	}
	mf := manifests.NewFactory(config)

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
		Client:                 kubeClient,
		Namespace:              config.Namespace,
		ManifestFactory:        mf,
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
	// Any types added to the list here will only queue a clusteringress if the
	// resource has the expected label associating the resource with a
	// clusteringress.
	for _, o := range []runtime.Object{
		&appsv1.Deployment{},
		&corev1.Service{},
	} {
		// TODO: may not be necessary to copy, but erring on the side of caution for
		// now given we're in a loop.
		obj := o.DeepCopyObject()
		informer, err := operandCache.GetInformer(obj)
		if err != nil {
			return nil, fmt.Errorf("failed to create informer for %v: %v", obj, err)
		}
		operatorController.Watch(&source.Informer{Informer: informer}, &handler.EnqueueRequestsFromMapFunc{
			ToRequests: handler.ToRequestsFunc(func(a handler.MapObject) []reconcile.Request {
				labels := a.Meta.GetLabels()
				if ingressName, ok := labels[manifests.OwningClusterIngressLabel]; ok {
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
	}

	// Set up the certificate controller
	if _, err := certcontroller.New(operatorManager, kubeClient, config.Namespace); err != nil {
		return nil, fmt.Errorf("failed to create cacert controller: %v", err)
	}

	return &Operator{
		manager: operatorManager,
		caches:  []cache.Cache{operandCache},

		// TODO: These are only needed for the default cluster ingress stuff, which
		// should be refactored away.
		manifestFactory: mf,
		client:          kubeClient,
	}, nil
}

// Start creates the default ClusterIngress and then starts the operator
// synchronously until a message is received on the stop channel.
// TODO: Move the default ClusterIngress logic elsewhere.
func (o *Operator) Start(stop <-chan struct{}) error {
	// Ensure the default cluster ingress exists.
	if err := o.ensureDefaultClusterIngress(); err != nil {
		return fmt.Errorf("failed to ensure default cluster ingress: %v", err)
	}

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

// ensureDefaultClusterIngress ensures that a default ClusterIngress exists.
func (o *Operator) ensureDefaultClusterIngress() error {
	ci := manifests.DefaultClusterIngress()
	err := o.client.Create(context.TODO(), ci)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if err == nil {
		log.Info("created default clusteringress", "namespace", ci.Namespace, "name", ci.Name)
	}
	return nil
}

// Client builds an operator-compatible kube client from the given REST config.
func Client(kubeConfig *rest.Config) (client.Client, error) {
	managerOptions := manager.Options{
		Scheme:         scheme,
		MapperProvider: apiutil.NewDiscoveryRESTMapper,
	}
	mapper, err := managerOptions.MapperProvider(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get API Group-Resources")
	}
	kubeClient, err := client.New(kubeConfig, client.Options{
		Scheme: scheme,
		Mapper: mapper,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %v", err)
	}
	return kubeClient, nil
}
