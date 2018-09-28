package manifests

import (
	"testing"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManifests(t *testing.T) {
	f := &Factory{}

	ci := &ingressv1alpha1.ClusterIngress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
	}

	if _, err := f.OperatorNamespace(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OperatorCustomResourceDefinition(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OperatorRole(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OperatorRoleBinding(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OperatorClusterRole(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OperatorClusterRoleBinding(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OperatorDeployment(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OperatorServiceAccount(); err != nil {
		t.Fatal(err)
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
	if _, err := f.RouterDaemonSet(ci); err != nil {
		t.Errorf("invalid RouterDaemonSet: %v", err)
	}
	if _, err := f.RouterServiceCloud(ci); err != nil {
		t.Errorf("invalid RouterServiceCloud: %v", err)
	}

	if assetData := f.OperatorAssetContent(); len(assetData) == 0 {
		t.Fatal("expected some valid operator asset content")
	}
}
