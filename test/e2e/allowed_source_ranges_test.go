//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TestAllowedSourceRanges creates an ingresscontroller with the
// "LoadBalancerService" endpoint publishing strategy type and verifies that the
// operator correctly configures the associated load balancer and updates its
// configuration when the ingresscontroller's allowed source ranges are changed.
func TestAllowedSourceRanges(t *testing.T) {
	t.Parallel()

	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AWSPlatformType:   {},
		configv1.AzurePlatformType: {},
		configv1.GCPPlatformType:   {},
	}
	if _, supported := supportedPlatforms[infraConfig.Status.PlatformStatus.Type]; !supported {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}

	validCIDR := "127.0.0.0/8"
	invalidCIDR := "127.0.0.1"
	// Try creating an ingresscontroller with an invalid CIDR
	t.Logf("Trying to create ingresscontroller with spec.endpointPublishingStrategy.loadBalancer.allowedSourceRanges set to an invalid CIDR range: %s", invalidCIDR)
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "sourcerange"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(name, domain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		AllowedSourceRanges: []operatorv1.CIDR{operatorv1.CIDR(invalidCIDR)},
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
	}
	if err := kclient.Create(context.TODO(), ic); err == nil {
		assertIngressControllerDeleted(t, kclient, ic)
		t.Fatalf("expected ingresscontroller creation to be failed with invalid CIDR: %s", invalidCIDR)
	}

	// Create an ingresscontroller with a valid CIDR
	t.Logf("Creating ingresscontroller with spec.endpointPublishingStrategy.loadBalancer.allowedSourceRanges set to a valid CIDR range %s", validCIDR)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.AllowedSourceRanges = []operatorv1.CIDR{operatorv1.CIDR(validCIDR)}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}

	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Poll for a minute and verify that LoadBalancerSourceRanges field is updated as expected.
	lbService := &corev1.Service{}
	err := wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			return false, fmt.Errorf("failed to get service: %w", err)
		}

		if len(lbService.Spec.LoadBalancerSourceRanges) == 1 && lbService.Spec.LoadBalancerSourceRanges[0] == validCIDR {
			t.Logf("LoadBalancerSourceRanges was updated as expected")
			return true, nil
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected LoadBalancerSourceRanges to be updated: %v, lbService: %v", err, lbService)
	}

	// Update the AllowedSourceRanges and verify that LoadBalancerSourceRanges field is updated as well.
	updatedCIDR := "10.0.0.0/8"
	t.Log("Updating AllowedSourceRanges")
	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.AllowedSourceRanges = []operatorv1.CIDR{operatorv1.CIDR(updatedCIDR)}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatal(err)
	}

	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			return false, fmt.Errorf("failed to get service: %w", err)
		}

		if len(lbService.Spec.LoadBalancerSourceRanges) > 0 && lbService.Spec.LoadBalancerSourceRanges[0] == updatedCIDR {
			t.Logf("LoadBalancerSourceRanges was updated as expected")
			return true, nil
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected LoadBalancerSourceRanges to be updated: %v", err)
	}
}

// TestAllowedSourceRangesStatus creates an ingresscontroller with the
// "LoadBalancerService" endpoint publishing strategy type, adds
// service.beta.kubernetes.io/load-balancer-source-ranges annotation
// to the service, and verifies that it is reflected in AllowedSourceRanges
// in IngressController's status. Then, it sets  LoadBalancerSourceRanges
// and verifies that it is reflected in AllowedSourceRanges instead of
// the annotation as the field takes precedence over the annotation.
//
// This test is serialized because it has been observed to conflict with
// TestScopeChange.
func TestAllowedSourceRangesStatus(t *testing.T) {
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AWSPlatformType:   {},
		configv1.AzurePlatformType: {},
		configv1.GCPPlatformType:   {},
	}
	if _, supported := supportedPlatforms[infraConfig.Status.PlatformStatus.Type]; !supported {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}

	// Create an ingresscontroller with a loadbalancer endpoint publishing strategy.
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "sourcerangesstatus"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(name, domain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get service: %v", err)
	}

	// Set service.beta.kubernetes.io/load-balancer-source-ranges annotation and see if it will be reflected in AllowedSourceRanges in IngressController's status
	lbService.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey] = "127.0.0.0/8"
	if err := kclient.Update(context.TODO(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}

	err := wait.PollImmediate(10*time.Second, 2*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		cidrs := []string{}
		if ic.Status.EndpointPublishingStrategy != nil {
			lb := ic.Status.EndpointPublishingStrategy.LoadBalancer
			if lb != nil && len(lb.AllowedSourceRanges) > 0 {
				for _, cidr := range lb.AllowedSourceRanges {
					cidrs = append(cidrs, string(cidr))
				}
			}
		}

		annotationRanges := strings.Split(lbService.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey], ",")
		t.Logf("Annotation: %v, AllowedSourceRanges: %v", annotationRanges, cidrs)
		// Although it does not complain at compile or run time, reflect.DeepEqual(annotationRanges, lb.AllowedSourceRanges)
		// does not work as they have different types ([]string versus []CIDR).
		if reflect.DeepEqual(annotationRanges, cidrs) {
			return true, nil
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected the annotation to be reflected in status.allowedSourceRanges: %v", err)
	}

	// Set .Spec.LoadBalancerSourceRanges and see if it will be reflected in AllowedSourceRanges in IngressController's status
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get service: %v", err)
	}
	lbService.Spec.LoadBalancerSourceRanges = []string{"0.0.0.0/0"}
	if err := kclient.Update(context.TODO(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}

	err = wait.PollImmediate(10*time.Second, 2*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		cidrs := []string{}
		if ic.Status.EndpointPublishingStrategy != nil {
			lb := ic.Status.EndpointPublishingStrategy.LoadBalancer
			if lb != nil && len(lb.AllowedSourceRanges) > 0 {
				for _, cidr := range lb.AllowedSourceRanges {
					cidrs = append(cidrs, string(cidr))
				}
			}
		}

		t.Logf("LoadBalancerSourceRanges: %v, AllowedSourceRanges: %v", lbService.Spec.LoadBalancerSourceRanges, cidrs)

		if reflect.DeepEqual(lbService.Spec.LoadBalancerSourceRanges, cidrs) {
			return true, nil
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected the LoadBalancerSourceRanges to be reflected in status.allowedSourceRanges: %v", err)
	}
}

// TestSourceRangesProgressingAndEvaluationConditionsDetectedStatuses creates
// an ingresscontroller with the "LoadBalancerService" endpoint publishing strategy type, adds
// service.beta.kubernetes.io/load-balancer-source-ranges annotation
// to the service, and verifies that progressing and evaluation conditions
// detected statuses of the ingresscontroller are set to true.
// Then, it clears the annotation, sets LoadBalancerSourceRanges field
// of the service, leaves AllowedSourceRanges of ingresscontroller empty,
// and verifies that progressing and evaluation conditions detected statuses of
// the ingresscontroller are set to true as the fields do not match.
func TestSourceRangesProgressingAndEvaluationConditionsDetectedStatuses(t *testing.T) {
	t.Parallel()

	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AWSPlatformType:   {},
		configv1.AzurePlatformType: {},
		configv1.GCPPlatformType:   {},
	}
	if _, supported := supportedPlatforms[infraConfig.Status.PlatformStatus.Type]; !supported {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}

	// Create an ingresscontroller with a loadbalancer endpoint publishing strategy.
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "sourcerangeannotation"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(name, domain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get service: %v", err)
	}

	// Set the annotation and verify statuses are set to true
	lbService.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey] = "127.0.0.0/8"
	if err := kclient.Update(context.TODO(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}
	err := wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		progressing := false
		evalCondDetected := false
		for _, cond := range ic.Status.Conditions {
			if cond.Type == operatorv1.OperatorStatusTypeProgressing && cond.Status == operatorv1.ConditionTrue {
				progressing = true
			} else if cond.Type == ingresscontroller.IngressControllerEvaluationConditionsDetectedConditionType && cond.Status == operatorv1.ConditionTrue {
				evalCondDetected = true
			}
			if progressing && evalCondDetected {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected ingresscontroller to have progressing=true and evaluationConditionsDetected=true: %v, lbService: %v", err, lbService)
	}

	// Remove annotation and verify statuses are back to false
	delete(lbService.Annotations, corev1.AnnotationLoadBalancerSourceRangesKey)
	if err := kclient.Update(context.TODO(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}
	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		progressing := true
		evalCondDetected := true
		for _, cond := range ic.Status.Conditions {
			if cond.Type == operatorv1.OperatorStatusTypeProgressing && cond.Status == operatorv1.ConditionFalse {
				progressing = false
			} else if cond.Type == ingresscontroller.IngressControllerEvaluationConditionsDetectedConditionType && cond.Status == operatorv1.ConditionFalse {
				evalCondDetected = false
			}
			if !progressing && !evalCondDetected {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected ingresscontroller to have progressing=false and evaluationConditionsDetected=false: %v, lbService: %v", err, lbService)
	}

	// Set LoadBalancerSourceRanges when AllowedSourceRanges is empty, verify statuses are set to true
	lbService.Spec.LoadBalancerSourceRanges = []string{"127.0.0.0/8"}
	if err := kclient.Update(context.TODO(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}
	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		progressing := false
		evalCondDetected := false
		for _, cond := range ic.Status.Conditions {
			if cond.Type == operatorv1.OperatorStatusTypeProgressing && cond.Status == operatorv1.ConditionTrue {
				progressing = true
			} else if cond.Type == ingresscontroller.IngressControllerEvaluationConditionsDetectedConditionType && cond.Status == operatorv1.ConditionTrue {
				evalCondDetected = true
			}
			if progressing && evalCondDetected {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected ingresscontroller to have progressing=true and evaluationConditionsDetected=true: %v, lbService: %v", err, lbService)
	}

	// Unset LoadBalancerSourceRanges and verify statuses are back to false
	lbService.Spec.LoadBalancerSourceRanges = nil
	if err := kclient.Update(context.TODO(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}
	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		progressing := true
		evalCondDetected := true
		for _, cond := range ic.Status.Conditions {
			if cond.Type == operatorv1.OperatorStatusTypeProgressing && cond.Status == operatorv1.ConditionFalse {
				progressing = false
			} else if cond.Type == ingresscontroller.IngressControllerEvaluationConditionsDetectedConditionType && cond.Status == operatorv1.ConditionFalse {
				evalCondDetected = false
			}
			if !progressing && !evalCondDetected {
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("expected ingresscontroller to have progressing=false and evaluationConditionsDetected=false: %v, lbService: %v", err, lbService)
	}
}
