package configurableroutes

import (
	"context"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ControllerName             = "configurable_route_controller"
	componentRouteHashLabelKey = "ingress.operator.openshift.io/component-route-hash"
)

var (
	log = logf.Logger.WithName(ControllerName)
)

// New creates the configurable route controller from configuration. This is the controller
// that handles all the logic for generating roles and rolebindings for operators that
// include routes with configurable hostnames and serving certificates.
//
// Cluster-admins may provide a custom hostname and serving certificate for a route
// by creating a spec.componentRoute entry in the ingresses.config.openshift.io/cluster
// resource. If a componentRoute entry exists in the status.componentRoutes list with
// a matching namespace and name this controller will generate:
// - A role that grants get/list/watch permissions for the secret defined in the spec.
// - A roleBinding that binds the aforementioned role to each consumingUser specified
// in the corresponding status entry.
func New(mgr manager.Manager, config Config, eventRecorder events.Recorder) (controller.Controller, error) {
	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}

	reconciler := &reconciler{
		kclient:       kubeClient,
		config:        config,
		client:        mgr.GetClient(),
		cache:         mgr.GetCache(),
		eventRecorder: eventRecorder,
	}

	c, err := controller.New(ControllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// Trigger reconcile requests for the cluster ingress resource.
	clusterNamePredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		clusterIngressResource := operatorcontroller.IngressClusterConfigName()
		return o.GetName() == clusterIngressResource.Name && o.GetNamespace() == clusterIngressResource.Namespace
	})

	if err := c.Watch(&source.Kind{Type: &configv1.Ingress{}}, &handler.EnqueueRequestForObject{}, clusterNamePredicate); err != nil {
		return nil, err
	}

	// Trigger reconcile requests for the roles and roleBindings with the componentRoute label.
	defaultPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		labels := o.GetLabels()
		_, ok := labels[componentRouteHashLabelKey]
		return ok
	})

	if err := c.Watch(source.NewKindWithCache(&rbacv1.Role{}, mgr.GetCache()), handler.EnqueueRequestsFromMapFunc(reconciler.resourceToClusterIngressConfig), defaultPredicate); err != nil {
		return nil, err
	}

	if err := c.Watch(source.NewKindWithCache(&rbacv1.RoleBinding{}, mgr.GetCache()), handler.EnqueueRequestsFromMapFunc(reconciler.resourceToClusterIngressConfig), defaultPredicate); err != nil {
		return nil, err
	}

	return c, nil
}

// resourceToClusterIngressConfig is used to only trigger reconciles on the cluster ingress config.
func (r *reconciler) resourceToClusterIngressConfig(o client.Object) []reconcile.Request {
	return []reconcile.Request{
		{
			operatorcontroller.IngressClusterConfigName(),
		},
	}
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	SecretNamespace string
}

// reconciler handles the actual ingress reconciliation logic in response to
// events.
type reconciler struct {
	config        Config
	client        client.Client
	kclient       kubernetes.Interface
	cache         cache.Cache
	eventRecorder events.Recorder
}

// Reconcile expects request to refer to the
// ingresses.config.openshift.io/cluster object and will do all the work to
// ensure that RBAC for any configured component routes is in the desired state.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Only proceed if we can get the ingress resource.
	ingress := &configv1.Ingress{}
	if err := r.cache.Get(ctx, request.NamespacedName, ingress); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ingress cr not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingress %q: %w", request.NamespacedName, err)
	}

	// Get the list of componentRoutes defined in both the spec and status of the ingress resource that require
	// roles and roleBindings.
	componentRoutes := intersectingComponentRoutes(ingress.Spec.ComponentRoutes, ingress.Status.ComponentRoutes)

	// Ensure role and roleBindings exist for each valid componentRoute.
	for _, componentRoute := range componentRoutes {
		// Ensure role.
		roleName, err := r.ensureServiceCertKeyPairSecretRole(componentRoute)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to create role: %v", err)
		}

		// Get the role just created so the UID is available for the ownerReference on the roleBinding.
		role := &rbacv1.Role{}
		if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.config.SecretNamespace, Name: roleName}, role); err != nil {
			return reconcile.Result{}, err
		}

		// Ensure roleBinding.
		if err := r.ensureServiceCertKeyPairSecretRoleBinding(role, componentRoute); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to create roleBinding: %v", err)
		}
	}

	existingHashes := sets.String{}
	for _, cr := range componentRoutes {
		existingHashes.Insert(cr.Hash)
	}

	// Delete any roles or roleBindings that were generated for componentRoutes that are no longer defined.
	// RoleBindings are cleanedup by garbage collector due to owner reference to Role.
	if err := utilerrors.NewAggregate(r.deleteOrphanedRoles(componentRoutes, existingHashes)); err != nil {
		return reconcile.Result{}, fmt.Errorf("error(s) deleting orphaned roles: %v", err)
	}

	return reconcile.Result{}, nil
}

// newAggregatedComponentRoute returns an aggregatedComponentRoute.
func newAggregatedComponentRoute(spec configv1.ComponentRouteSpec, status configv1.ComponentRouteStatus) aggregatedComponentRoute {
	// Copy the list of consuming users.
	consumingUsersCopy := make([]configv1.ConsumingUser, len(status.ConsumingUsers))
	copy(consumingUsersCopy, status.ConsumingUsers)

	return aggregatedComponentRoute{
		Name:                   spec.Name,
		Hash:                   util.Hash(namespacedName(spec.Namespace, spec.Name)),
		ServingCertificateName: spec.ServingCertKeyPairSecret.Name,
		ConsumingUsers:         consumingUsersCopy,
	}
}

// aggregatedComponeRoute contains information from the ComponentRouteSpec
// and ComponentRouteStatus to generate the required Role and RoleBinding.
type aggregatedComponentRoute struct {
	Name                   string
	Hash                   string
	ServingCertificateName string
	ConsumingUsers         []configv1.ConsumingUser
}

// getSubjects returns a list of subjects defined in the aggregatedComponentRoute.
func (componentRoute *aggregatedComponentRoute) getSubjects() []rbacv1.Subject {
	subjects := []rbacv1.Subject{}
	for _, consumingUser := range componentRoute.ConsumingUsers {
		splitConsumingUser := strings.Split(string(consumingUser), ":")

		// Ignore invalid consuming users.
		if len(splitConsumingUser) != 4 {
			continue
		}

		switch splitConsumingUser[1] {
		case "serviceaccount":
			subjects = append(subjects, rbacv1.Subject{
				Kind:      rbacv1.ServiceAccountKind,
				APIGroup:  "",
				Name:      splitConsumingUser[3],
				Namespace: splitConsumingUser[2],
			})
		}
	}
	return subjects
}

// requiresRBAC returns a boolean indicating if the componentRoute requires roles or rolebindings to be generated.
func (componentRoute *aggregatedComponentRoute) requiresRBAC() bool {
	// Do not generate RBAC if no consuming users exist.
	if len(componentRoute.getSubjects()) == 0 {
		return false
	}

	// Do not generate RBAC if no secret is specified.
	if componentRoute.ServingCertificateName == "" {
		return false
	}

	return true
}

// intersectingComponentRoutes takes a slice of componentRouteSpec and a slice
// of componentRouteStatus, identifies which (namespace,name) tuples appear in
// both slices, and returns a slice of aggregatedComponentRoute corresponding to
// those tuples if they require Roles and RoleBindings.
func intersectingComponentRoutes(componentRouteSpecs []configv1.ComponentRouteSpec, componentRouteStatuses []configv1.ComponentRouteStatus) []aggregatedComponentRoute {
	componentRouteHashToComponentRouteStatus := map[string]configv1.ComponentRouteStatus{}
	for _, componentRouteStatus := range componentRouteStatuses {
		componentRouteHash := util.Hash(namespacedName(componentRouteStatus.Namespace, componentRouteStatus.Name))
		componentRouteHashToComponentRouteStatus[componentRouteHash] = componentRouteStatus
	}

	componentRoutes := []aggregatedComponentRoute{}
	for _, componentRouteSpec := range componentRouteSpecs {
		hash := util.Hash(namespacedName(componentRouteSpec.Namespace, componentRouteSpec.Name))
		if componentRouteStatus, ok := componentRouteHashToComponentRouteStatus[hash]; ok {
			componentRoute := newAggregatedComponentRoute(componentRouteSpec, componentRouteStatus)
			if componentRoute.requiresRBAC() {
				componentRoutes = append(componentRoutes, componentRoute)
			}
		}
	}
	return componentRoutes
}

func namespacedName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

func componentRouteResources(componentRoute aggregatedComponentRoute) []client.ListOption {
	return []client.ListOption{
		client.MatchingLabels{
			componentRouteHashLabelKey: componentRoute.Hash,
		},
		client.InNamespace(operatorcontroller.GlobalUserSpecifiedConfigNamespace),
	}
}

func allComponentRouteResources() []client.ListOption {
	return []client.ListOption{
		client.HasLabels{componentRouteHashLabelKey},
		client.InNamespace(operatorcontroller.GlobalUserSpecifiedConfigNamespace),
	}
}

func (r *reconciler) deleteOrphanedRoles(componentRoutes []aggregatedComponentRoute, existingHashes sets.String) []error {
	errors := []error{}
	roleList := &rbacv1.RoleList{}
	if err := r.cache.List(context.TODO(), roleList, allComponentRouteResources()...); err != nil {
		return append(errors, err)
	}
	for _, item := range roleList.Items {
		expectedHash, ok := item.GetLabels()[componentRouteHashLabelKey]
		if !ok {
			errors = append(errors, fmt.Errorf("Unable to find componentRoute hash label on role %s/%s", item.GetNamespace(), item.GetName()))
			continue
		}

		if !existingHashes.Has(expectedHash) {
			log.Info("deleting role", "name", item.GetName(), "namespace", item.GetNamespace())
			if err := r.client.Delete(context.TODO(), &item); err != nil && !apierrors.IsNotFound(err) {
				errors = append(errors, err)
			}
		}
	}

	return errors
}

func (r *reconciler) ensureServiceCertKeyPairSecretRole(componentRoute aggregatedComponentRoute) (string, error) {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: componentRoute.Name + "-",
			Namespace:    r.config.SecretNamespace,
			Labels: map[string]string{
				componentRouteHashLabelKey: componentRoute.Hash,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"get", "list", "watch"},
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{componentRoute.ServingCertificateName},
			},
		},
	}

	roleList := &rbacv1.RoleList{}
	if err := r.cache.List(context.TODO(), roleList, componentRouteResources(componentRoute)...); err != nil {
		return "", err
	}

	if len(roleList.Items) == 0 {
		if err := r.client.Create(context.TODO(), role); err != nil {
			return "", err
		}
	} else {
		role.Name = roleList.Items[0].Name
		role.GenerateName = ""
		if _, _, err := resourceapply.ApplyRole(context.TODO(), r.kclient.RbacV1(), r.eventRecorder, role); err != nil {
			return "", err
		}
	}

	return role.GetName(), nil
}

func (r *reconciler) ensureServiceCertKeyPairSecretRoleBinding(role *rbacv1.Role, componentRoute aggregatedComponentRoute) error {
	if role == nil {
		return fmt.Errorf("cannot be passed nil role")
	}
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      role.GetName(),
			Namespace: r.config.SecretNamespace,
			Labels: map[string]string{
				componentRouteHashLabelKey: componentRoute.Hash,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: rbacv1.SchemeGroupVersion.String(),
					Kind:       "Role",
					Name:       role.GetName(),
					UID:        role.GetUID(),
				},
			},
		},
		Subjects: componentRoute.getSubjects(),
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     role.GetName(),
			APIGroup: rbacv1.GroupName,
		},
	}

	_, _, err := resourceapply.ApplyRoleBinding(context.TODO(), r.kclient.RbacV1(), r.eventRecorder, roleBinding)
	return err
}
