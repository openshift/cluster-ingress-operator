package ingress

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	awsdns "github.com/openshift/cluster-ingress-operator/pkg/dns/aws"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"
	cvoclientset "github.com/openshift/cluster-version-operator/pkg/generated/clientset/versioned"
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// installerConfigNamespace is the namespace containing the installer config.
	installerConfigNamespace = "kube-system"

	// clusterConfigResource is the resource containing the installer config.
	clusterConfigResource = "cluster-config-v1"

	// ClusterIngressFinalizer is applied to all ClusterIngress resources before
	// they are considered for processing; this ensures the operator has a chance
	// to handle all states.
	// TODO: Make this generic and not tied to the "default" ingress.
	ClusterIngressFinalizer = "ingress.openshift.io/default-cluster-ingress"
)

var _ reconcile.Reconciler = &IngressReconciler{}

// IngressReconciler reconciles a ClusterIngress object
type IngressReconciler struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client          client.Client
	scheme          *runtime.Scheme
	installConfig   *util.InstallConfig
	manifestFactory *manifests.Factory
	namespace       string
	cvoClient       *cvoclientset.Clientset
	dnsManager      dns.Manager
}

// Add creates a new ClusterIngress Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	r, err := newReconciler(mgr)
	if err != nil {
		return err
	}

	// TODO: abstract default cluster ingress creation from controller
	if err := r.ensureDefaultClusterIngress(); err != nil {
		return fmt.Errorf("failed to ensure default cluster ingress: %v", err)
	}

	// Create a new controller
	c, err := controller.New("openshift-ingress-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource ClusterIngress
	ciMapFn := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: r.namespace}}}
		})
	err = c.Watch(&source.Kind{Type: &ingressv1alpha1.ClusterIngress{}}, &handler.EnqueueRequestsFromMapFunc{ToRequests: ciMapFn})
	if err != nil {
		return err
	}

	mapFn := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			// TODO Use a named constant for the router's namespace or get the namespace from config.
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "openshift-ingress"}}}
		})
	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestsFromMapFunc{ToRequests: mapFn})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Service{}}, &handler.EnqueueRequestsFromMapFunc{ToRequests: mapFn})
	if err != nil {
		return err
	}
	return nil
}

// Reconcile reads that state of the cluster for a ClusterIngress object and makes changes based on the state read
// and what is in the ClusterIngress.Spec
func (r *IngressReconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	defer r.syncOperatorStatus()
	logrus.Infof("reconciling in response to request %s/%s", request.Namespace, request.Name)

	// Ensure we have all the necessary scaffolding on which to place router instances.
	if err := r.ensureRouterNamespace(); err != nil {
		return reconcile.Result{}, err
	}

	// Fetch the ClusterIngress instance
	ingresses := &ingressv1alpha1.ClusterIngressList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterIngress",
			APIVersion: "ingress.openshift.io/v1alpha1",
		},
	}
	err := r.client.List(context.TODO(), &client.ListOptions{Namespace: r.namespace}, ingresses)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// Reconcile all the ingresses.
	errors := []error{}
	for _, ci := range ingresses.Items {
		if ci.DeletionTimestamp != nil {
			// Destroy any router associated with the clusteringress.
			err := r.ensureRouterDeleted(&ci)
			if err != nil {
				errors = append(errors, fmt.Errorf("failed to delete clusteringress %s/%s: %v", ci.Namespace, ci.Name, err))
				continue
			}
			// Clean up the finalizer to allow the clusteringress to be deleted.
			if slice.ContainsString(ci.Finalizers, ClusterIngressFinalizer) {
				ci.Finalizers = slice.RemoveString(ci.Finalizers, ClusterIngressFinalizer)
				if err = r.client.Update(context.TODO(), &ci); err != nil {
					errors = append(errors, fmt.Errorf("failed to remove finalizer from clusteringress %s/%s: %v", ci.Namespace, ci.Name, err))
					continue
				}
			}
		} else {
			// Handle active ingress.
			if err := r.ensureRouterForIngress(&ci); err != nil {
				errors = append(errors, fmt.Errorf("failed to ensure clusteringress %s/%s: %v", ci.Namespace, ci.Name, err))
				continue
			}
		}
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errors)
}

// newReconciler returns a new reconcile.Reconciler and esnures default cluster ingress
func newReconciler(mgr manager.Manager) (*IngressReconciler, error) {
	client, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to get k8s client: %v", err)
	}
	ic, err := util.GetInstallConfig(client)
	if err != nil {
		return nil, fmt.Errorf("failed to get installconfig: %v", err)
	}
	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		return nil, fmt.Errorf("failed to get watch namespace: %v", err)
	}

	cvoClient, err := cvoclientset.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to get cvo client: %v", err)
	}

	rClient := mgr.GetClient()
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
		err = rClient.Get(context.TODO(), types.NamespacedName{Name: awsCreds.Name, Namespace: awsCreds.Namespace}, awsCreds)
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

	return &IngressReconciler{
		client:          rClient,
		scheme:          mgr.GetScheme(),
		installConfig:   ic,
		manifestFactory: manifests.NewFactory(),
		namespace:       namespace,
		cvoClient:       cvoClient,
		dnsManager:      dnsManager,
	}, nil
}

// ensureDefaultClusterIngress ensures that a default ClusterIngress exists.
func (r *IngressReconciler) ensureDefaultClusterIngress() error {
	ci, err := r.manifestFactory.DefaultClusterIngress(r.installConfig)
	if err != nil {
		return err
	}

	err = r.client.Create(context.TODO(), ci)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if err == nil {
		logrus.Infof("created default clusteringress %s/%s", ci.Namespace, ci.Name)
	}
	return nil
}

// ensureRouterNamespace ensures all the necessary scaffolding exists for
// routers generally, including a namespace and all RBAC setup.
func (r *IngressReconciler) ensureRouterNamespace() error {
	cr, err := r.manifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role: %v", err)
	}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: cr.Name}, cr)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role %s: %v", cr.Name, err)
		}
		err = r.client.Create(context.TODO(), cr)
		if err == nil {
			logrus.Infof("created router cluster role %s", cr.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router cluster role %s: %v", cr.Name, err)
		}
	}

	ns, err := r.manifestFactory.RouterNamespace()
	if err != nil {
		return fmt.Errorf("failed to build router namespace: %v", err)
	}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router namespace %q: %v", ns.Name, err)
		}
		err = r.client.Create(context.TODO(), ns)
		if err == nil {
			logrus.Infof("created router namespace %s", ns.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router namespace %s: %v", ns.Name, err)
		}
	}

	sa, err := r.manifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("failed to build router service account: %v", err)
	}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: sa.Name, Namespace: sa.Namespace}, sa)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		err = r.client.Create(context.TODO(), sa)
		if err == nil {
			logrus.Infof("created router service account %s/%s", sa.Namespace, sa.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
	}

	crb, err := r.manifestFactory.RouterClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role binding: %v", err)
	}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role binding %s: %v", crb.Name, err)
		}
		err = r.client.Create(context.TODO(), crb)
		if err == nil {
			logrus.Infof("created router cluster role binding %s", crb.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router cluster role binding %s: %v", crb.Name, err)
		}
	}

	return nil
}

// ensureRouterForIngress ensures all necessary router resources exist for a
// given clusteringress.
func (r *IngressReconciler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress) error {
	ds, err := r.manifestFactory.RouterDaemonSet(ci)
	if err != nil {
		return fmt.Errorf("failed to build router daemonset: %v", err)
	}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: ds.Name, Namespace: ds.Namespace}, ds)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router daemonset %s/%s, %v", ds.Namespace, ds.Name, err)
		}
		err = r.client.Create(context.TODO(), ds)
		if err == nil {
			logrus.Infof("created router daemonset %s/%s", ds.Namespace, ds.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router daemonset %s/%s: %v", ds.Namespace, ds.Name, err)
		}
	}

	if ci.Spec.HighAvailability != nil {
		switch ci.Spec.HighAvailability.Type {
		case ingressv1alpha1.CloudClusterIngressHA:
			service, err := r.manifestFactory.RouterServiceCloud(ci)
			if err != nil {
				return fmt.Errorf("failed to build router service: %v", err)
			}

			err = r.client.Get(context.TODO(), types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, service)
			if err != nil {
				if !errors.IsNotFound(err) {
					return fmt.Errorf("failed to get router service %s/%s, %v", service.Namespace, service.Name, err)
				}

				if err := controllerutil.SetControllerReference(ds, service, r.scheme); err != nil {
					return fmt.Errorf("failed to set owner reference on service %s/%s, %v", service.Namespace, service.Name, err)
				}
				err = r.client.Create(context.TODO(), service)
				if err == nil {
					logrus.Infof("created router service %s/%s", service.Namespace, service.Name)
				} else if !errors.IsAlreadyExists(err) {
					return fmt.Errorf("failed to create router service %s/%s: %v", service.Namespace, service.Name, err)
				}
			}
			if ci.Spec.IngressDomain != nil {
				err = r.ensureDNSForLoadBalancer(ci, service)
				if err != nil {
					return fmt.Errorf("failed to ensure DNS for router service %s/%s: %v", service.Namespace, service.Name, err)
				}
			}
		}
	}

	return nil
}

// ensureDNSForLoadBalancer configures a wildcard DNS alias for a ClusterIngress
// targeting the given service.
func (r *IngressReconciler) ensureDNSForLoadBalancer(ci *ingressv1alpha1.ClusterIngress, service *corev1.Service) error {
	if len(service.Status.LoadBalancer.Ingress) == 0 {
		logrus.Infof("won't update DNS record for load balancer service %s/%s because status contains no ingresses", service.Namespace, service.Name)
		return nil
	}
	target := service.Status.LoadBalancer.Ingress[0].Hostname
	if len(target) == 0 {
		logrus.Infof("won't update DNS record for load balancer service %s/%s because ingress hostname is empty", service.Namespace, service.Name)
		return nil
	}
	// TODO: the routing wildcard prefix should come from cluster config or
	// ClusterIngress.
	domain := fmt.Sprintf("*.apps.%s", *ci.Spec.IngressDomain)
	return r.dnsManager.EnsureAlias(domain, target)
}

// ensureRouterDeleted ensures that any router resources associated with the
// clusteringress are deleted.
func (r *IngressReconciler) ensureRouterDeleted(ci *ingressv1alpha1.ClusterIngress) error {
	ds, err := r.manifestFactory.RouterDaemonSet(ci)
	if err != nil {
		return fmt.Errorf("failed to build router daemonset for deletion: %v", err)
	}
	err = r.client.Delete(context.TODO(), ds)
	if !errors.IsNotFound(err) {
		return err
	}
	return nil
}
