//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	machinev1 "github.com/openshift/api/machine/v1beta1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	v12 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	awsLBSubnetAnnotation = "service.beta.kubernetes.io/aws-load-balancer-subnets"
)

// TestAWSLBSubnets creates an IngressController with both the public and private cluster subnets for its LB-type
// Service in AWS. The test verifies the provisioning of the LB-type Service, confirms ingress connectivity, and
// confirms that the service.beta.kubernetes.io/aws-load-balancer-subnets as well as the IngressController status is
// correctly configured.
func TestAWSLBSubnets(t *testing.T) {
	t.Parallel()

	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}
	if enabled, err := isFeatureGateEnabled(features.FeatureGateIngressControllerLBSubnetsAWS); err != nil {
		t.Fatalf("failed to get feature gate: %v", err)
	} else if !enabled {
		t.Skipf("test skipped because %q feature gate is not enabled", features.FeatureGateIngressControllerLBSubnetsAWS)
	}

	// First, let's get the list of public subnets to use for the LB.
	// Unfortunately these subnets aren't stored in any K8S API objects. But by using the installer-created subnet
	// naming convention, we can generate the names of the public subnets.
	// Subnet Naming Convention: <clusterName>-[public|private]-<availabilityZone>
	machineSets := &machinev1.MachineSetList{}
	listOpts := []client.ListOption{
		client.InNamespace("openshift-machine-api"),
	}
	if err := kclient.List(context.TODO(), machineSets, listOpts...); err != nil {
		t.Fatalf("failed to get machineSets: %v", err)
	}
	// The availability zones can be derived from the providerSpec on MachineSet object.
	publicSubnets := &operatorv1.AWSSubnets{}
	privateSubnets := &operatorv1.AWSSubnets{}
	for _, machineset := range machineSets.Items {
		providerSpec := new(machinev1.AWSMachineProviderConfig)
		if err := unmarshalInto(&machineset, providerSpec); err != nil {
			t.Fatalf("failure to unmarshal machineset %q provider spec: %v", machineset.Name, err)
		}

		if len(providerSpec.Placement.AvailabilityZone) == 0 {
			t.Fatalf("machineset %q availability zone is empty", machineset.Name)
		}
		// Build the public and private subnet name according to the installer naming convention.
		publicSubnet := infraConfig.Status.InfrastructureName + "-public-" + providerSpec.Placement.AvailabilityZone
		publicSubnets.Names = append(publicSubnets.Names, operatorv1.AWSSubnetName(publicSubnet))
		privateSubnet := infraConfig.Status.InfrastructureName + "-private-" + providerSpec.Placement.AvailabilityZone
		privateSubnets.Names = append(privateSubnets.Names, operatorv1.AWSSubnetName(privateSubnet))
	}
	t.Logf("discovered the following public subnets: %q", publicSubnets.Names)
	t.Logf("discovered the following private subnets: %q", privateSubnets.Names)

	// First create the ingress controller with no subnets.
	t.Logf("Creating ingresscontroller without subnets specified")
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "aws-subnets"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(name, domain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("expected ingresscontroller creation failed: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the Service annotation is unset by default.
	lbService := &v12.Service{}
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get lb service: %v", err)
	}
	if val, ok := lbService.Annotations[awsLBSubnetAnnotation]; ok {
		t.Fatalf("unexpected %q annotation: %q", awsLBSubnetAnnotation, val)
	}

	// Make sure a vanilla IngressController has connectivity.
	externalTestPodName := types.NamespacedName{Name: name.Name + "-external-verify", Namespace: name.Namespace}
	testHostname := "apps." + ic.Spec.Domain
	t.Logf("verifying vanilla ingress controller %q has external connectivity", ic.Name)
	if err := verifyExternalIngressController(t, externalTestPodName, testHostname, testHostname, 10*time.Minute); err != nil {
		t.Fatalf("failed to verify connectivity with workload with hostname %s using external client: %v", testHostname, err)
	}

	// Update the IngressController to add the public subnets we discovered.
	icName := types.NamespacedName{Name: ic.Name, Namespace: ic.Namespace}
	t.Logf("updating ingress controller %q to use public subnets", ic.Name)
	if err := updateIngressControllerWithRetryOnConflict(t, icName, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Subnets: publicSubnets,
				Type:    operatorv1.AWSClassicLoadBalancer,
			},
		}
	}); err != nil {
		t.Fatalf("failed to update ingress controller: %v", err)
	}

	// Wait for LoadBalancerProgressing=True due to IngressController subnet update.
	progressingTrue := operatorv1.OperatorCondition{
		Type:   ingress.IngressControllerLoadBalancerProgressingConditionType,
		Status: operatorv1.ConditionTrue,
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, progressingTrue); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	var oldLoadBalancerStatus v12.LoadBalancerStatus
	lbService.Status.LoadBalancer.DeepCopyInto(&oldLoadBalancerStatus)

	// Delete the LB-type Service to effectuate the subnet update.
	t.Logf("deleting the service to effectuate the subnets: %s/%s", lbService.Namespace, lbService.Name)
	if err := kclient.Delete(context.Background(), lbService); err != nil {
		t.Fatalf("failed to delete lb service: %v", err)
	}

	// Ensure the service's load-balancer status changes, and verify we get the expected subnet annotation on the service.
	waitForLBSubnetAnnotation(t, ic, publicSubnets, oldLoadBalancerStatus)

	// Since we are using valid public subnets from the nodes, we expect the ingress controller
	// to provision the load balancer successfully.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the subnets status field is configured to what we expect.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	subnetSpec := ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.Subnets
	subnetStatus := ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.Subnets
	if !reflect.DeepEqual(subnetSpec, subnetStatus) {
		t.Fatalf("expected subnet status to be %q got %q", subnetSpec, subnetStatus)
	}

	// Verify we can still reach the LoadBalancer with the provided public subnets.
	t.Logf("verifying ingress controller %q with specified public subnets has external connectivity", ic.Name)
	if err := verifyExternalIngressController(t, externalTestPodName, testHostname, testHostname, 10*time.Minute); err != nil {
		t.Fatalf("failed to verify connectivity with workload with hostname %s using external client: %v", testHostname, err)
	}

	// Now, update the IngressController to change to use the private subnets.
	t.Logf("updating ingress controller %q to use private subnets as well as auto-delete annotation", ic.Name)
	if err := updateIngressControllerWithRetryOnConflict(t, icName, 5*time.Minute, func(ic *operatorv1.IngressController) {
		if ic.Annotations == nil {
			ic.Annotations = map[string]string{}
		}
		ic.Annotations["ingress.operator.openshift.io/auto-delete-load-balancer"] = ""
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Subnets: privateSubnets,
				Type:    operatorv1.AWSClassicLoadBalancer,
			},
		}
	}); err != nil {
		t.Fatalf("failed to update ingress controller: %v", err)
	}

	// Update oldLoadBalancerStatus again, so we can use it in waitForLBSubnetAnnotation.
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get lb service: %v", err)
	}
	lbService.Status.LoadBalancer.DeepCopyInto(&oldLoadBalancerStatus)

	// Delete the LB-type Service to effectuate the subnet update.
	t.Logf("deleting the service to effectuate the subnets: %s/%s", lbService.Namespace, lbService.Name)
	if err := kclient.Delete(context.Background(), lbService); err != nil {
		t.Fatalf("failed to delete lb service: %v", err)
	}

	// Ensure the service's load-balancer status changes, and verify we get the expected subnet annotation on the service.
	waitForLBSubnetAnnotation(t, ic, privateSubnets, oldLoadBalancerStatus)

	// Verify the ingress controller does NOT connect externally.
	// Note that we can't verify internal connectivity because the DNS name provides an external VIP. So the ingress
	// controller is essentially broken.
	t.Logf("verifying ingress controller %q with private subnets does NOT have external connectivity", ic.Name)
	if err := verifyExternalIngressController(t, externalTestPodName, testHostname, testHostname, 3*time.Minute); !wait.Interrupted(err) {
		t.Fatalf("unexpectedly verified connectivity with workload with hostname %s using external client: %v", testHostname, err)
	}

	// Update oldLoadBalancerStatus again, so we can use it later in waitForLBSubnetAnnotation.
	if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
		t.Fatalf("failed to get lb service: %v", err)
	}
	lbService.Status.LoadBalancer.DeepCopyInto(&oldLoadBalancerStatus)

	// Now, update the IngressController to change to remove the subnets, but let's use the
	// auto-delete-load-balancer annotation, so we don't have to manually delete the service.
	t.Logf("updating ingress controller %q to remove the subnets while using the auto-delete-load-balancer annotation", ic.Name)
	if err := updateIngressControllerWithRetryOnConflict(t, icName, 5*time.Minute, func(ic *operatorv1.IngressController) {
		if ic.Annotations == nil {
			ic.Annotations = map[string]string{}
		}
		ic.Annotations["ingress.operator.openshift.io/auto-delete-load-balancer"] = ""
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
		}
	}); err != nil {
		t.Fatalf("failed to update ingress controller: %v", err)
	}

	// Ensure the service's load-balancer status changes, and verify the subnet annotation is removed on the service.
	waitForLBSubnetAnnotation(t, ic, nil, oldLoadBalancerStatus)
}

// waitForLBSubnetAnnotation waits for the provided subnets to appear on the load balancer service for the ingress
// controller. It will return an error if they don't get updated.
func waitForLBSubnetAnnotation(t *testing.T, ic *operatorv1.IngressController, subnets *operatorv1.AWSSubnets, oldLoadBalancerStatus v12.LoadBalancerStatus) error {
	lbService := &v12.Service{}
	expectedSubnetAnnotation := ingress.JoinAWSSubnets(subnets, ",")
	t.Logf("waiting for %q service to have subnet annotation of %q", controller.LoadBalancerServiceName(ic), expectedSubnetAnnotation)
	err := wait.PollUntilContextTimeout(context.TODO(), 10*time.Second, 5*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, controller.LoadBalancerServiceName(ic), lbService); err != nil {
			t.Logf("Get %q failed: %v, retrying ...", controller.LoadBalancerServiceName(ic), err)
			return false, nil
		}
		if reflect.DeepEqual(lbService.Status.LoadBalancer, oldLoadBalancerStatus) {
			t.Logf("Waiting for service %q to be updated", controller.LoadBalancerServiceName(ic))
			return false, nil
		}
		val, ok := lbService.Annotations[awsLBSubnetAnnotation]
		// Handle the case where subnets are expected to be nil (annotation should not exist).
		if subnets == nil {
			if ok {
				return true, fmt.Errorf("expected %q annotation to be removed got %q", awsLBSubnetAnnotation, val)
			}
			return true, nil
		}
		// Handle the case where subnets are expected (annotation should exist and match).
		if !ok || val != expectedSubnetAnnotation {
			return true, fmt.Errorf("expected %q annotation %q got %q", awsLBSubnetAnnotation, expectedSubnetAnnotation, val)
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("error updating the %q service: %v", controller.LoadBalancerServiceName(ic), err)
	}
	return nil
}

// unmarshalInto unmarshals a MachineSet's ProviderSpec into the provided providerSpec parameter.
func unmarshalInto(m *machinev1.MachineSet, providerSpec interface{}) *field.Error {
	if m.Spec.Template.Spec.ProviderSpec.Value == nil {
		return field.Required(field.NewPath("providerSpec", "value"), "a value must be provided")
	}

	if err := json.Unmarshal(m.Spec.Template.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		return field.Invalid(field.NewPath("providerSpec", "value"), providerSpec, err.Error())
	}
	return nil
}
