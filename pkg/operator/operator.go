package operator

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/library-go/pkg/operator/v1helpers"

	errorpageconfigmapcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/sync-http-error-code-configmap"
	"github.com/openshift/library-go/pkg/operator/onepodpernodeccontroller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	canarycontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/canary"
	certcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/certificate"
	certpublishercontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/certificate-publisher"
	clientcacontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/clientca-configmap"
	configurableroutecontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/configurable-route"
	crlcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/crl"
	dnscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/dns"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	ingressclasscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingressclass"
	statuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/status"
	"github.com/openshift/library-go/pkg/operator/events"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

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

	namespace string
}

// New creates (but does not start) a new operator from configuration.
func New(config operatorconfig.Config, kubeConfig *rest.Config) (*Operator, error) {
	scheme := operatorclient.GetScheme()
	// Set up an operator manager for the operator namespace.
	mgr, err := manager.New(kubeConfig, manager.Options{
		Namespace: config.Namespace,
		Scheme:    scheme,
		NewCache: cache.MultiNamespacedCacheBuilder([]string{
			config.Namespace,
			operatorcontroller.GlobalUserSpecifiedConfigNamespace,
			operatorcontroller.DefaultOperandNamespace,
			operatorcontroller.DefaultCanaryNamespace,
			operatorcontroller.GlobalMachineSpecifiedConfigNamespace,
			"",
			operatorcontroller.SourceConfigMapNamespace,
			operatorcontroller.GlobalUserSpecifiedConfigNamespace,
		}),
		// Use a non-caching client everywhere. The default split client does not
		// promise to invalidate the cache during writes (nor does it promise
		// sequential create/get coherence), and we have code which (probably
		// incorrectly) assumes a get immediately following a create/update will
		// return the updated resource. All client consumers will need audited to
		// ensure they are tolerant of stale data (or we need a cache or client that
		// makes stronger coherence guarantees).
		NewClient: func(_ cache.Cache, config *rest.Config, options client.Options, uncachedObjects ...client.Object) (client.Client, error) {
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

	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %w", err)
	}
	namespaceInformers := informers.NewSharedInformerFactoryWithOptions(kubeClient, 24*time.Hour, informers.WithNamespace(operatorcontroller.DefaultOperandNamespace))
	// this only handles the case for the default router which is used for oauth-server, console, and other
	// platform services.  The scheduler bug will need to be fixed to correct the rest.
	// Once https://bugzilla.redhat.com/show_bug.cgi?id=2062459 is fixed, this controller can be removed.
	forcePodSpread := onepodpernodeccontroller.NewOnePodPerNodeController(
		"spread-default-router-pods",
		operatorcontroller.DefaultOperandNamespace,
		operatorcontroller.IngressControllerDeploymentPodSelector(&operatorv1.IngressController{ObjectMeta: metav1.ObjectMeta{Name: "default"}}),
		30, // minReadySeconds from deployments.apps/router-default
		events.NewKubeRecorder(kubeClient.CoreV1().Events(config.Namespace), "cluster-ingress-operator", &corev1.ObjectReference{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  config.Namespace,
			Name:       "ingress-operator",
		}),
		// ingress operator appears to be wired to a namespaced resource
		v1helpers.NewFakeOperatorClient(&operatorv1.OperatorSpec{ManagementState: operatorv1.Managed}, &operatorv1.OperatorStatus{}, nil),
		kubeClient,
		namespaceInformers.Core().V1().Pods(),
	)
	go forcePodSpread.Run(context.TODO(), 1)
	go namespaceInformers.Start(config.Stop)

	// Create and register the configurable route controller with the operator manager.
	if _, err := configurableroutecontroller.New(mgr, configurableroutecontroller.Config{
		SecretNamespace: operatorcontroller.GlobalUserSpecifiedConfigNamespace,
	}, events.NewLoggingEventRecorder(configurableroutecontroller.ControllerName)); err != nil {
		return nil, fmt.Errorf("failed to create configurable-route controller: %v", err)
	}

	// Set up the status controller.
	if _, err := statuscontroller.New(mgr, statuscontroller.Config{
		Namespace:              config.Namespace,
		IngressControllerImage: config.IngressControllerImage,
		CanaryImage:            config.CanaryImage,
		OperatorReleaseVersion: config.OperatorReleaseVersion,
	}); err != nil {
		return nil, fmt.Errorf("failed to create status controller: %v", err)
	}

	// Set up the certificate controller
	if _, err := certcontroller.New(mgr, config.Namespace); err != nil {
		return nil, fmt.Errorf("failed to create cacert controller: %v", err)
	}

	// Set up the error-page configmap controller.
	if _, err := errorpageconfigmapcontroller.New(mgr, errorpageconfigmapcontroller.Config{
		OperatorNamespace: config.Namespace,
		ConfigNamespace:   operatorcontroller.GlobalUserSpecifiedConfigNamespace,
		OperandNamespace:  operatorcontroller.DefaultOperandNamespace,
	}); err != nil {
		return nil, fmt.Errorf("failed to create error-page configmap controller: %w", err)
	}

	// Set up the certificate-publisher controller
	if _, err := certpublishercontroller.New(mgr, config.Namespace, operatorcontroller.DefaultOperandNamespace); err != nil {
		return nil, fmt.Errorf("failed to create certificate-publisher controller: %v", err)
	}

	// Set up the client CA configmap controller
	if _, err := clientcacontroller.New(mgr, clientcacontroller.Config{
		OperatorNamespace: config.Namespace,
		SourceNamespace:   operatorcontroller.GlobalUserSpecifiedConfigNamespace,
		TargetNamespace:   operatorcontroller.DefaultOperandNamespace,
	}); err != nil {
		return nil, fmt.Errorf("failed to create client CA configmap controller: %w", err)
	}

	// Set up the crl controller
	if _, err := crlcontroller.New(mgr); err != nil {
		return nil, fmt.Errorf("failed to create crl controller: %v", err)
	}

	// Set up the DNS controller
	if _, err := dnscontroller.New(mgr, dnscontroller.Config{
		Namespace:              config.Namespace,
		OperatorReleaseVersion: config.OperatorReleaseVersion,
	}); err != nil {
		return nil, fmt.Errorf("failed to create dns controller: %v", err)
	}

	// Set up the ingressclass controller.
	if _, err := ingressclasscontroller.New(mgr, ingressclasscontroller.Config{
		Namespace: config.Namespace,
	}); err != nil {
		return nil, fmt.Errorf("failed to create ingressclass controller: %w", err)
	}

	// Set up the canary controller when the config.CanaryImage is not empty
	// Canary can be disabled when running the operator locally.
	if len(config.CanaryImage) != 0 {
		if _, err := canarycontroller.New(mgr, canarycontroller.Config{
			Namespace:   config.Namespace,
			CanaryImage: config.CanaryImage,
			Stop:        config.Stop,
		}); err != nil {
			return nil, fmt.Errorf("failed to create canary controller: %v", err)
		}
	}

	return &Operator{
		manager: mgr,
		// TODO: These are only needed for the default ingress controller stuff, which
		// should be refactored away.
		client:    mgr.GetClient(),
		namespace: config.Namespace,
	}, nil
}

// Start creates the default IngressController and then starts the operator
// synchronously until a message is received on the stop channel.
// TODO: Move the default IngressController logic elsewhere.
func (o *Operator) Start(ctx context.Context) error {
	// Periodicaly ensure the default controller exists.
	go wait.Until(func() {
		if !o.manager.GetCache().WaitForCacheSync(ctx) {
			log.Error(nil, "failed to sync cache before ensuring default ingresscontroller")
			return
		}
		err := o.ensureDefaultIngressController()
		if err != nil {
			log.Error(err, "failed to ensure default ingresscontroller")
		}
	}, 1*time.Minute, ctx.Done())

	errChan := make(chan error)
	go func() {
		errChan <- o.manager.Start(ctx)
	}()

	// Wait for the manager to exit or an explicit stop.
	select {
	case <-ctx.Done():
		return nil
	case err := <-errChan:
		return err
	}
}

// ensureDefaultIngressController creates the default ingresscontroller if it
// doesn't already exist.
func (o *Operator) ensureDefaultIngressController() error {
	name := types.NamespacedName{Namespace: o.namespace, Name: manifests.DefaultIngressControllerName}
	ic := &operatorv1.IngressController{}
	if err := o.client.Get(context.TODO(), name, ic); err == nil {
		return nil
	} else if !errors.IsNotFound(err) {
		return err
	}
	infraConfig := &configv1.Infrastructure{}
	if err := o.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return err
	}
	// Set the replicas field to a non-nil value because otherwise its
	// persisted value will be nil, which causes GETs on the /scale
	// subresource to fail, which breaks the scaling client.  See also:
	// https://github.com/kubernetes/kubernetes/pull/75210
	//
	// TODO: Set the replicas value to the number of workers.
	replicas := int32(2)
	if infraConfig.Status.InfrastructureTopology == configv1.SingleReplicaTopologyMode {
		replicas = 1
	}
	ic = &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Spec: operatorv1.IngressControllerSpec{
			Replicas: &replicas,
		},
	}
	if err := o.client.Create(context.TODO(), ic); err != nil {
		return err
	}
	log.Info("created default ingresscontroller", "namespace", ic.Namespace, "name", ic.Name)
	return nil
}
