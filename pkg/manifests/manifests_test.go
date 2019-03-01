package manifests

import (
	"fmt"
	"testing"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManifests(t *testing.T) {
	config := operatorconfig.Config{
		RouterImage: "quay.io/openshift/router:latest",
	}
	f := NewFactory(config)

	ci := &ingressv1alpha1.ClusterIngress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: ingressv1alpha1.ClusterIngressSpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Replicas: 1,
			RouteSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"baz": "quux",
				},
			},
		},
	}

	if _, err := f.RouterNamespace(); err != nil {
		t.Errorf("invalid RouterNamespace: %v", err)
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

	RouterDeployment(ci)

	metricsCertSecretName := fmt.Sprintf("router-metrics-certs-%s", ci.Name)
	if svc, err := f.RouterServiceInternal(ci); err != nil {
		t.Errorf("invalid RouterServiceInternal: %v", err)
	} else if svc.Annotations[ServingCertSecretAnnotation] != metricsCertSecretName {
		t.Errorf("RouterServiceInternal expected serving secret annotation %s, got %s",
			metricsCertSecretName, svc.Annotations[ServingCertSecretAnnotation])
	}

	LoadBalancerService()
}

func TestDefaultClusterIngress(t *testing.T) {
	DefaultClusterIngress()
}
