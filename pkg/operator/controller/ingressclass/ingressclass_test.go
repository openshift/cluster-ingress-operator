package ingressclass

import (
	"reflect"
	"testing"

	routev1 "github.com/openshift/api/route/v1"

	networkingv1 "k8s.io/api/networking/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestDesiredIngressClass verifies that desiredIngressClass behaves as
// expected.
func TestDesiredIngressClass(t *testing.T) {
	makeIngressClass := func(icName string, annotateAsDefault bool) *networkingv1.IngressClass {
		apiGroup := "operator.openshift.io"
		name := "openshift-" + icName
		class := networkingv1.IngressClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: networkingv1.IngressClassSpec{
				Controller: routev1.IngressToRouteIngressClassControllerName,
				Parameters: &networkingv1.IngressClassParametersReference{
					APIGroup: &apiGroup,
					Kind:     "IngressController",
					Name:     icName,
				},
			},
		}
		if annotateAsDefault {
			class.Annotations = map[string]string{
				"ingressclass.kubernetes.io/is-default-class": "true",
			}
		}
		return &class
	}
	testCases := []struct {
		description string

		haveIngressController bool
		ingressControllerName string
		ingressClasses        []networkingv1.IngressClass

		expectWant         bool
		expectIngressClass *networkingv1.IngressClass
	}{
		{
			description:           "no ingresscontroller",
			haveIngressController: false,
			expectWant:            false,
		},
		{
			description:           "custom ingresscontroller when no ingressclasses exist",
			haveIngressController: true,
			ingressControllerName: "custom",
			ingressClasses:        []networkingv1.IngressClass{},
			expectWant:            true,
			expectIngressClass:    makeIngressClass("custom", false),
		},
		{
			description:           "custom ingresscontroller when its ingressclass already exists",
			haveIngressController: true,
			ingressControllerName: "custom",
			ingressClasses: []networkingv1.IngressClass{
				*makeIngressClass("custom", false),
			},
			expectWant:         true,
			expectIngressClass: makeIngressClass("custom", false),
		},
		{
			description:           "custom ingresscontroller when its ingressclass already exists and is annotated as default",
			haveIngressController: true,
			ingressControllerName: "custom",
			ingressClasses: []networkingv1.IngressClass{
				*makeIngressClass("custom", true),
			},
			expectWant: true,
			// desired doesn't have the annotation, but that's all
			// right because the update logic ignores the user-set
			// annotation.
			expectIngressClass: makeIngressClass("custom", false),
		},
		{
			description:           "default ingresscontroller when no default ingressclass exists",
			haveIngressController: true,
			ingressControllerName: "default",
			ingressClasses:        []networkingv1.IngressClass{},
			expectWant:            true,
			// TODO This test case expects the default ingressclass
			// not to be annotated as default because doing so
			// breaks "[sig-network] IngressClass [Feature:Ingress]
			// should not set default value if no default
			// IngressClass"; we need to fix that test and then
			// update this test case.
			expectIngressClass: makeIngressClass("default", false),
		},
		{
			description:           "default ingresscontroller when some custom ingressclass exists and is annotated as default",
			haveIngressController: true,
			ingressControllerName: "default",
			ingressClasses: []networkingv1.IngressClass{
				*makeIngressClass("custom", true),
			},
			expectWant:         true,
			expectIngressClass: makeIngressClass("default", false),
		},
	}

	for _, tc := range testCases {
		want, class := desiredIngressClass(tc.haveIngressController, tc.ingressControllerName, tc.ingressClasses)
		if want != tc.expectWant {
			t.Errorf("%q: expected desiredIngressClass to return %t, got %t", tc.description, tc.expectWant, want)
		}
		if !reflect.DeepEqual(class, tc.expectIngressClass) {
			t.Errorf("%q: expected desiredIngressClass to return %+v, got %+v", tc.description, tc.expectIngressClass, class)
		}
	}
}

// TestIngressClassChanged verifies that ingressClassChanged behaves as
// expected.
func TestIngressClassChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*networkingv1.IngressClass)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *networkingv1.IngressClass) {},
			expect:      false,
		},
		{
			description: "if .uid changes",
			mutate: func(class *networkingv1.IngressClass) {
				class.UID = "2"
			},
			expect: false,
		},
		{
			description: "if .spec.controller changes",
			mutate: func(class *networkingv1.IngressClass) {
				class.Spec.Controller = "acme.io/ingress"
			},
			expect: true,
		},
		{
			description: "if .spec.parameters.kind changes",
			mutate: func(class *networkingv1.IngressClass) {
				class.Spec.Parameters.Kind = "AcmeIngress"
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		apiGroup := "operator.openshift.io"
		original := networkingv1.IngressClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "openshift-custom",
				UID:  "1",
			},
			Spec: networkingv1.IngressClassSpec{
				Controller: routev1.IngressToRouteIngressClassControllerName,
				Parameters: &networkingv1.IngressClassParametersReference{
					APIGroup: &apiGroup,
					Kind:     "IngressController",
					Name:     "custom",
				},
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := ingressClassChanged(&original, mutated); changed != tc.expect {
			t.Errorf("%s, expect ingressClassChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := ingressClassChanged(mutated, updated); changedAgain {
				t.Errorf("%s, ingressClassChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
