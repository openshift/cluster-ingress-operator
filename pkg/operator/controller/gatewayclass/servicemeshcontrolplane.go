package gatewayclass

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	maistrav1 "github.com/maistra/istio-operator/pkg/apis/maistra/v1"
	maistrav2 "github.com/maistra/istio-operator/pkg/apis/maistra/v2"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ensureServiceMeshControlPlane attempts to ensure that a
// servicemeshcontrolplane is present and returns a Boolean indicating whether
// it exists, the servicemeshcontrolplane if it exists, and an error value.
func (r *reconciler) ensureServiceMeshControlPlane(ctx context.Context, gatewayclass *gatewayapiv1beta1.GatewayClass) (bool, *maistrav2.ServiceMeshControlPlane, error) {
	name := controller.ServiceMeshControlPlaneName(r.config.OperandNamespace)
	have, current, err := r.currentServiceMeshControlPlane(ctx, name)
	if err != nil {
		return false, nil, err
	}

	// TODO If we have a current SMCP with a different owner reference,
	// should we append the new gatewayclass?
	ownerRef := metav1.OwnerReference{
		APIVersion: gatewayapiv1beta1.SchemeGroupVersion.String(),
		Kind:       "GatewayClass",
		Name:       gatewayclass.Name,
		UID:        gatewayclass.UID,
	}
	desired, err := desiredServiceMeshControlPlane(name, ownerRef)
	if err != nil {
		return have, current, err
	}

	switch {
	case !have:
		if err := r.createServiceMeshControlPlane(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentServiceMeshControlPlane(ctx, name)
	case have:
		if updated, err := r.updateServiceMeshControlPlane(ctx, current, desired); err != nil {
			return have, current, err
		} else if updated {
			return r.currentServiceMeshControlPlane(ctx, name)
		}
	}
	return true, current, nil
}

// desiredServiceMeshControlPlane returns the desired servicemeshcontrolplane.
func desiredServiceMeshControlPlane(name types.NamespacedName, ownerRef metav1.OwnerReference) (*maistrav2.ServiceMeshControlPlane, error) {
	pilotContainerEnv := map[string]string{
		"PILOT_ENABLE_GATEWAY_CONTROLLER_MODE":   "true",
		"PILOT_GATEWAY_API_CONTROLLER_NAME":      OpenShiftGatewayClassControllerName,
		"PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS": OpenShiftDefaultGatewayClassName,
		// OSSM will only reconcile the default gateway class if this is true.
		"PILOT_ENABLE_GATEWAY_API_GATEWAYCLASS_CONTROLLER": "true",
	}
	f := false
	t := true
	smcp := maistrav2.ServiceMeshControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       name.Namespace,
			Name:            name.Name,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: maistrav2.ControlPlaneSpec{
			Addons: &maistrav2.AddonsConfig{
				// TODO Autodetect when these components have
				// been installed and enable them in the SMCP
				// if they are present.
				Grafana: &maistrav2.GrafanaAddonConfig{
					Enablement: maistrav2.Enablement{Enabled: &f},
				},
				Jaeger: &maistrav2.JaegerAddonConfig{
					Name:    "jaeger",
					Install: nil,
				},
				Kiali: &maistrav2.KialiAddonConfig{
					Enablement: maistrav2.Enablement{Enabled: &f},
				},
				Prometheus: &maistrav2.PrometheusAddonConfig{
					Enablement: maistrav2.Enablement{Enabled: &f},
				},
			},
			// Both ingress and egress gateways are enabled by default in OSSM.
			// We don't need them, so we have to explicitly disable them.
			Gateways: &maistrav2.GatewaysConfig{
				ClusterIngress: &maistrav2.ClusterIngressGatewayConfig{
					IngressEnabled: &f,
					IngressGatewayConfig: maistrav2.IngressGatewayConfig{
						GatewayConfig: maistrav2.GatewayConfig{
							Enablement: maistrav2.Enablement{
								Enabled: &f,
							},
						},
					},
				},
				ClusterEgress: &maistrav2.EgressGatewayConfig{
					GatewayConfig: maistrav2.GatewayConfig{
						Enablement: maistrav2.Enablement{
							Enabled: &f,
						},
					},
				},
			},
			Mode: maistrav2.ClusterWideMode,
			Policy: &maistrav2.PolicyConfig{
				Type: maistrav2.PolicyTypeIstiod,
			},
			Profiles: []string{"default"},
			Proxy: &maistrav2.ProxyConfig{
				AccessLogging: &maistrav2.ProxyAccessLoggingConfig{
					EnvoyService: &maistrav2.ProxyEnvoyServiceConfig{
						Enablement: maistrav2.Enablement{
							Enabled: &t,
						},
					},
					File: &maistrav2.ProxyFileAccessLogConfig{
						Name: "/dev/stdout",
					},
				},
			},
			Runtime: &maistrav2.ControlPlaneRuntimeConfig{
				Components: map[maistrav2.ControlPlaneComponentName]*maistrav2.ComponentRuntimeConfig{
					maistrav2.ControlPlaneComponentNamePilot: {
						Container: &maistrav2.ContainerConfig{
							Env: pilotContainerEnv,
						},
					},
				},
			},
			Security: &maistrav2.SecurityConfig{
				ManageNetworkPolicy: &f,
			},
			Tracing: &maistrav2.TracingConfig{
				Type: maistrav2.TracerTypeNone,
			},
			Version: "v2.5",
			TechPreview: maistrav1.NewHelmValues(map[string]interface{}{
				"gatewayAPI": map[string]interface{}{
					"enabled": &t,
				},
			}),
		},
	}
	return &smcp, nil
}

// currentServiceMeshControlPlane returns the current servicemeshcontrolplane.
func (r *reconciler) currentServiceMeshControlPlane(ctx context.Context, name types.NamespacedName) (bool, *maistrav2.ServiceMeshControlPlane, error) {
	var smcp maistrav2.ServiceMeshControlPlane
	if err := r.cache.Get(ctx, name, &smcp); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to get ServiceMeshControlPlane %s: %w", name, err)
	}
	return true, &smcp, nil
}

// createServiceMeshControlPlane creates a servicemeshcontrolplane.
func (r *reconciler) createServiceMeshControlPlane(ctx context.Context, smcp *maistrav2.ServiceMeshControlPlane) error {
	if err := r.client.Create(ctx, smcp); err != nil {
		return fmt.Errorf("failed to create ServiceMeshControlPlane %s/%s: %w", smcp.Namespace, smcp.Name, err)
	}
	log.Info("created ServiceMeshControlPlane", "namespace", smcp.Namespace, "name", smcp.Name)
	return nil
}

// smcpCmpOpts is an array of options for go-cmp to use when comparing
// ServiceMeshControlPlane CRs.
var smcpCmpOpts = []cmp.Option{
	cmpopts.EquateEmpty(),
	cmp.AllowUnexported(maistrav1.HelmValues{}),
}

// updateServiceMeshControlPlane updates a servicemeshcontrolplane.
func (r *reconciler) updateServiceMeshControlPlane(ctx context.Context, current, desired *maistrav2.ServiceMeshControlPlane) (bool, error) {
	changed, updated := serviceMeshControlPlaneChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	// Only diff spec because status may include unexported fields that
	// cause go-cmp to panic.
	diff := cmp.Diff(current.Spec, updated.Spec, smcpCmpOpts...)
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update ServiceMeshControlPlane %s/%s: %w", updated.Namespace, updated.Name, err)
	}
	log.Info("updated ServiceMeshControlPlane", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// serviceMeshControlPlaneChanged returns a Boolean indicating whether the
// current ServiceMeshControlPlane matches the expected servicemeshcontrolplane
// and the updated servicemeshcontrolplane if they do not match.
func serviceMeshControlPlaneChanged(current, expected *maistrav2.ServiceMeshControlPlane) (bool, *maistrav2.ServiceMeshControlPlane) {
	if cmp.Equal(current.Spec, expected.Spec, smcpCmpOpts...) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	return true, updated
}
