package gatewayclass

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// systemClusterCriticalPriorityClassName is the keyword to specify
// cluster-critical priority class in a pod's spec.priorityClassName.
const systemClusterCriticalPriorityClassName = "system-cluster-critical"

// ensureIstio attempts to ensure that an Istio CR is present and returns a
// Boolean indicating whether it exists, the CR if it exists, and an error
// value.
func (r *reconciler) ensureIstio(ctx context.Context, gatewayclass *gatewayapiv1.GatewayClass) (bool, *sailv1.Istio, error) {
	name := controller.IstioName(r.config.OperandNamespace)
	have, current, err := r.currentIstio(ctx, name)
	if err != nil {
		return false, nil, err
	}

	ownerRef := metav1.OwnerReference{
		APIVersion: gatewayapiv1.SchemeGroupVersion.String(),
		Kind:       "GatewayClass",
		Name:       gatewayclass.Name,
		UID:        gatewayclass.UID,
	}
	desired := desiredIstio(name, ownerRef)

	switch {
	case !have:
		if err := r.createIstio(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentIstio(ctx, name)
	case have:
		if updated, err := r.updateIstio(ctx, current, desired); err != nil {
			return have, current, err
		} else if updated {
			return r.currentIstio(ctx, name)
		}
	}
	return true, current, nil
}

// desiredIstio returns the desired Istio CR.
func desiredIstio(name types.NamespacedName, ownerRef metav1.OwnerReference) *sailv1.Istio {
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
		"PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME": controller.OpenShiftDefaultGatewayClassName,
		// Watch Gateway API and Kubernetes resources in all namespaces,
		// but ignore Istio resources that don't match our label
		// selector.  (We do not specify the label selector, so this
		// causes Istio to ignore all Istio resources.)
		"PILOT_ENABLE_GATEWAY_CONTROLLER_MODE": "true",
		// Only reconcile resources that are associated with
		// gatewayclasses that have our controller name.
		"PILOT_GATEWAY_API_CONTROLLER_NAME": controller.OpenShiftGatewayClassControllerName,
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
	}
	return &sailv1.Istio{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       name.Namespace,
			Name:            name.Name,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: sailv1.IstioSpec{
			Namespace: controller.DefaultOperandNamespace,
			UpdateStrategy: &sailv1.IstioUpdateStrategy{
				Type: sailv1.UpdateStrategyTypeInPlace,
			},
			Values: &sailv1.Values{
				Global: &sailv1.GlobalConfig{
					DefaultPodDisruptionBudget: &sailv1.DefaultPodDisruptionBudgetConfig{
						Enabled: ptr.To(false),
					},
					IstioNamespace:    ptr.To(controller.DefaultOperandNamespace),
					PriorityClassName: ptr.To(systemClusterCriticalPriorityClassName),
				},
				Pilot: &sailv1.PilotConfig{
					Cni: &sailv1.CNIUsageConfig{
						Enabled: ptr.To(false),
					},
					Enabled:            ptr.To(true),
					Env:                pilotContainerEnv,
					ExtraContainerArgs: []string{},
					PodAnnotations: map[string]string{
						WorkloadPartitioningManagementAnnotationKey: WorkloadPartitioningManagementPreferredScheduling,
					},
				},
				SidecarInjectorWebhook: &sailv1.SidecarInjectorConfig{
					EnableNamespacesByDefault: ptr.To(false),
				},
				MeshConfig: &sailv1.MeshConfig{
					AccessLogFile: ptr.To("/dev/stdout"),
					DefaultConfig: &sailv1.MeshConfigProxyConfig{
						ProxyHeaders: &sailv1.ProxyConfigProxyHeaders{
							// Don't set the Server HTTP header.
							Server: &sailv1.ProxyConfigProxyHeadersServer{
								Disabled: ptr.To(true),
							},
							// Don't set the X-Envoy-* header.
							EnvoyDebugHeaders: &sailv1.ProxyConfigProxyHeadersEnvoyDebugHeaders{
								Disabled: ptr.To(true),
							},
							// Don't set X-Envoy-Peer-Metadata
							// or X-Envoy-Peer-Metadata-Id.
							MetadataExchangeHeaders: &sailv1.ProxyConfigProxyHeadersMetadataExchangeHeaders{
								Mode: sailv1.ProxyConfigProxyHeadersMetadataExchangeModeInMesh,
							},
						},
					},
					IngressControllerMode: sailv1.MeshConfigIngressControllerModeOff,
				},
			},
			Version: "v1.24.4",
		},
	}
}

// currentIstio returns the current istio CR.
func (r *reconciler) currentIstio(ctx context.Context, name types.NamespacedName) (bool, *sailv1.Istio, error) {
	var istio sailv1.Istio
	if err := r.cache.Get(ctx, name, &istio); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to get istio %s: %w", name, err)
	}
	return true, &istio, nil
}

// createIstio creates an Istio CR.
func (r *reconciler) createIstio(ctx context.Context, istio *sailv1.Istio) error {
	if err := r.client.Create(ctx, istio); err != nil {
		return fmt.Errorf("failed to create istio %s/%s: %w", istio.Namespace, istio.Name, err)
	}
	log.Info("created istio", "namespace", istio.Namespace, "name", istio.Name)
	return nil
}

// istioCmpOpts is an array of options for go-cmp to use when comparing Istio
// CRs.
var istioCmpOpts = []cmp.Option{
	cmpopts.EquateEmpty(),
}

// updateIstio updates an Istio CR.
func (r *reconciler) updateIstio(ctx context.Context, current, desired *sailv1.Istio) (bool, error) {
	changed, updated := istioChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	// Only diff spec because status may include unexported fields that
	// cause go-cmp to panic.
	diff := cmp.Diff(current.Spec, updated.Spec, istioCmpOpts...)
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update istio %s/%s: %w", updated.Namespace, updated.Name, err)
	}
	log.Info("updated istio", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// istioChanged returns a Boolean indicating whether the current Istio CR
// matches the expected CR, and the updated CR if they do not match.
func istioChanged(current, expected *sailv1.Istio) (bool, *sailv1.Istio) {
	changed := false

	if !cmp.Equal(current.Spec, expected.Spec, istioCmpOpts...) {
		changed = true
	}

	if !cmp.Equal(current.OwnerReferences, expected.OwnerReferences, cmpopts.EquateEmpty()) {
		log.Info("Unexpected ownerReferences, possibly caused by a logic error in this controller or tampering by another entity", "name", current.Name, "ownerReferences", current.OwnerReferences)
		changed = true
	}

	if !changed {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec
	updated.OwnerReferences = expected.OwnerReferences

	return true, updated
}
