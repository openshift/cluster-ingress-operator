package monitoringdashboard

import (
	"context"
	_ "embed"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	dashboardConfigMapName      = "grafana-dashboard-ingress-operator"
	dashboardConfigMapNamespace = "openshift-config-managed"
	consoleDashboardLabel       = "console.openshift.io/dashboard"
	DashboardFileName           = "dashboard.json"
)

// ensureMonitoringDashboard creates, updates or deletes an operator generated
// configmap containing the dashboard for ingress operator monitoring.
// Return any errors.
func (r *reconciler) ensureMonitoringDashboard(ctx context.Context, infraStatus configv1.InfrastructureStatus) error {
	current, err := r.currentMonitoringDashboard(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current monitoring dashboard: %v", err)
	}

	desired := desiredMonitoringDashboard(ctx, infraStatus, current)

	switch {
	case current == nil && desired != nil:
		err = r.client.Create(ctx, desired)
	case current != nil && desired != nil:
		if dashboardNeedsUpdate(current, desired) {
			err = r.client.Update(ctx, desired)
		}
	case current != nil && desired == nil:
		err = r.client.Delete(ctx, current)
	case current == nil && desired == nil:
		// nothing to do
	}

	return err
}

func ConfigMapName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: dashboardConfigMapNamespace,
		Name:      dashboardConfigMapName,
	}
}

// currentMonitoringDashboard retrieves the existing monitoring dashboard ConfigMap if it exists, otherwise returns nil.
// If an error occurs during the retrieval, it returns the error.
func (r *reconciler) currentMonitoringDashboard(ctx context.Context) (*corev1.ConfigMap, error) {
	configmap := &corev1.ConfigMap{}
	name := ConfigMapName()
	if err := r.client.Get(ctx, name, configmap); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return configmap, nil
}

// dashboardJSON is the string representation of the embedded 'dashboard.json' file.
//
//go:embed dashboard.json
var dashboardJSON string

// desiredMonitoringDashboard return the desired configmap for the monitoring dashboard or nil if the
// configmap should not be deployed
func desiredMonitoringDashboard(ctx context.Context, infraStatus configv1.InfrastructureStatus, current *corev1.ConfigMap) *corev1.ConfigMap {
	// If control plane topology is set to external, we do not deploy the dashboard
	if infraStatus.ControlPlaneTopology == configv1.ExternalTopologyMode {
		return nil
	}
	desired := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dashboardConfigMapName,
			Namespace: dashboardConfigMapNamespace,
			Labels: map[string]string{
				consoleDashboardLabel: "true",
			},
		},
		Data: map[string]string{
			DashboardFileName: dashboardJSON,
		},
	}
	if current != nil {
		desired.SetResourceVersion(current.GetResourceVersion())
	}
	return &desired
}

// dashboardNeedsUpdate compares the current dashboard configmap
// and desired ConfigMaps. It returns true if the data within the
// current ConfigMap does not match the desired ConfigMap, or if the
// console label dashboard is false or not present. This indicates
// that the dashboard data requires an update.
func dashboardNeedsUpdate(current *corev1.ConfigMap, desired *corev1.ConfigMap) bool {
	if labelValue, ok := current.ObjectMeta.Labels[consoleDashboardLabel]; !ok || labelValue != "true" {
		return true
	}
	return !reflect.DeepEqual(current.Data, desired.Data)
}
