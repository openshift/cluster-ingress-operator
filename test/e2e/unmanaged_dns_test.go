//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiserver/pkg/storage/names"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TODO: Remove this once this condition is added to all e2e test
// and stability issue is fixed.
var operatorProgressingFalse = operatorv1.OperatorCondition{
	Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse,
}

// TestUnmanagedDNSToManagedDNSIngressController tests dnsManagementPolicy during
// transitioning from Unmanaged to Managed DNS on an external ingress controller.
// The load balancer scope remains external throughout the transition, so only the
// DNS management policy changes.
func TestUnmanagedDNSToManagedDNSIngressController(t *testing.T) {
	t.Parallel()

	testPodNamespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("unmanaged-to-managed-"))
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "unmanaged-migrated"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.UnmanagedLoadBalancerDNS,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to reach stable conditions.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, append(availableConditionsForIngressControllerWithLoadBalancerUnmanagedDNS, operatorProgressingFalse)...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	if ingresscontroller.IsServiceInternal(lbService) {
		t.Fatalf("load balancer %s is internal but should be external", lbService.Name)
	}

	wildcardRecordName := controller.WildcardDNSRecordName(ic)
	wildcardRecord := &iov1.DNSRecord{}
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err != nil {
		t.Fatalf("failed to get wildcard dnsrecord %s: %v", wildcardRecordName, err)
	}

	if wildcardRecord.Spec.DNSManagementPolicy != iov1.UnmanagedDNS {
		t.Fatalf("DNSRecord %s expected in dnsManagementPolicy=UnmanagedDNS but got dnsManagementPolicy=%s", wildcardRecordName.Name, wildcardRecord.Spec.DNSManagementPolicy)
	}

	verifyUnmanagedDNSRecordStatus(t, wildcardRecord)

	testPodName := types.NamespacedName{Name: name.Name + "-initial", Namespace: testPodNamespace.Name}
	verifyExternalIngressController(t, testPodName, "apps."+ic.Spec.Domain, wildcardRecord.Spec.Targets[0])

	t.Logf("Updating ingresscontroller %s to dnsManagementPolicy=Managed", ic.Name)

	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.DNSManagementPolicy = operatorv1.ManagedLoadBalancerDNS
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller %s: %v", name, err)
	}

	t.Logf("Waiting for stable conditions on ingresscontroller %s after dnsManagementPolicy=Managed", ic.Name)

	// Wait for the load balancer and DNS to reach stable conditions.
	if err := waitForIngressControllerCondition(t, kclient, 10*time.Minute, name, append(availableConditionsForIngressControllerWithLoadBalancer, operatorProgressingFalse)...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure DNSRecord CR is present.
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err != nil {
		t.Fatalf("failed to get wildcard dnsrecord %s: %v", wildcardRecordName, err)
	}

	if wildcardRecord.Spec.DNSManagementPolicy != iov1.ManagedDNS {
		t.Fatalf("DNSRecord %s expected in dnsManagementPolicy=ManagedDNS but got dnsManagementPolicy=%s", wildcardRecordName.Name, wildcardRecord.Spec.DNSManagementPolicy)
	}

	if len(wildcardRecord.Status.Zones) == 0 {
		t.Fatalf("DNSRecord %s expected allocated dnsZones but found none", wildcardRecordName.Name)
	}

	testPodName = types.NamespacedName{Name: name.Name + "-final", Namespace: testPodNamespace.Name}
	verifyExternalIngressController(t, testPodName, "apps."+ic.Spec.Domain, wildcardRecord.Spec.Targets[0])
}

// TestManagedDNSToUnmanagedDNSIngressController tests dnsManagementPolicy during
// transitioning from Managed to Unmanaged DNS on an external ingress controller.
// The load balancer scope remains external throughout the transition, so only the
// DNS management policy changes.
func TestManagedDNSToUnmanagedDNSIngressController(t *testing.T) {
	t.Parallel()

	testPodNamespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("managed-to-unmanaged-"))
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "managed-migrated"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.ExternalLoadBalancer,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to reach stable conditions.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, append(availableConditionsForIngressControllerWithLoadBalancer, operatorProgressingFalse)...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	if ingresscontroller.IsServiceInternal(lbService) {
		t.Fatalf("load balancer %s is internal but should be external", lbService.Name)
	}

	wildcardRecordName := controller.WildcardDNSRecordName(ic)
	wildcardRecord := &iov1.DNSRecord{}
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err != nil {
		t.Fatalf("failed to get wildcard dnsrecord %s: %v", wildcardRecordName, err)
	}

	if wildcardRecord.Spec.DNSManagementPolicy != iov1.ManagedDNS {
		t.Fatalf("DNSRecord %s expected in dnsManagementPolicy=ManagedDNS but got dnsManagementPolicy=%s", wildcardRecordName.Name, wildcardRecord.Spec.DNSManagementPolicy)
	}

	testPodName := types.NamespacedName{Name: name.Name + "-initial", Namespace: testPodNamespace.Name}
	verifyExternalIngressController(t, testPodName, "apps."+ic.Spec.Domain, wildcardRecord.Spec.Targets[0])
	t.Logf("Updating ingresscontroller %s to dnsManagementPolicy=Unmanaged", ic.Name)

	// Updating the ingresscontroller's DNSManagementPolicy to Unmanaged, meaning
	// the DNSRecord CR associated with the controller will only be updated with
	// dnsManagementPolicy=Unmanaged and need not be deleted. The DNS records on the
	// cloud provider will continue to exist and must be manually deleted. (This is
	// outside the scope of the test.)
	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.DNSManagementPolicy = operatorv1.UnmanagedLoadBalancerDNS
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller %s: %v", name, err)
	}

	t.Logf("Waiting for stable conditions on ingresscontroller %s after dnsManagementPolicy=Unmanaged", ic.Name)

	// Wait for the load balancer and DNS to reach stable conditions.
	if err := waitForIngressControllerCondition(t, kclient, 10*time.Minute, name, append(availableConditionsForIngressControllerWithLoadBalancerUnmanagedDNS, operatorProgressingFalse)...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure DNSRecord CR is present.
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err != nil {
		t.Fatalf("failed to get wildcard dnsrecord %s: %v", wildcardRecordName, err)
	}

	if wildcardRecord.Spec.DNSManagementPolicy != iov1.UnmanagedDNS {
		t.Fatalf("DNSRecord %s expected in dnsManagementPolicy=UnmanagedDNS but got dnsManagementPolicy=%s", wildcardRecordName.Name, wildcardRecord.Spec.DNSManagementPolicy)
	}

	verifyUnmanagedDNSRecordStatus(t, wildcardRecord)

	// To verify the external ingresscontroller after updating dnsManagementPolicy=Unmanaged, we use
	// the IP address from the dnsrecord to route to the correct router pod, and we use the HTTP host
	// header to map to the correct route.  This means the old DNS records from when
	// dnsManagementPolicy=Managed was set are not used to verify the ingresscontroller (but they
	// will continue to exist unless they are manually deleted).
	testPodName = types.NamespacedName{Name: name.Name + "-final", Namespace: testPodNamespace.Name}
	verifyExternalIngressController(t, testPodName, "apps."+ic.Spec.Domain, wildcardRecord.Spec.Targets[0])
}

// TestUnmanagedDNSToManagedDNSInternalIngressController tests dnsManagementPolicy during
// transitioning from Unmanaged internal ingress controller to Managed external ingress
// controller. During the transition it deletes the load balancer so that the lb svc
// target changes and ensures the new updated target is published to the DNSZone.
func TestUnmanagedDNSToManagedDNSInternalIngressController(t *testing.T) {
	t.Parallel()

	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	platform := infraConfig.Status.PlatformStatus.Type

	supportedPlatforms := map[configv1.PlatformType]struct{}{
		configv1.AWSPlatformType:      {},
		configv1.AzurePlatformType:    {},
		configv1.GCPPlatformType:      {},
		configv1.IBMCloudPlatformType: {},
		configv1.PowerVSPlatformType:  {},
	}
	if _, supported := supportedPlatforms[platform]; !supported {
		t.Skipf("test skipped on platform %q", platform)
	}

	testPodNamespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("unmanaged-to-managed-internal-"))
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "unmanaged-migrated-internal"}
	ic := newLoadBalancerController(name, name.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.InternalLoadBalancer,
		DNSManagementPolicy: operatorv1.UnmanagedLoadBalancerDNS,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Wait for the load balancer and DNS to reach stable conditions.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, append(availableConditionsForIngressControllerWithLoadBalancerUnmanagedDNS, operatorProgressingFalse)...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	lbService := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get LoadBalancer service: %v", err)
	}

	if !ingresscontroller.IsServiceInternal(lbService) {
		t.Fatalf("load balancer %s is external but should be internal", lbService.Name)
	}

	wildcardRecordName := controller.WildcardDNSRecordName(ic)
	wildcardRecord := &iov1.DNSRecord{}
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err != nil {
		t.Fatalf("failed to get wildcard dnsrecord %s: %v", wildcardRecordName, err)
	}

	if wildcardRecord.Spec.DNSManagementPolicy != iov1.UnmanagedDNS {
		t.Fatalf("DNSRecord %s expected in dnsManagementPolicy=UnmanagedDNS but got dnsManagementPolicy=%s", wildcardRecordName.Name, wildcardRecord.Spec.DNSManagementPolicy)
	}

	verifyUnmanagedDNSRecordStatus(t, wildcardRecord)

	routerDeployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), routerDeployment); err != nil {
		t.Fatalf("failed to get router deployment: %v", err)
	}

	testPodName := types.NamespacedName{Name: name.Name, Namespace: testPodNamespace.Name}
	verifyInternalIngressController(t, testPodName, "apps."+ic.Spec.Domain, wildcardRecord.Spec.Targets[0], routerDeployment.Spec.Template.Spec.Containers[0].Image)

	t.Logf("Updating ingresscontroller %s to dnsManagementPolicy=Managed", ic.Name)

	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.Scope = operatorv1.ExternalLoadBalancer
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.DNSManagementPolicy = operatorv1.ManagedLoadBalancerDNS
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller %s: %v", name, err)
	}

	var oldLoadBalancerStatus corev1.LoadBalancerStatus
	lbService.Status.LoadBalancer.DeepCopyInto(&oldLoadBalancerStatus)

	// Only delete the service on platforms that don't automatically update the service's scope.
	switch platform {
	case configv1.AWSPlatformType, configv1.IBMCloudPlatformType, configv1.PowerVSPlatformType:
		if err := kclient.Delete(context.TODO(), lbService); err != nil && !errors.IsNotFound(err) {
			t.Fatalf("failed to delete svc %s: %v", lbService.Name, err)
		}
	}

	// Ensure the service's load-balancer status changes.
	lbService = &corev1.Service{}
	var lbAddress string
	err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			t.Logf("Get %q failed: %v, retrying ...", controller.LoadBalancerServiceName(ic), err)
			return false, nil
		}
		if reflect.DeepEqual(lbService.Status.LoadBalancer, oldLoadBalancerStatus) {
			t.Logf("Waiting for service %q to be updated", controller.LoadBalancerServiceName(ic))
			return false, nil
		} else if ingresscontroller.IsServiceInternal(lbService) {
			// The service got updated, but is not external.
			return true, fmt.Errorf("load balancer %s is internal but should be external", lbService.Name)
		}
		if len(lbService.Status.LoadBalancer.Ingress) == 0 {
			t.Logf("service %s has no load balancer ingress, retrying...", lbService.Name)
			return false, nil
		}
		lbAddress = lbService.Status.LoadBalancer.Ingress[0].IP
		if lbAddress == "" {
			lbAddress = lbService.Status.LoadBalancer.Ingress[0].Hostname
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("error updating the %q service: %v", controller.LoadBalancerServiceName(ic), err)
	}

	t.Logf("Waiting for stable conditions on ingresscontroller %s after dnsManagementPolicy=Managed", ic.Name)

	// Wait for the load balancer and DNS to reach stable conditions.
	if err := waitForIngressControllerCondition(t, kclient, 10*time.Minute, name, append(availableConditionsForIngressControllerWithLoadBalancer, operatorProgressingFalse)...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure DNSRecord CR is present.
	if err := kclient.Get(context.TODO(), wildcardRecordName, wildcardRecord); err != nil {
		t.Fatalf("failed to get wildcard dnsrecord %s: %v", wildcardRecordName, err)
	}

	if wildcardRecord.Spec.DNSManagementPolicy != iov1.ManagedDNS {
		t.Fatalf("DNSRecord %s expected in dnsManagementPolicy=ManagedDNS but got dnsManagementPolicy=%s", wildcardRecordName.Name, wildcardRecord.Spec.DNSManagementPolicy)
	}

	if len(wildcardRecord.Status.Zones) != 2 {
		t.Fatalf("DNSRecord %s expected allocated dnsZones but found none", wildcardRecordName.Name)
	}

	// Use lbAddress from the service instead of wildcardRecord.Spec.Targets[0] because when migrating
	// from internal to external scope, the IngressController may report DNSReady=True (causing
	// waitForIngressControllerCondition to pass) before the ingress-operator has reconciled the DNSRecord
	// with the new external load balancer address.
	testPodName = types.NamespacedName{Name: name.Name + "-final", Namespace: testPodNamespace.Name}
	verifyExternalIngressController(t, testPodName, "apps."+ic.Spec.Domain, lbAddress)
}

func verifyUnmanagedDNSRecordStatus(t *testing.T, record *iov1.DNSRecord) {
	t.Helper()
	for _, zoneInStatus := range record.Status.Zones {
		if len(zoneInStatus.Conditions) == 0 {
			t.Fatalf("DNSRecord zone %+v expected to have conditions", zoneInStatus.DNSZone)
		}

		t.Logf("verifying conditions on DNSRecord zone %+v", zoneInStatus.DNSZone)
		for _, condition := range zoneInStatus.Conditions {
			if condition.Type != iov1.DNSRecordPublishedConditionType {
				t.Fatalf("DNSRecord zone expected to have condition type=Published but got type=%s", condition.Type)
			}

			if condition.Status != string(operatorv1.ConditionUnknown) {
				t.Fatalf("DNSRecord zone expected to have status=Unknown but got status=%s", condition.Status)
			}
		}
	}
}
