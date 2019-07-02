package controller

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"

	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func (r *reconciler) ensureServiceMonitor(ci *operatorv1.IngressController, svc *corev1.Service, deploymentRef metav1.OwnerReference) (*unstructured.Unstructured, error) {
	desired := desiredServiceMonitor(ci, svc, deploymentRef)

	current, err := r.currentServiceMonitor(ci)
	if err != nil {
		return nil, err
	}

	if desired != nil && current == nil {
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return nil, fmt.Errorf("failed to create servicemonitor %s/%s: %v", desired.GetNamespace(), desired.GetName(), err)
		}
		log.Info("created servicemonitor", "namespace", desired.GetNamespace(), "name", desired.GetName())
		return desired, nil
	}
	return current, nil
}

func serviceMonitorName(ci *operatorv1.IngressController) types.NamespacedName {
	return types.NamespacedName{Namespace: "openshift-ingress", Name: "router-" + ci.Name}
}

func desiredServiceMonitor(ci *operatorv1.IngressController, svc *corev1.Service, deploymentRef metav1.OwnerReference) *unstructured.Unstructured {
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
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						manifests.OwningIngressControllerLabel: ci.Name,
					},
				},
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

func (r *reconciler) currentServiceMonitor(ci *operatorv1.IngressController) (*unstructured.Unstructured, error) {
	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Kind:    "ServiceMonitor",
		Version: "v1",
	})
	if err := r.client.Get(context.TODO(), serviceMonitorName(ci), sm); err != nil {
		if meta.IsNoMatchError(err) {
			// Refresh kube client with latest rest scheme/mapper.
			kClient, err := operatorclient.NewClient(r.KubeConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to create kube client: %v", err)
			}
			r.client = kClient

			err = r.client.Get(context.TODO(), serviceMonitorName(ci), sm)
			if err == nil {
				return sm, nil
			}
		}

		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return sm, nil
}
