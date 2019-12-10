package operator

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	certcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/certificate"
	certpublishercontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/certificate-publisher"
	dnscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/dns"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	statuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/status"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/watcher"
	operatorutil "github.com/openshift/cluster-ingress-operator/pkg/util"

	operatorv1 "github.com/openshift/api/operator/v1"

	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var (
	log = logf.Logger.WithName("init")
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
	client client.Client

	manager manager.Manager

	caWatcher *watcher.FileWatcher

	namespace string
}

// New creates (but does not start) a new operator from configuration.
func New(config operatorconfig.Config, dnsProvider dns.Provider, kubeConfig *rest.Config) (*Operator, error) {
	scheme := operatorclient.GetScheme()
	// Set up an operator manager for the operator namespace.
	mgr, err := manager.New(kubeConfig, manager.Options{
		Namespace:      config.Namespace,
		Scheme:         scheme,
		MapperProvider: operatorutil.NewDynamicRESTMapper,
		NewCache: cache.MultiNamespacedCacheBuilder([]string{
			config.Namespace,
			manifests.DefaultOperandNamespace,
			operatorcontroller.GlobalMachineSpecifiedConfigNamespace,
		}),
		// Use a non-caching client everywhere. The default split client does not
		// promise to invalidate the cache during writes (nor does it promise
		// sequential create/get coherence), and we have code which (probably
		// incorrectly) assumes a get immediately following a create/update will
		// return the updated resource. All client consumers will need audited to
		// ensure they are tolerant of stale data (or we need a cache or client that
		// makes stronger coherence guarantees).
		NewClient: func(_ cache.Cache, config *rest.Config, options client.Options) (client.Client, error) {
			return client.New(config, options)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create operator manager: %v", err)
	}

	// Create and register the ingress controller with the operator manager.
	if _, err := ingresscontroller.New(mgr, ingresscontroller.Config{
		Namespace:              config.Namespace,
		IngressControllerImage: config.IngressControllerImage,
	}); err != nil {
		return nil, fmt.Errorf("failed to create ingress controller: %v", err)
	}

	// Set up the status controller.
	if _, err := statuscontroller.New(mgr, statuscontroller.Config{
		Namespace:              config.Namespace,
		IngressControllerImage: config.IngressControllerImage,
		OperatorReleaseVersion: config.OperatorReleaseVersion,
	}); err != nil {
		return nil, fmt.Errorf("failed to create status controller: %v", err)
	}

	// Set up the certificate controller
	if _, err := certcontroller.New(mgr, config.Namespace); err != nil {
		return nil, fmt.Errorf("failed to create cacert controller: %v", err)
	}

	// Set up the certificate-publisher controller
	if _, err := certpublishercontroller.New(mgr, config.Namespace, "openshift-ingress"); err != nil {
		return nil, fmt.Errorf("failed to create certificate-publisher controller: %v", err)
	}

	// Set up the DNS controller
	if _, err := dnscontroller.New(mgr, dnsProvider); err != nil {
		return nil, fmt.Errorf("failed to create dns controller: %v", err)
	}

	// Set up trusted ca bundle watcher
	watcher, err := watcher.New(config.TrustedCABundle)
	if err != nil {
		return nil, fmt.Errorf("failed to create trusted ca watcher: %v", err)
	}

	return &Operator{
		manager:   mgr,
		caWatcher: watcher,
		// TODO: These are only needed for the default ingress controller stuff, which
		// should be refactored away.
		client:    mgr.GetClient(),
		namespace: config.Namespace,
	}, nil
}

// Start creates the default IngressController and then starts the operator
// and trusted ca bundle watcher synchronously until a message is received
// on the stop or reload channels.
// TODO: Move the default IngressController logic elsewhere.
func (o *Operator) Start(stop <-chan struct{}) error {
	// Periodicaly ensure the default controller exists.
	go wait.Until(func() {
		if !o.manager.GetCache().WaitForCacheSync(stop) {
			log.Error(nil, "failed to sync cache before ensuring default ingresscontroller")
			return
		}
		err := o.ensureDefaultIngressController()
		if err != nil {
			log.Error(err, "failed to ensure default ingresscontroller")
		}
	}, 1*time.Minute, stop)

	errChan := make(chan error)
	go func() {
		errChan <- o.manager.Start(stop)
	}()

	reloadChan := make(chan struct{})
	go func() {
		errChan <- o.caWatcher.Start(stop, reloadChan)
	}()

	// Wait for the manager or watcher to exit or an explicit stop.
	select {
	case <-stop:
		return nil
	case <-reloadChan:
		return nil
	case err := <-errChan:
		return err
	}
}

// ensureDefaultIngressController creates the default ingresscontroller if it
// doesn't already exist.
func (o *Operator) ensureDefaultIngressController() error {
	// Set the replicas field to a non-nil value because otherwise its
	// persisted value will be nil, which causes GETs on the /scale
	// subresource to fail, which breaks the scaling client.  See also:
	// https://github.com/kubernetes/kubernetes/pull/75210
	//
	// TODO: Set the replicas value to the number of workers.
	two := int32(2)
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      manifests.DefaultIngressControllerName,
			Namespace: o.namespace,
		},
		Spec: operatorv1.IngressControllerSpec{
			Replicas: &two,
		},
	}
	err := o.client.Get(context.TODO(), types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}, ic)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	err = o.client.Create(context.TODO(), ic)
	if err != nil {
		return err
	}
	log.Info("created default ingresscontroller", "namespace", ic.Namespace, "name", ic.Name)
	return nil
}
