package gatewayclass

import (
	"context"
	"fmt"
	"github.com/Azure/go-autorest/autorest/to"
	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"

	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// ReleaseName is the Helm release name for istiod managed by the GatewayClass controller.
	//
	// NOTE: This is the same as Sail Operator's release name ("openshift-gateway-istiod").
	// This is intentional to be consistent.
	// 1. Delete Sail Operator's Istio CR
	// 2. Sail Operator cleans up its Helm release completely
	// 3. GatewayClass controller installs with this release name
	// 4. No conflict because old release is gone
	//
	// The revision value (not release name) determines resource names and Gateway compatibility.
	// TODO: Should we keep the same release name?
	ReleaseName = "openshift-gateway-istiod"

	// ChartBasePath is the base path to the Istio charts directory.
	// This directory is populated by running: make update-istio-charts
	ChartBasePath = "pkg/manifests/assets/istio/charts"

	// WorkloadPartitioningManagementAnnotationKey is the annotation key for
	// workload partitioning.
	WorkloadPartitioningManagementAnnotationKey = "target.workload.openshift.io/management"
	// WorkloadPartitioningManagementPreferredScheduling is the annotation
	// value for preferred scheduling of workload.
	WorkloadPartitioningManagementPreferredScheduling = `{"effect": "PreferredDuringScheduling"}`
)

// systemClusterCriticalPriorityClassName is the keyword to specify
// cluster-critical priority class in a pod's spec.priorityClassName.
const systemClusterCriticalPriorityClassName = "system-cluster-critical"

// helmInstaller wraps sail-operator's helm.ChartManager
type helmInstaller struct {
	chartManager *helm.ChartManager
}

// newHelmInstaller creates a new helm installer
func newHelmInstaller(cfg *rest.Config) *helmInstaller {
	return &helmInstaller{
		chartManager: helm.NewChartManager(cfg, ""),
	}
}

// installIstio installs istiod via Helm Chart
func (h *helmInstaller) installIstio(ctx context.Context, gatewayClass *gatewayapiv1.GatewayClass, istioVersion string, enableInferenceExtension bool) error {
	// Create owner reference - GatewayClass owns the Helm resources
	ownerRef := metav1.OwnerReference{
		APIVersion:         gatewayapiv1.GroupVersion.String(),
		Kind:               "GatewayClass",
		Name:               gatewayClass.Name,
		UID:                gatewayClass.UID,
		Controller:         to.BoolPtr(true),
		BlockOwnerDeletion: to.BoolPtr(true),
	}

	// Construct chart path for the requested Istio version
	chartPath := fmt.Sprintf("%s/%s/istiod", ChartBasePath, istioVersion)

	values := buildIstioHelmValues(istioVersion, enableInferenceExtension)

	_, err := h.chartManager.UpgradeOrInstallChart(
		ctx,
		chartPath,
		values,
		operatorcontroller.DefaultOperandNamespace,
		ReleaseName,
		&ownerRef,
	)

	return err
}

// buildIstioHelmValues constructs the Helm values for installing istiod
// This configuration matches what we currently set via the Istio CR
func buildIstioHelmValues(istioVersion string, enableInferenceExtension bool) helm.Values {
	pilotContainerEnv := map[string]string{
		// Enable Gateway API.
		"PILOT_ENABLE_GATEWAY_API": "true",
		// Do not enable experimental Gateway API features.
		"PILOT_ENABLE_ALPHA_GATEWAY_API": "false",
		// Enable Istio to update status of Gateway API resources.
		"PILOT_ENABLE_GATEWAY_API_STATUS": "true",
		// Enable automated deployment, so that Istio creates an Envoy
		// proxy deployment and a service load-balancer for a gateway.
		"PILOT_ENABLE_GATEWAY_API_DEPLOYMENT_CONTROLLER": "true",
		// Do not enable Istio's gatewayclass controller, which creates
		// the default gatewayclass.  (Only the cluster-admin should be
		// managing the gatewayclass.)
		"PILOT_ENABLE_GATEWAY_API_GATEWAYCLASS_CONTROLLER": "false",
		// Set the default gatewayclass name for the gatewayclass
		// controller.  We do not enable the gatewayclass controller,
		// which otherwise would create the default gatewayclass.
		// However, Istio's deployment controller also uses this
		// setting, namely by reconciling gateways that specify this
		// gatewayclass (irrespective of the controller name that the
		// gatewayclass specifies), so we don't want to leave this at
		// the default value, which is "istio".  If we didn't set this,
		// then our Istiod instance might try to reconcile gateways
		// belonging to an unrelated Istiod instance.
		"PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME": operatorcontroller.OpenShiftDefaultGatewayClassName,
		// Only reconcile resources that are associated with
		// gatewayclasses that have our controller name.
		"PILOT_GATEWAY_API_CONTROLLER_NAME": operatorcontroller.OpenShiftGatewayClassControllerName,
		// Don't create an "istio-remote" gatewayclass for
		// "multi-network gateways".  This is an Istio feature that I
		// haven't really found any explanation for.
		"PILOT_MULTI_NETWORK_DISCOVER_GATEWAY_API": "false",
		// Don't allow Istio's "manual deployment" feature, which would
		// allow a gateway to specify an existing service.  Only allow
		// "automated deployment", meaning Istio creates a new load-
		// balancer service for each gateway.
		"ENABLE_GATEWAY_API_MANUAL_DEPLOYMENT": "false",
		// Only create CA Bundle CM in namespaces where there are
		// Gateway API Gateways
		"PILOT_ENABLE_GATEWAY_API_CA_CERT_ONLY": "true",
		// Don't copy labels or annotations from gateways to resources
		// that Istiod creates for that gateway.  This is an Istio-
		// specific behavior which might not be supported by other
		// Gateway API implementations and that could allow the end-user
		// to inject unsupported configuration, for example using
		// service annotations.
		"PILOT_ENABLE_GATEWAY_API_COPY_LABELS_ANNOTATIONS": "false",
	}

	// Enable inference extension if InferencePool CRD exists
	if enableInferenceExtension {
		pilotContainerEnv["ENABLE_GATEWAY_API_INFERENCE_EXTENSION"] = "true"
	}

	return map[string]interface{}{
		// Set revision to match our existing label used by gateway-labeler and the previous Sail Operator.
		// Using the existing tag avoids having to update gateway-labeler to change the revision tags upon migration.
		"revision": operatorcontroller.IstioName("").Name,
		"global": map[string]interface{}{
			"istioNamespace":    operatorcontroller.DefaultOperandNamespace,
			"priorityClassName": systemClusterCriticalPriorityClassName,
			"trustBundleName":   operatorcontroller.OpenShiftGatewayCARootCertName,
			"defaultPodDisruptionBudget": map[string]interface{}{
				"enabled": false,
			},
		},
		"pilot": map[string]interface{}{
			"enabled":            true,
			"env":                pilotContainerEnv,
			"extraContainerArgs": []string{},
			"cni": map[string]interface{}{
				"enabled": false,
			},
			"podAnnotations": map[string]string{
				WorkloadPartitioningManagementAnnotationKey: WorkloadPartitioningManagementPreferredScheduling,
			},
		},
		"sidecarInjectorWebhook": map[string]interface{}{
			"enableNamespacesByDefault": false,
		},
		"meshConfig": map[string]interface{}{
			"accessLogFile": "/dev/stdout",
			"defaultConfig": map[string]interface{}{
				"proxyHeaders": map[string]interface{}{
					"server": map[string]interface{}{
						"disabled": true,
					},
					"envoyDebugHeaders": map[string]interface{}{
						"disabled": true,
					},
					"metadataExchangeHeaders": map[string]interface{}{
						"mode": sailv1.ProxyConfigProxyHeadersMetadataExchangeModeInMesh,
					},
				},
			},
			"ingressControllerMode": sailv1.MeshConfigIngressControllerModeOff,
		},
	}
}
