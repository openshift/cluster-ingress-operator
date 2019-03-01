package controller

import (
	"context"
	"fmt"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func (r *reconciler) ensureServiceMonitor(ci *ingressv1alpha1.ClusterIngress, svc *corev1.Service, deploymentRef metav1.OwnerReference) (*unstructured.Unstructured, error) {
	desired := desiredServiceMonitor(ci, svc, deploymentRef)

	current, err := r.currentServiceMonitor(ci)
	if err != nil {
		return nil, err
	}

	if desired != nil && current == nil {
		if err := r.Client.Create(context.TODO(), desired); err != nil {
			return nil, fmt.Errorf("failed to create servicemonitor %s/%s: %v", desired.GetNamespace(), desired.GetName(), err)
		}
		log.Info("created servicemonitor", "namespace", desired.GetNamespace(), "name", desired.GetName())
		return desired, nil
	}
	return current, nil
}

func serviceMonitorName(ci *ingressv1alpha1.ClusterIngress) types.NamespacedName {
	return types.NamespacedName{Namespace: "openshift-ingress", Name: "router-" + ci.Name}
}

func desiredServiceMonitor(ci *ingressv1alpha1.ClusterIngress, svc *corev1.Service, deploymentRef metav1.OwnerReference) *unstructured.Unstructured {
	name := serviceMonitorName(ci)
	sm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"namespace": name.Namespace,
				"name":      name.Name,
			},
			"spec": map[string]interface{}{
				"namespaceSelector": map[string]interface{}{
					"matchNames": []interface{}{
						"openshift-ingress",
					},
				},
				"selector": map[string]interface{}{},
				"endpoints": []map[string]interface{}{
					{
						"bearerTokenFile": "/var/run/secrets/kubernetes.io/serviceaccount/token",
						"interval":        "30s",
						"port":            "metrics",
						"scheme":          "https",
						"path":            "/metrics",
						"tlsConfig": map[string]interface{}{
							"caFile":     "/etc/prometheus/configmaps/serving-certs-ca-bundle/service-ca.crt",
							"serverName": fmt.Sprintf("%s.%s.svc", svc.Name, svc.Namespace),
						},
					},
				},
			},
		},
	}
	sm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Kind:    "ServiceMonitor",
		Version: "v1",
	})
	sm.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	return sm
}

func (r *reconciler) currentServiceMonitor(ci *ingressv1alpha1.ClusterIngress) (*unstructured.Unstructured, error) {
	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Kind:    "ServiceMonitor",
		Version: "v1",
	})
	if err := r.Client.Get(context.TODO(), serviceMonitorName(ci), sm); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return sm, nil
}
