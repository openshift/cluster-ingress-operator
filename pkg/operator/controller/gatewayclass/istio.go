package gatewayclass

import (
	"context"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	sailv1alpha1 "github.com/istio-ecosystem/sail-operator/api/v1alpha1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// ensureIstio attempts to ensure that an Istio CR is present and returns a
// Boolean indicating whether it exists, the CR if it exists, and an error
// value.
func (r *reconciler) ensureIstio(ctx context.Context, gatewayclass *gatewayapiv1beta1.GatewayClass) (bool, *sailv1alpha1.Istio, error) {
	name := controller.IstioName(r.config.OperandNamespace)
	have, current, err := r.currentIstio(ctx, name)
	if err != nil {
		return false, nil, err
	}

	// TODO If we have a current CR with a different owner reference,
	// should we append the new gatewayclass?
	ownerRef := metav1.OwnerReference{
		APIVersion: gatewayapiv1beta1.SchemeGroupVersion.String(),
		Kind:       "GatewayClass",
		Name:       gatewayclass.Name,
		UID:        gatewayclass.UID,
	}
	desired, err := desiredIstio(name, ownerRef)
	if err != nil {
		return have, current, err
	}

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
func desiredIstio(name types.NamespacedName, ownerRef metav1.OwnerReference) (*sailv1alpha1.Istio, error) {
	pilotContainerEnv := map[string]string{
		"ENABLE_IOR":               "false",
		"PILOT_ENABLE_GATEWAY_API": "true",
		"PILOT_ENABLE_GATEWAY_API_DEPLOYMENT_CONTROLLER":   "true",
		"PILOT_ENABLE_GATEWAY_API_GATEWAYCLASS_CONTROLLER": "false",
		"PILOT_ENABLE_GATEWAY_API_STATUS":                  "true",
		"PILOT_ENABLE_GATEWAY_CONTROLLER_MODE":             "true",
		"PILOT_GATEWAY_API_CONTROLLER_NAME":                OpenShiftGatewayClassControllerName,
		"PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS":           OpenShiftDefaultGatewayClassName,
		"PILOT_GATEWAY_API_DEPLOYMENT_DEFAULT_LABELS":      `{\"gateway.openshift.io/inject\":\"true\"}`,
	}
	istio := sailv1alpha1.Istio{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       name.Namespace,
			Name:            name.Name,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: sailv1alpha1.IstioSpec{
			Namespace: controller.DefaultOperandNamespace,
			UpdateStrategy: &sailv1alpha1.IstioUpdateStrategy{
				Type: sailv1alpha1.UpdateStrategyTypeInPlace,
			},
			Values: &sailv1alpha1.Values{
				Global: &sailv1alpha1.GlobalConfig{
					IstioNamespace: controller.DefaultOperandNamespace,
				},
				Pilot: &sailv1alpha1.PilotConfig{
					Enabled:            ptr.To(true),
					Env:                pilotContainerEnv,
					ExtraContainerArgs: []string{},
				},
				SidecarInjectorWebhook: &sailv1alpha1.SidecarInjectorConfig{
					EnableNamespacesByDefault: ptr.To(false),
				},
				MeshConfig: &sailv1alpha1.MeshConfig{
					AccessLogFile: "/dev/stdout",
					DefaultConfig: &sailv1alpha1.MeshConfigProxyConfig{
						ProxyHeaders: &sailv1alpha1.ProxyConfigProxyHeaders{
							Server: &sailv1alpha1.ProxyConfigProxyHeadersServer{
								Disabled: ptr.To(true),
							},
							EnvoyDebugHeaders: &sailv1alpha1.ProxyConfigProxyHeadersEnvoyDebugHeaders{
								Disabled: ptr.To(true),
							},
							MetadataExchangeHeaders: &sailv1alpha1.ProxyConfigProxyHeadersMetadataExchangeHeaders{
								Mode: sailv1alpha1.ProxyConfigProxyHeadersMetadataExchangeModeInMesh,
							},
						},
					},
				},
			},
			Version: "v1.23.0",
		},
	}
	return &istio, nil
}

// currentIstio returns the current istio CR.
func (r *reconciler) currentIstio(ctx context.Context, name types.NamespacedName) (bool, *sailv1alpha1.Istio, error) {
	var istio sailv1alpha1.Istio
	if err := r.cache.Get(ctx, name, &istio); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, &istio, nil
}

// createIstio creates an Istio CR.
func (r *reconciler) createIstio(ctx context.Context, istio *sailv1alpha1.Istio) error {
	if err := r.client.Create(ctx, istio); err != nil {
		return err
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
func (r *reconciler) updateIstio(ctx context.Context, current, desired *sailv1alpha1.Istio) (bool, error) {
	changed, updated := istioChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	// Only diff spec because status may include unexported fields that
	// cause go-cmp to panic.
	diff := cmp.Diff(current.Spec, updated.Spec, istioCmpOpts...)
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	log.Info("updated istio", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// istioChanged returns a Boolean indicating whether the current Istio CR
// matches the expected CR, and the updated CR if they do not match.
func istioChanged(current, expected *sailv1alpha1.Istio) (bool, *sailv1alpha1.Istio) {
	if cmp.Equal(current.Spec, expected.Spec, istioCmpOpts...) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	return true, updated
}
