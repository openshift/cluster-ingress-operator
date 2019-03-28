package manifests

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManifests(t *testing.T) {
	f := &Factory{}

	var one int32 = 1
	ci := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: operatorv1.IngressControllerSpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Replicas: &one,
			RouteSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"baz": "quux",
				},
			},
		},
	}

	if _, err := f.RouterServiceAccount(); err != nil {
		t.Errorf("invalid RouterServiceAccount: %v", err)
	}

	if _, err := f.RouterClusterRole(); err != nil {
		t.Errorf("invalid RouterClusterRole: %v", err)
	}

	if _, err := f.RouterClusterRoleBinding(); err != nil {
		t.Errorf("invalid RouterClusterRoleBinding: %v", err)
	}

	if _, err := f.MetricsClusterRole(); err != nil {
		t.Errorf("invalid MetricsClusterRole: %v", err)
	}

	if _, err := f.MetricsClusterRoleBinding(); err != nil {
		t.Errorf("invalid MetricsClusterRoleBinding: %v", err)
	}

	if _, err := f.MetricsRole(); err != nil {
		t.Errorf("invalid MetricsRole: %v", err)
	}

	if _, err := f.MetricsRoleBinding(); err != nil {
		t.Errorf("invalid MetricsRoleBinding: %v", err)
	}

	if _, err := f.RouterStatsSecret(ci); err != nil {
		t.Errorf("invalid RouterStatsSecret: %v", err)
	}

	RouterNamespace()
	RouterDeployment(ci)
	InternalIngressControllerService()
	LoadBalancerService()
}
