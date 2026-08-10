//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	condutils "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// bundleVersionAnnotation is the annotation key used for CRD compliance checking
	bundleVersionAnnotation = "gateway.networking.k8s.io/bundle-version"

	// ingressCRName is the singleton Ingress CR name
	ingressCRName = "cluster"
)

// testGatewayAPIManagementModeDefault verifies the default Managed state:
// - Ingress CR exists with mode=Managed
// - CRDs are installed with bundle-version annotation
// - VAP is installed
// - Istio/Sail is running
// - Status conditions: Managed/Present/Compliant all True
func testGatewayAPIManagementModeDefault(t *testing.T) {
	t.Log("Verifying Ingress CR exists with Managed mode")
	ingress := &operatorv1alpha1.Ingress{}
	ingressName := types.NamespacedName{Name: ingressCRName}

	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}
		return true
	}, 1*time.Minute, 2*time.Second,
		"Expected Ingress CR %s to exist", ingressCRName)

	// Verify mode is Managed (or empty, which defaults to Managed)
	mode := ingress.Spec.GatewayAPI.ManagementMode
	if mode == "" {
		mode = operatorv1alpha1.GatewayAPIManagementModeManaged
	}
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeManaged, mode,
		"Expected Ingress CR to have Managed mode by default")

	t.Log("Verifying all Gateway API CRDs are installed")
	ensureCRDs(t)

	t.Log("Verifying CRDs have bundle-version annotation")
	for _, crdName := range crdNames {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		name := types.NamespacedName{Name: crdName}

		require.Eventually(t, func() bool {
			if err := kclient.Get(context.Background(), name, crd); err != nil {
				t.Logf("Failed to get CRD %s: %v", crdName, err)
				return false
			}

			bundleVersion, found := crd.Annotations[bundleVersionAnnotation]
			if !found || bundleVersion == "" {
				t.Logf("CRD %s missing bundle-version annotation", crdName)
				return false
			}
			return true
		}, 1*time.Minute, 2*time.Second,
			"Expected CRD %s to have bundle-version annotation", crdName)
	}

	t.Log("Verifying VAP is installed")
	if err := assertVAP(t, gwapiCRDVAPName); err != nil {
		t.Fatalf("VAP %s not found: %v", gwapiCRDVAPName, err)
	}

	t.Log("Verifying Istio control plane is running")
	if err := assertIstiodControlPlane(t); err != nil {
		t.Fatalf("Istiod control plane not running: %v", err)
	}

	t.Log("Verifying Ingress status conditions")
	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		managedCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsManaged")
		presentCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsPresent")
		compliantCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsCompliant")

		if managedCond == nil || managedCond.Status != metav1.ConditionTrue {
			t.Logf("GatewayAPICRDsManaged not True: %+v", managedCond)
			return false
		}
		if presentCond == nil || presentCond.Status != metav1.ConditionTrue {
			t.Logf("GatewayAPICRDsPresent not True: %+v", presentCond)
			return false
		}
		if compliantCond == nil || compliantCond.Status != metav1.ConditionTrue {
			t.Logf("GatewayAPICRDsCompliant not True: %+v", compliantCond)
			return false
		}

		t.Logf("All conditions True: Managed=%s, Present=%s, Compliant=%s",
			managedCond.Reason, presentCond.Reason, compliantCond.Reason)
		return true
	}, 2*time.Minute, 5*time.Second,
		"Expected Managed/Present/Compliant conditions to be True")
}

// testGatewayAPIManagementModeMetrics verifies the management mode metrics:
// - ingress_controller_gateway_api_management_mode{mode="Managed"} == 1
// - ingress_controller_gateway_api_management_mode{mode="Unmanaged"} == 0
// - ingress_controller_gateway_api_info{gateway_api_version,ossm_version} == 1
func testGatewayAPIManagementModeMetrics(t *testing.T) {
	prometheusClient := createPrometheusClient(t)

	t.Log("Verifying management_mode metric shows Managed=1 and Unmanaged=0")
	managedQuery := `ingress_controller_gateway_api_management_mode{mode="Managed"}`
	unmanagedQuery := `ingress_controller_gateway_api_management_mode{mode="Unmanaged"}`

	assertMetricValue(t, prometheusClient, managedQuery, 1,
		"Expected ingress_controller_gateway_api_management_mode{mode=\"Managed\"}=1 in default state")
	assertMetricValue(t, prometheusClient, unmanagedQuery, 0,
		"Expected ingress_controller_gateway_api_management_mode{mode=\"Unmanaged\"}=0 in default state")

	t.Log("Verifying gateway_api_info metric is present with version labels")
	infoQuery := `ingress_controller_gateway_api_info`

	assert.Eventually(t, func() bool {
		result, _, err := prometheusClient.Query(t.Context(), infoQuery, time.Now())
		if err != nil {
			t.Logf("Failed to query Prometheus for info metric: %v", err)
			return false
		}
		vector, ok := result.(model.Vector)
		if !ok {
			t.Logf("Unexpected result type for info metric: %T", result)
			return false
		}
		if len(vector) == 0 {
			t.Logf("Info metric not yet available")
			return false
		}

		// Verify the metric has the expected labels
		metric := vector[0].Metric
		gatewayAPIVersion, hasGatewayAPIVersion := metric["gateway_api_version"]
		ossmVersion, hasOSSMVersion := metric["ossm_version"]

		if !hasGatewayAPIVersion || string(gatewayAPIVersion) == "" {
			t.Logf("Info metric missing or empty gateway_api_version label")
			return false
		}
		if !hasOSSMVersion || string(ossmVersion) == "" {
			t.Logf("Info metric missing or empty ossm_version label")
			return false
		}

		// Verify metric value is 1 (info-style metric)
		if float64(vector[0].Value) != 1 {
			t.Logf("Info metric has unexpected value: %v", vector[0].Value)
			return false
		}

		t.Logf("Info metric present with gateway_api_version=%s, ossm_version=%s",
			gatewayAPIVersion, ossmVersion)
		return true
	}, 2*time.Minute, 5*time.Second,
		"Expected ingress_controller_gateway_api_info metric with version labels")
}

// testGatewayAPIManagementModeCRDCompliance verifies CRD compliance checking:
// - Modifying a CRD's bundle-version annotation causes Compliant=False
// - Restoring the annotation causes Compliant=True
func testGatewayAPIManagementModeCRDCompliance(t *testing.T) {
	// Pick the first CRD to test with
	testCRDName := crdNames[0]
	t.Logf("Testing CRD compliance with %s", testCRDName)

	ingress := &operatorv1alpha1.Ingress{}
	ingressName := types.NamespacedName{Name: ingressCRName}

	// Get the original bundle-version
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crdName := types.NamespacedName{Name: testCRDName}

	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), crdName, crd); err != nil {
			t.Logf("Failed to get CRD: %v", err)
			return false
		}
		return true
	}, 30*time.Second, 2*time.Second, "Failed to get CRD %s", testCRDName)

	originalBundleVersion, found := crd.Annotations[bundleVersionAnnotation]
	require.True(t, found, "CRD should have bundle-version annotation")
	t.Logf("Original bundle-version: %s", originalBundleVersion)

	// Bypass VAP to modify the CRD
	bypassVAP(t, func(t *testing.T) {
		t.Log("Modifying CRD bundle-version annotation to trigger non-compliance")

		require.Eventually(t, func() bool {
			// Re-fetch to get latest resourceVersion
			if err := kclient.Get(context.Background(), crdName, crd); err != nil {
				t.Logf("Failed to get CRD: %v", err)
				return false
			}

			crd.Annotations[bundleVersionAnnotation] = "v0.0.0-test-mismatch"

			if err := kclient.Update(context.Background(), crd); err != nil {
				t.Logf("Failed to update CRD: %v; retrying...", err)
				return false
			}
			t.Logf("Successfully modified CRD bundle-version to trigger mismatch")
			return true
		}, 30*time.Second, 2*time.Second, "Failed to modify CRD")
	})

	// Verify Compliant condition becomes False
	t.Log("Waiting for Compliant condition to become False")
	assert.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		compliantCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsCompliant")
		if compliantCond == nil {
			t.Logf("Compliant condition not found yet")
			return false
		}

		if compliantCond.Status != metav1.ConditionFalse {
			t.Logf("Compliant condition not False yet: %+v", compliantCond)
			return false
		}

		if !strings.Contains(compliantCond.Message, "bundle-version annotation mismatch") {
			t.Logf("Compliant message doesn't mention bundle-version mismatch: %s", compliantCond.Message)
			return false
		}

		t.Logf("Compliant=False with message: %s", compliantCond.Message)
		return true
	}, 2*time.Minute, 5*time.Second,
		"Expected Compliant condition to become False after bundle-version mismatch")

	// Restore the original bundle-version
	bypassVAP(t, func(t *testing.T) {
		t.Log("Restoring original CRD bundle-version annotation")

		require.Eventually(t, func() bool {
			// Re-fetch to get latest resourceVersion
			if err := kclient.Get(context.Background(), crdName, crd); err != nil {
				t.Logf("Failed to get CRD: %v", err)
				return false
			}

			crd.Annotations[bundleVersionAnnotation] = originalBundleVersion

			if err := kclient.Update(context.Background(), crd); err != nil {
				t.Logf("Failed to restore CRD: %v; retrying...", err)
				return false
			}
			t.Logf("Successfully restored CRD bundle-version")
			return true
		}, 30*time.Second, 2*time.Second, "Failed to restore CRD")
	})

	// Verify Compliant condition becomes True again
	t.Log("Waiting for Compliant condition to become True")
	assert.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		compliantCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsCompliant")
		if compliantCond == nil || compliantCond.Status != metav1.ConditionTrue {
			t.Logf("Compliant condition not True yet: %+v", compliantCond)
			return false
		}

		t.Logf("Compliant=True restored")
		return true
	}, 2*time.Minute, 5*time.Second,
		"Expected Compliant condition to become True after restoring bundle-version")
}

// testGatewayAPIManagementModeUnmanaged verifies transitioning to Unmanaged mode:
// - Set mode=Unmanaged in Ingress CR
// - Istio/Sail is stopped
// - VAP is deleted
// - CRDs, GatewayClass, and Gateways are preserved
// - Managed=False with reason "Unmanaged"
// - Can modify Gateway API CRDs (no VAP protection)
func testGatewayAPIManagementModeUnmanaged(t *testing.T) {
	ingress := &operatorv1alpha1.Ingress{}
	ingressName := types.NamespacedName{Name: ingressCRName}

	t.Log("Transitioning to Unmanaged mode")
	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		ingress.Spec.GatewayAPI.ManagementMode = operatorv1alpha1.GatewayAPIManagementModeUnmanaged

		if err := kclient.Update(context.Background(), ingress); err != nil {
			t.Logf("Failed to update Ingress CR to Unmanaged: %v; retrying...", err)
			return false
		}
		t.Log("Successfully set Ingress CR to Unmanaged mode")
		return true
	}, 30*time.Second, 2*time.Second,
		"Failed to set Ingress CR to Unmanaged mode")

	// Verify Managed condition becomes False with reason Unmanaged
	t.Log("Waiting for Managed condition to become False")
	assert.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		managedCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsManaged")
		if managedCond == nil {
			t.Logf("Managed condition not found yet")
			return false
		}

		if managedCond.Status != metav1.ConditionFalse || managedCond.Reason != "Unmanaged" {
			t.Logf("Managed condition not False/Unmanaged yet: %+v", managedCond)
			return false
		}

		t.Logf("Managed=False with reason Unmanaged")
		return true
	}, 2*time.Minute, 5*time.Second,
		"Expected Managed condition to become False with reason Unmanaged")

	// Verify VAP is deleted
	t.Log("Verifying VAP is deleted")
	assert.Eventually(t, func() bool {
		vap := &admissionregistrationv1.ValidatingAdmissionPolicy{}
		vapName := types.NamespacedName{Name: gwapiCRDVAPName}

		err := kclient.Get(context.Background(), vapName, vap)
		if err != nil && errors.IsNotFound(err) {
			t.Log("VAP successfully deleted")
			return true
		}
		if err != nil {
			t.Logf("Error checking VAP: %v", err)
			return false
		}
		t.Logf("VAP still exists, waiting for deletion...")
		return false
	}, 2*time.Minute, 5*time.Second,
		"Expected VAP to be deleted in Unmanaged mode")

	// Verify Istio is stopped
	t.Log("Verifying Istio control plane is stopped")
	assert.Eventually(t, func() bool {
		if err := assertIstiodControlPlaneRemoved(t); err == nil {
			t.Log("Istiod successfully removed")
			return true
		} else {
			t.Logf("Istiod still running: %v", err)
			return false
		}
	}, 5*time.Minute, 10*time.Second,
		"Expected Istiod to be stopped in Unmanaged mode")

	// Verify CRDs still exist
	t.Log("Verifying Gateway API CRDs are still present")
	ensureCRDs(t)

	// Verify GatewayClass still exists
	t.Log("Verifying GatewayClass is still present")
	gwc := &gatewayapiv1.GatewayClass{}
	gwcName := types.NamespacedName{Name: "openshift-default"}
	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), gwcName, gwc); err != nil {
			t.Logf("GatewayClass not found: %v", err)
			return false
		}
		t.Log("GatewayClass still exists")
		return true
	}, 30*time.Second, 2*time.Second,
		"Expected GatewayClass to still exist in Unmanaged mode")

	// Verify we can modify a Gateway API CRD (no VAP protection)
	t.Log("Verifying CRDs can be modified without VAP protection")
	testCRDName := crdNames[0]
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crdName := types.NamespacedName{Name: testCRDName}

	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), crdName, crd); err != nil {
			t.Logf("Failed to get CRD: %v", err)
			return false
		}

		// Add a test annotation
		if crd.Annotations == nil {
			crd.Annotations = make(map[string]string)
		}
		crd.Annotations["test.openshift.io/unmanaged"] = "true"

		if err := kclient.Update(context.Background(), crd); err != nil {
			t.Logf("Failed to modify CRD: %v; retrying...", err)
			return false
		}
		t.Log("Successfully modified CRD (no VAP protection in Unmanaged mode)")
		return true
	}, 30*time.Second, 2*time.Second,
		"Should be able to modify CRD in Unmanaged mode")

	// Verify metrics reflect Unmanaged state
	t.Log("Verifying metrics show Unmanaged=1")
	prometheusClient := createPrometheusClient(t)
	unmanagedQuery := `ingress_controller_gateway_api_management_mode{mode="Unmanaged"}`
	assertMetricValue(t, prometheusClient, unmanagedQuery, 1,
		"Expected ingress_controller_gateway_api_management_mode{mode=\"Unmanaged\"}=1")

	// Info metric should be gone in Unmanaged mode
	t.Log("Verifying info metric is removed in Unmanaged mode")
	infoQuery := `ingress_controller_gateway_api_info`
	assertMetricGone(t, prometheusClient, infoQuery,
		"Expected ingress_controller_gateway_api_info metric to be removed in Unmanaged mode")

	// Transition back to Managed for subsequent tests
	t.Cleanup(func() {
		t.Log("Cleanup: Transitioning back to Managed mode")
		require.Eventually(t, func() bool {
			if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
				t.Logf("Failed to get Ingress CR: %v", err)
				return false
			}

			ingress.Spec.GatewayAPI.ManagementMode = operatorv1alpha1.GatewayAPIManagementModeManaged

			if err := kclient.Update(context.Background(), ingress); err != nil {
				t.Logf("Failed to restore Managed mode: %v; retrying...", err)
				return false
			}
			return true
		}, 30*time.Second, 2*time.Second,
			"Failed to restore Managed mode in cleanup")

		// Wait for Managed condition to be True
		assert.Eventually(t, func() bool {
			if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
				return false
			}
			managedCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsManaged")
			return managedCond != nil && managedCond.Status == metav1.ConditionTrue
		}, 3*time.Minute, 5*time.Second, "Failed to restore Managed state in cleanup")

		t.Log("Cleanup complete: Restored Managed mode")
	})
}

// testGatewayAPIManagementModeTakeover verifies takeover behavior:
// - In Unmanaged mode with non-compliant CRDs, switching to Managed is blocked
// - Managed=False with reason "TakeoverBlocked"
// - Restoring CRD compliance allows takeover
func testGatewayAPIManagementModeTakeover(t *testing.T) {
	ingress := &operatorv1alpha1.Ingress{}
	ingressName := types.NamespacedName{Name: ingressCRName}

	// Ensure we're in Unmanaged mode first
	t.Log("Ensuring Unmanaged mode for takeover test")
	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		if ingress.Spec.GatewayAPI.ManagementMode != operatorv1alpha1.GatewayAPIManagementModeUnmanaged {
			ingress.Spec.GatewayAPI.ManagementMode = operatorv1alpha1.GatewayAPIManagementModeUnmanaged
			if err := kclient.Update(context.Background(), ingress); err != nil {
				t.Logf("Failed to set Unmanaged: %v; retrying...", err)
				return false
			}
		}

		managedCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsManaged")
		return managedCond != nil && managedCond.Status == metav1.ConditionFalse
	}, 2*time.Minute, 5*time.Second,
		"Failed to ensure Unmanaged mode")

	// Modify a CRD to make it non-compliant
	testCRDName := crdNames[0]
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crdName := types.NamespacedName{Name: testCRDName}

	t.Logf("Making CRD %s non-compliant", testCRDName)
	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), crdName, crd); err != nil {
			t.Logf("Failed to get CRD: %v", err)
			return false
		}

		// Save original for restoration
		if crd.Annotations[bundleVersionAnnotation] == "" {
			t.Logf("CRD missing bundle-version annotation")
			return false
		}

		crd.Annotations[bundleVersionAnnotation] = "v0.0.0-takeover-blocked"

		if err := kclient.Update(context.Background(), crd); err != nil {
			t.Logf("Failed to make CRD non-compliant: %v; retrying...", err)
			return false
		}
		t.Log("Successfully made CRD non-compliant")
		return true
	}, 30*time.Second, 2*time.Second,
		"Failed to make CRD non-compliant")

	// Try to switch to Managed mode
	t.Log("Attempting to switch to Managed mode (should be blocked)")
	require.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		ingress.Spec.GatewayAPI.ManagementMode = operatorv1alpha1.GatewayAPIManagementModeManaged

		if err := kclient.Update(context.Background(), ingress); err != nil {
			t.Logf("Failed to set Managed mode: %v; retrying...", err)
			return false
		}
		t.Log("Set desired mode to Managed")
		return true
	}, 30*time.Second, 2*time.Second,
		"Failed to set desired mode to Managed")

	// Verify takeover is blocked
	t.Log("Verifying takeover is blocked (Managed=False, reason=TakeoverBlocked)")
	assert.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		managedCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsManaged")
		compliantCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsCompliant")

		if managedCond == nil {
			t.Logf("Managed condition not found")
			return false
		}

		if managedCond.Status != metav1.ConditionFalse || managedCond.Reason != "TakeoverBlocked" {
			t.Logf("Managed condition not TakeoverBlocked yet: %+v", managedCond)
			return false
		}

		if compliantCond == nil || compliantCond.Status != metav1.ConditionFalse {
			t.Logf("Compliant condition not False: %+v", compliantCond)
			return false
		}

		t.Logf("Takeover blocked: Managed=%s/%s, Compliant=%s",
			managedCond.Status, managedCond.Reason, compliantCond.Status)
		return true
	}, 2*time.Minute, 5*time.Second,
		"Expected takeover to be blocked with non-compliant CRDs")

	// Restore CRD compliance by deleting and letting CIO recreate it
	t.Log("Deleting non-compliant CRD to allow CIO to recreate it")
	require.Eventually(t, func() bool {
		if err := kclient.Delete(context.Background(), crd); err != nil {
			if !errors.IsNotFound(err) {
				t.Logf("Failed to delete CRD: %v; retrying...", err)
				return false
			}
		}
		t.Log("CRD deleted")
		return true
	}, 30*time.Second, 2*time.Second,
		"Failed to delete non-compliant CRD")

	// Wait for CRD to be recreated with correct bundle-version
	t.Log("Waiting for CIO to recreate compliant CRD")
	assert.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), crdName, crd); err != nil {
			if errors.IsNotFound(err) {
				t.Logf("CRD not yet recreated")
			} else {
				t.Logf("Error getting CRD: %v", err)
			}
			return false
		}

		bundleVersion, found := crd.Annotations[bundleVersionAnnotation]
		if !found || bundleVersion == "v0.0.0-takeover-blocked" {
			t.Logf("CRD not yet compliant (bundle-version=%s, found=%v)", bundleVersion, found)
			return false
		}

		t.Logf("CRD recreated with compliant bundle-version: %s", bundleVersion)
		return true
	}, 3*time.Minute, 5*time.Second,
		"Expected CIO to recreate CRD with compliant bundle-version")

	// Verify takeover succeeds
	t.Log("Verifying takeover succeeds after restoring compliance")
	assert.Eventually(t, func() bool {
		if err := kclient.Get(context.Background(), ingressName, ingress); err != nil {
			t.Logf("Failed to get Ingress CR: %v", err)
			return false
		}

		managedCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsManaged")
		presentCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsPresent")
		compliantCond := condutils.FindStatusCondition(ingress.Status.Conditions, "GatewayAPICRDsCompliant")

		if managedCond == nil || managedCond.Status != metav1.ConditionTrue {
			t.Logf("Managed not True yet: %+v", managedCond)
			return false
		}
		if presentCond == nil || presentCond.Status != metav1.ConditionTrue {
			t.Logf("Present not True yet: %+v", presentCond)
			return false
		}
		if compliantCond == nil || compliantCond.Status != metav1.ConditionTrue {
			t.Logf("Compliant not True yet: %+v", compliantCond)
			return false
		}

		t.Log("Takeover successful: all conditions True")
		return true
	}, 3*time.Minute, 5*time.Second,
		"Expected takeover to succeed after restoring CRD compliance")

	// Verify VAP is recreated
	t.Log("Verifying VAP is recreated after takeover")
	if err := assertVAP(t, gwapiCRDVAPName); err != nil {
		t.Fatalf("VAP not recreated after takeover: %v", err)
	}

	// Verify Istio is restarted
	t.Log("Verifying Istio control plane is restarted")
	if err := assertIstiodControlPlane(t); err != nil {
		t.Fatalf("Istiod not restarted after takeover: %v", err)
	}
}
