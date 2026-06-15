//go:build e2e
// +build e2e

package e2e

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"
)

func TestGatewayAPIManagementMode(t *testing.T) {
	requireGatewayAPIManagementMode(t)
	defer restoreGatewayAPIManagementModeManaged(t)

	t.Run("testManagementMode_defaultManaged", testManagementMode_defaultManaged)
	t.Run("testManagementMode_knobDisableEnableCycle", testManagementMode_knobDisableEnableCycle)
	t.Run("testManagementMode_transitionToUnmanaged", testManagementMode_transitionToUnmanaged)
	t.Run("testManagementMode_returnToManaged_compliant", testManagementMode_returnToManaged_compliant)
	t.Run("testManagementMode_returnToManaged_nonCompliant", testManagementMode_returnToManaged_nonCompliant)
	t.Run("testManagementMode_returnToManaged_wrongChannel", testManagementMode_returnToManaged_wrongChannel)
	t.Run("testManagementMode_complianceReassessOnCRDChange", testManagementMode_complianceReassessOnCRDChange)
	t.Run("testManagementMode_vapAbsentUnmanaged", testManagementMode_vapAbsentUnmanaged)
}

func testManagementMode_defaultManaged(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsManaged, operatorv1.ConditionTrue, managementmode.ReasonManagedByCIO)
	assertGatewayAPIVAPPresent(t, true)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsManaged, operatorv1.ConditionFalse, managementmode.ReasonUnmanaged)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsManaged, operatorv1.ConditionTrue, managementmode.ReasonManagedByCIO)
}

func testManagementMode_knobDisableEnableCycle(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	before := snapshotGatewayAPIResources(t)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	assertGatewayAPIVAPPresent(t, false)
	assertIstioControlPlaneRunning(t, false)
	assertGatewayAPIResourcesPreserved(t, before)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsCompliant, operatorv1.ConditionTrue, managementmode.ReasonVersionMatch)
	assertGatewayAPIVAPPresent(t, true)
	assertIstioControlPlaneRunning(t, true)
	assertGatewayAPIResourcesPreserved(t, before)
}

func testManagementMode_transitionToUnmanaged(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	before := snapshotGatewayAPIResources(t)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	assertGatewayAPIVAPPresent(t, false)
	assertIstioControlPlaneRunning(t, false)
	assertGatewayAPIResourcesPreserved(t, before)
}

func testManagementMode_returnToManaged_compliant(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsManaged, operatorv1.ConditionTrue, managementmode.ReasonManagedByCIO)
	assertGatewayAPIVAPPresent(t, true)
}

func testManagementMode_returnToManaged_nonCompliant(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	const crdName = "gatewayclasses.gateway.networking.k8s.io"
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	patchCRDAnnotation(t, crdName, managementmode.AnnotationBundleVersion, "v0.0.1")
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsCompliant, operatorv1.ConditionFalse, managementmode.ReasonVersionMismatch)
	patchCRDAnnotation(t, crdName, managementmode.AnnotationBundleVersion, managementmode.SupportedProfile().BundleVersion)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsCompliant, operatorv1.ConditionTrue, managementmode.ReasonVersionMatch)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
}

func testManagementMode_returnToManaged_wrongChannel(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	const crdName = "gatewayclasses.gateway.networking.k8s.io"
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	patchCRDAnnotation(t, crdName, managementmode.AnnotationChannel, "experimental")
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsCompliant, operatorv1.ConditionFalse, managementmode.ReasonVersionMismatch)
	patchCRDAnnotation(t, crdName, managementmode.AnnotationChannel, managementmode.SupportedProfile().Channel)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsCompliant, operatorv1.ConditionTrue, managementmode.ReasonVersionMatch)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
}

func testManagementMode_complianceReassessOnCRDChange(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	const crdName = "gatewayclasses.gateway.networking.k8s.io"
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	patchCRDAnnotation(t, crdName, managementmode.AnnotationBundleVersion, "v0.0.1")
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsCompliant, operatorv1.ConditionFalse, managementmode.ReasonVersionMismatch)
	patchCRDAnnotation(t, crdName, managementmode.AnnotationBundleVersion, managementmode.SupportedProfile().BundleVersion)
	waitForIngressOperatorConfigCondition(t, managementmode.ConditionGatewayAPICRDsCompliant, operatorv1.ConditionTrue, managementmode.ReasonVersionMatch)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
}

func testManagementMode_vapAbsentUnmanaged(t *testing.T) {
	defer restoreGatewayAPIManagementModeManaged(t)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
	assertGatewayAPIVAPPresent(t, false)
	setGatewayAPIManagementMode(t, operatorv1alpha1.GatewayAPIManagementModeManaged)
	assertGatewayAPIVAPPresent(t, true)
}
