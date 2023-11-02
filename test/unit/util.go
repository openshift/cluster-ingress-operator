package unit

import (
	"time"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	v1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type routeBuilder struct {
	name          string
	namespace     string
	labels        map[string]string
	admittedICs   []string
	unAdmittedICs []string
}

func NewRouteBuilder() *routeBuilder {
	return &routeBuilder{
		name:      "sample",
		namespace: "openshift-ingress",
		labels:    map[string]string{},
	}
}

func (b *routeBuilder) WithName(name string) *routeBuilder {
	b.name = name
	return b
}

func (b *routeBuilder) WithNamespace(namespace string) *routeBuilder {
	b.namespace = namespace
	return b
}

func (b *routeBuilder) WithLabel(key, value string) *routeBuilder {
	b.labels[key] = value
	return b
}

func (b *routeBuilder) WithAdmittedICs(admittedICs ...string) *routeBuilder {
	b.admittedICs = admittedICs
	return b
}

func (b *routeBuilder) WithUnAdmittedICs(unAdmittedICs ...string) *routeBuilder {
	b.unAdmittedICs = unAdmittedICs
	return b
}

func (b *routeBuilder) Build() *routev1.Route {
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.namespace,
			Labels:    b.labels,
		},
		Spec:   routev1.RouteSpec{},
		Status: routev1.RouteStatus{},
	}

	for _, ic := range b.admittedICs {
		route.Status.Ingress = append(route.Status.Ingress, routev1.RouteIngress{
			RouterName: ic,
			Conditions: []routev1.RouteIngressCondition{
				{
					Type:   routev1.RouteAdmitted,
					Status: corev1.ConditionTrue,
				},
			},
		})
	}

	for _, ic := range b.unAdmittedICs {
		route.Status.Ingress = append(route.Status.Ingress, routev1.RouteIngress{
			RouterName: ic,
			Conditions: []routev1.RouteIngressCondition{
				{
					Type:   routev1.RouteAdmitted,
					Status: corev1.ConditionFalse,
				},
			},
		})
	}

	return route
}

type ingressControllerBuilder struct {
	name                        string
	namespace                   string
	namespaceSelectors          map[string]string
	routeSelectors              map[string]string
	namespaceExpressionSelector []metav1.LabelSelectorRequirement
	routeExpressionSelector     []metav1.LabelSelectorRequirement
	deleting                    bool
	admittedStatus              *v1.OperatorCondition
}

func NewIngressControllerBuilder() *ingressControllerBuilder {
	return &ingressControllerBuilder{
		name:               "sample",
		namespace:          "openshift-ingress-operator",
		namespaceSelectors: map[string]string{},
		routeSelectors:     map[string]string{},
	}
}

func (b *ingressControllerBuilder) WithName(name string) *ingressControllerBuilder {
	b.name = name
	return b
}

func (b *ingressControllerBuilder) WithNamespace(namespace string) *ingressControllerBuilder {
	b.namespace = namespace
	return b
}

func (b *ingressControllerBuilder) WithNamespaceSelector(key, value string) *ingressControllerBuilder {
	b.namespaceSelectors[key] = value
	return b
}

func (b *ingressControllerBuilder) WithRouteSelector(key, value string) *ingressControllerBuilder {
	b.routeSelectors[key] = value
	return b
}

func (b *ingressControllerBuilder) WithRouteExpressionSelector(key string, operator metav1.LabelSelectorOperator, values []string) *ingressControllerBuilder {
	b.routeExpressionSelector = []metav1.LabelSelectorRequirement{{
		Key:      key,
		Operator: operator,
		Values:   values,
	}}
	return b
}

func (b *ingressControllerBuilder) WithNamespaceExpressionSelector(key string, operator metav1.LabelSelectorOperator, values []string) *ingressControllerBuilder {
	b.namespaceExpressionSelector = []metav1.LabelSelectorRequirement{{
		Key:      key,
		Operator: operator,
		Values:   values,
	}}
	return b
}

func (b *ingressControllerBuilder) IsDeleting() *ingressControllerBuilder {
	b.deleting = true
	return b
}

func (b *ingressControllerBuilder) WithAdmitted(admitted bool) *ingressControllerBuilder {
	var admittedStatus v1.ConditionStatus
	if admitted {
		admittedStatus = v1.ConditionTrue
	} else {
		admittedStatus = v1.ConditionFalse
	}

	b.admittedStatus = &v1.OperatorCondition{
		Type:   "Admitted",
		Status: admittedStatus,
	}
	return b
}

func (b *ingressControllerBuilder) Build() *v1.IngressController {
	ic := &v1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.namespace,
		},
		Spec: v1.IngressControllerSpec{
			RouteSelector: &metav1.LabelSelector{
				MatchLabels:      b.routeSelectors,
				MatchExpressions: b.routeExpressionSelector,
			},
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels:      b.namespaceSelectors,
				MatchExpressions: b.namespaceExpressionSelector,
			},
		},
	}
	if b.deleting {
		ic.ObjectMeta.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		ic.ObjectMeta.Finalizers = []string{manifests.IngressControllerFinalizer}
	}
	if b.admittedStatus != nil {
		ic.Status = v1.IngressControllerStatus{
			Conditions: append(ic.Status.Conditions, *b.admittedStatus),
		}
	}
	return ic
}

type namespaceBuilder struct {
	name   string
	labels map[string]string
}

func NewNamespaceBuilder() *namespaceBuilder {
	return &namespaceBuilder{
		name:   "name",
		labels: map[string]string{},
	}
}

func (b *namespaceBuilder) WithName(name string) *namespaceBuilder {
	b.name = name
	return b
}

func (b *namespaceBuilder) WithLabel(key, value string) *namespaceBuilder {
	b.labels[key] = value
	return b
}

func (b *namespaceBuilder) Build() *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   b.name,
			Labels: b.labels,
		},
	}
}
