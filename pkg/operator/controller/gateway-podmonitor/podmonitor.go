package gatewaypodmonitor

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func (r *reconciler) ensureGatewayPodMonitor(ctx context.Context, gateway *gatewayapiv1.Gateway) (bool, *unstructured.Unstructured, error) {
	name := operatorcontroller.GatewayPodMonitorName(gateway)
	have, current, err := r.currentGatewayPodMonitor(ctx, name)
	if err != nil {
		return false, nil, err
	}

	ownerRef := metav1.OwnerReference{
		APIVersion: gatewayapiv1.GroupVersion.String(),
		Kind:       "Gateway",
		Name:       gateway.Name,
		UID:        gateway.UID,
	}
	desired := desiredGatewayPodMonitor(name, ownerRef)

	switch {
	case !have:
		if err := r.client.Create(ctx, desired); err != nil {
			if errors.IsAlreadyExists(err) {
				return r.currentGatewayPodMonitor(ctx, name)
			}
			return false, nil, fmt.Errorf("failed to create gateway podmonitor: %w", err)
		}
		log.Info("Created gateway podmonitor", "namespace", desired.GetNamespace(), "name", desired.GetName())
		return r.currentGatewayPodMonitor(ctx, name)
	default:
		if updated, err := r.updateGatewayPodMonitor(ctx, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update gateway podmonitor: %w", err)
		} else if updated {
			return r.currentGatewayPodMonitor(ctx, name)
		}
	}

	return true, current, err
}

func desiredGatewayPodMonitor(name types.NamespacedName, ownerRef metav1.OwnerReference) *unstructured.Unstructured {
	// Use []interface{} for all slice fields to avoid DeepCopy failures
	// and comparison issues with API-returned objects.
	pm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"namespace": name.Namespace,
				"name":      name.Name,
				"labels": map[string]interface{}{
					manifests.OwningGatewayLabel: ownerRef.Name,
				},
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						operatorcontroller.GatewayNameLabelKey: ownerRef.Name,
					},
				},
				"podMetricsEndpoints": []interface{}{
					map[string]interface{}{
						"path":     "/stats/prometheus",
						"interval": "60s",
						"port":     "http-envoy-prom",
						"metricRelabelings": []interface{}{
							map[string]interface{}{
								"action":       "keep",
								"sourceLabels": []interface{}{"__name__"},
								"regex":        "istio_.*|envoy_cluster_upstream_cx_active|envoy_cluster_upstream_cx_total|envoy_cluster_upstream_rq_total|envoy_listener_downstream_cx_active|envoy_listener_http_downstream_rq|envoy_server_memory_allocated|envoy_server_memory_heap_size|envoy_server_uptime",
							},
						},
					},
				},
			},
		},
	}
	pm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Kind:    "PodMonitor",
		Version: "v1",
	})
	pm.SetOwnerReferences([]metav1.OwnerReference{ownerRef})
	return pm
}

func (r *reconciler) currentGatewayPodMonitor(ctx context.Context, name types.NamespacedName) (bool, *unstructured.Unstructured, error) {
	pm := &unstructured.Unstructured{}
	pm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Kind:    "PodMonitor",
		Version: "v1",
	})
	if err := r.client.Get(ctx, name, pm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, pm, nil
}

func (r *reconciler) updateGatewayPodMonitor(ctx context.Context, current, desired *unstructured.Unstructured) (bool, error) {
	changed, updated := gatewayPodMonitorChanged(current, desired)
	if !changed {
		return false, nil
	}
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	log.Info("Updated gateway podmonitor", "namespace", updated.GetNamespace(), "name", updated.GetName(), "diff", diff)
	return true, nil
}

func gatewayPodMonitorChanged(current, desired *unstructured.Unstructured) (bool, *unstructured.Unstructured) {
	changed := false
	updated := current.DeepCopy()

	if !cmp.Equal(current.Object["spec"], desired.Object["spec"], cmpopts.EquateEmpty()) {
		changed = true
		updated.Object["spec"] = desired.Object["spec"]
	}
	if !cmp.Equal(current.GetLabels(), desired.GetLabels(), cmpopts.EquateEmpty()) {
		changed = true
		updated.SetLabels(desired.GetLabels())
	}
	if !cmp.Equal(current.GetOwnerReferences(), desired.GetOwnerReferences(), cmpopts.EquateEmpty()) {
		changed = true
		updated.SetOwnerReferences(desired.GetOwnerReferences())
	}

	if !changed {
		return false, nil
	}
	return true, updated
}
