package controller

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// syncIngressControllerStatus computes the current status of ic and
// updates status upon any changes since last sync.
func (r *reconciler) syncIngressControllerStatus(deployment *appsv1.Deployment, ic *operatorv1.IngressController) error {
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return fmt.Errorf("deployment has invalid spec.selector: %v", err)
	}

	updated := ic.DeepCopy()
	updated.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	updated.Status.Selector = selector.String()

	updated.Status.Conditions = []operatorv1.OperatorCondition{}
	updated.Status.Conditions = append(updated.Status.Conditions, computeIngressStatusConditions(updated.Status.Conditions, deployment)...)
	updated.Status.Conditions = append(updated.Status.Conditions, r.statusCache.computeLoadBalancerStatus(ic)...)

	for i := range updated.Status.Conditions {
		newCondition := &updated.Status.Conditions[i]
		var oldCondition *operatorv1.OperatorCondition
		for j, possibleOldCondition := range ic.Status.Conditions {
			if possibleOldCondition.Type == newCondition.Type {
				old := ic.Status.Conditions[j]
				oldCondition = &old
				break
			}
		}
		setIngressLastTransitionTime(newCondition, oldCondition)
	}

	if !ingressStatusesEqual(updated.Status, ic.Status) {
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update ingresscontroller status: %v", err)
		}
	}

	return nil
}

// computeIngressStatusConditions computes the ingress controller's current state.
func computeIngressStatusConditions(oldConditions []operatorv1.OperatorCondition, deployment *appsv1.Deployment) []operatorv1.OperatorCondition {
	oldAvailableCondition := getIngressAvailableCondition(oldConditions)

	return []operatorv1.OperatorCondition{
		computeIngressAvailableCondition(oldAvailableCondition, deployment),
	}
}

// computeIngressAvailableCondition computes the ingress controller's current Available status state.
func computeIngressAvailableCondition(oldAvailableCondition *operatorv1.OperatorCondition, deployment *appsv1.Deployment) operatorv1.OperatorCondition {
	availableCondition := operatorv1.OperatorCondition{
		Type: operatorv1.IngressControllerAvailableConditionType,
	}

	if deployment.Status.AvailableReplicas > 0 {
		availableCondition.Status = operatorv1.ConditionTrue
	} else {
		availableCondition.Status = operatorv1.ConditionFalse
		availableCondition.Reason = "DeploymentUnavailable"
		availableCondition.Message = "no Deployment replicas available"
	}

	return availableCondition
}

// getIngressAvailableCondition fetches ingress controller's available condition from the given conditions.
func getIngressAvailableCondition(conditions []operatorv1.OperatorCondition) *operatorv1.OperatorCondition {
	var availableCondition *operatorv1.OperatorCondition
	for i := range conditions {
		switch conditions[i].Type {
		case operatorv1.IngressControllerAvailableConditionType:
			availableCondition = &conditions[i]
			break
		}
	}

	return availableCondition
}

// setIngressLastTransitionTime sets LastTransitionTime for the given ingress controller condition.
// If the condition has changed, it will assign a new timestamp otherwise keeps the old timestamp.
func setIngressLastTransitionTime(condition, oldCondition *operatorv1.OperatorCondition) {
	if oldCondition != nil && condition.Status == oldCondition.Status &&
		condition.Reason == oldCondition.Reason && condition.Message == oldCondition.Message {
		condition.LastTransitionTime = oldCondition.LastTransitionTime
	} else {
		condition.LastTransitionTime = metav1.Now()
	}
}

// ingressStatusesEqual compares two IngressControllerStatus values.  Returns true
// if the provided values should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func ingressStatusesEqual(a, b operatorv1.IngressControllerStatus) bool {
	conditionCmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
	}
	if !cmp.Equal(a.Conditions, b.Conditions, conditionCmpOpts...) || a.AvailableReplicas != b.AvailableReplicas ||
		a.Selector != b.Selector {
		return false
	}

	return true
}

func isProvisioned(service *corev1.Service) bool {
	ingresses := service.Status.LoadBalancer.Ingress
	return len(ingresses) > 0 && (len(ingresses[0].Hostname) > 0 || len(ingresses[0].IP) > 0)
}

func isPending(service *corev1.Service) bool {
	return !isProvisioned(service)
}

func getServiceOwnerIfMatches(service *corev1.Service, matches func(*corev1.Service) bool) []string {
	if !matches(service) {
		return []string{}
	}
	controller, ok := service.Labels[manifests.OwningIngressControllerLabel]
	if !ok {
		return []string{}
	}
	return []string{controller}
}

func indexLoadBalancerControllerByName(obj runtime.Object) []string {
	c, ok := obj.(*operatorv1.IngressController)
	if !ok {
		return []string{}
	}
	if c.Status.EndpointPublishingStrategy != nil &&
		c.Status.EndpointPublishingStrategy.Type == operatorv1.LoadBalancerServiceStrategyType {
		return []string{c.Name}
	}
	return []string{}
}

func indexProvisionedLoadBalancerServiceByOwner(obj runtime.Object) []string {
	service, ok := obj.(*corev1.Service)
	if !ok {
		return []string{}
	}
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return []string{}
	}
	return getServiceOwnerIfMatches(service, isProvisioned)
}

func indexPendingLoadBalancerServiceByOwner(obj runtime.Object) []string {
	service, ok := obj.(*corev1.Service)
	if !ok {
		return []string{}
	}
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return []string{}
	}
	return getServiceOwnerIfMatches(service, isPending)
}

// ingressStatusCache knows how to compute status for ingress controllers by
// querying indexes caches.
type ingressStatusCache struct {
	// contains returns true if there are >0 matches on value for the named index
	// for the given kind.
	//
	// listKind must be the List type for the object being indexed.
	contains func(listKind runtime.Object, name, value string) bool
}

// serviceIndexers are all the Service indexes supported by the cache.
var serviceIndexers = map[string]client.IndexerFunc{
	"pending-for":     indexPendingLoadBalancerServiceByOwner,
	"provisioned-for": indexProvisionedLoadBalancerServiceByOwner,
}

// controllerIndexers are all the IngressController indexes supported by the
// cache.
var controllerIndexers = map[string]client.IndexerFunc{
	"wants-load-balancer": indexLoadBalancerControllerByName,
}

// cacheContains is a contains fuction which knows how to query a cache.Cache.
func cacheContains(cache cache.Cache, list runtime.Object, name, value string) bool {
	err := cache.List(context.TODO(), list, client.MatchingField(name, value))
	if err != nil {
		return false
	}
	// TODO: after rebase, replace with:
	// meta.LenList(list) > 0
	items, _ := meta.ExtractList(list)
	return len(items) > 0
}

func NewIngressStatusCache(c cache.Cache) *ingressStatusCache {
	add := func(cache cache.Cache, kind runtime.Object, indexers map[string]client.IndexerFunc) {
		for name := range indexers {
			cache.IndexField(kind, name, indexers[name])
		}
	}
	add(c, &operatorv1.IngressController{}, controllerIndexers)
	add(c, &corev1.Service{}, serviceIndexers)
	return &ingressStatusCache{
		contains: func(kind runtime.Object, name, value string) bool {
			return cacheContains(c, kind, name, value)
		},
	}
}

// computeLoadBalancerStatus returns the complete set of current
// LoadBalancer-prefixed conditions for the given ingress controller.
func (c *ingressStatusCache) computeLoadBalancerStatus(ic *operatorv1.IngressController) []operatorv1.OperatorCondition {
	if !c.contains(&operatorv1.IngressControllerList{}, "wants-load-balancer", ic.Name) {
		return []operatorv1.OperatorCondition{
			{
				Type:    operatorv1.LoadBalancerManagedIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedEndpointPublishingStrategy",
				Message: fmt.Sprintf("The %s endpoint publishing strategy does not support a load balancer", ic.Status.EndpointPublishingStrategy.Type),
			},
		}
	}

	conditions := []operatorv1.OperatorCondition{}

	conditions = append(conditions, operatorv1.OperatorCondition{
		Type:    operatorv1.LoadBalancerManagedIngressConditionType,
		Status:  operatorv1.ConditionTrue,
		Reason:  "HasLoadBalancerEndpointPublishingStrategy",
		Message: "IngressController has LoadBalancer endpoint publishing strategy",
	})

	switch {
	case c.contains(&corev1.ServiceList{}, "pending-for", ic.Name):
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "LoadBalancerPending",
			Message: "The LoadBalancer service is pending",
		})
	case c.contains(&corev1.ServiceList{}, "provisioned-for", ic.Name):
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionTrue,
			Reason:  "LoadBalancerProvisioned",
			Message: "The LoadBalancer service is provisioned",
		})
	default:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "LoadBalancerNotFound",
			Message: "The LoadBalancer service resource is missing",
		})
	}

	return conditions
}
