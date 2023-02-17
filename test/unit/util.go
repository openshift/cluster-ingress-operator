package unit

import (
	v1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type routeBuilder struct {
	name          types.NamespacedName
	labels        map[string]string
	admittedICs   []string
	unAdmittedICs []string
}

func NewRouteBuilder() *routeBuilder {
	return &routeBuilder{
		name: types.NamespacedName{Name: "sample", Namespace: "openshift-ingress"},
	}
}

func (b *routeBuilder) WithName(name types.NamespacedName) *routeBuilder {
	b.name = name
	return b
}

func (b *routeBuilder) WithLabels(labels map[string]string) *routeBuilder {
	b.labels = labels
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

func (b routeBuilder) Build() *routev1.Route {
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name.Name,
			Namespace: b.name.Namespace,
			Labels:    b.labels,
		},
		Spec:   routev1.RouteSpec{},
		Status: routev1.RouteStatus{},
	}

	if len(b.admittedICs) != 0 {
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
	}

	if len(b.unAdmittedICs) != 0 {
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
	}

	return route
}

type ingressControllerBuilder struct {
	name                        types.NamespacedName
	namespaceSelectors          map[string]string
	routeSelectors              map[string]string
	namespaceExpressionSelector []metav1.LabelSelectorRequirement
	routeExpressionSelector     []metav1.LabelSelectorRequirement
}

func NewIngressControllerBuilder() *ingressControllerBuilder {
	return &ingressControllerBuilder{
		name: types.NamespacedName{Name: "sample", Namespace: "openshift-ingress"},
	}
}

func (b *ingressControllerBuilder) WithName(name types.NamespacedName) *ingressControllerBuilder {
	b.name = name
	return b
}

func (b *ingressControllerBuilder) WithNamespaceSelectors(namespaceSelectors map[string]string) *ingressControllerBuilder {
	b.namespaceSelectors = namespaceSelectors
	return b
}

func (b *ingressControllerBuilder) WithRouteSelectors(routeSelectors map[string]string) *ingressControllerBuilder {
	b.routeSelectors = routeSelectors
	return b
}

func (b *ingressControllerBuilder) WithRouteExpressionSelector(routeExpressionSelectors metav1.LabelSelectorRequirement) *ingressControllerBuilder {
	b.routeExpressionSelector = []metav1.LabelSelectorRequirement{routeExpressionSelectors}
	return b
}

func (b *ingressControllerBuilder) WithNamespaceExpressionSelector(namespaceExpressionSelectors metav1.LabelSelectorRequirement) *ingressControllerBuilder {
	b.namespaceExpressionSelector = []metav1.LabelSelectorRequirement{namespaceExpressionSelectors}
	return b
}

func (b ingressControllerBuilder) Build() *v1.IngressController {
	ic := &v1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name.Name,
			Namespace: b.name.Namespace,
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
	return ic
}

type namespaceBuilder struct {
	name   string
	labels map[string]string
}

func NewNamespaceBuilder() *namespaceBuilder {
	return &namespaceBuilder{
		name: "name",
	}
}

func (b *namespaceBuilder) WithName(name string) *namespaceBuilder {
	b.name = name
	return b
}

func (b *namespaceBuilder) WithLabels(labels map[string]string) *namespaceBuilder {
	b.labels = labels
	return b
}

func (b namespaceBuilder) Build() *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   b.name,
			Labels: b.labels,
		},
	}
}
