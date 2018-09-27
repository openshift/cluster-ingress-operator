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

	ds, err := f.RouterDaemonSet(ci)
	if err != nil {
		t.Errorf("invalid RouterDaemonSet: %v", err)
	}
	if len(ds.Spec.Template.Spec.NodeSelector) == 0 {
		t.Error("RouterDaemonSet has no default node selector")
	}

	ci.Spec.NodePlacement = &ingressv1alpha1.NodePlacement{
		NodeSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"xyzzy": "quux",
			},
		},
	}
	ds, err = f.RouterDaemonSet(ci)
	if err != nil {
		t.Errorf("invalid RouterDaemonSet: %v", err)
	}
	if len(ds.Spec.Template.Spec.NodeSelector) != 1 ||
		ds.Spec.Template.Spec.NodeSelector["xyzzy"] != "quux" {
		t.Errorf("RouterDaemonSet has unexpected node selector: %#v",
		         ds.Spec.Template.Spec.NodeSelector)
	}

	if _, err := f.RouterServiceCloud(ci); err != nil {
		t.Errorf("invalid RouterServiceCloud: %v", err)
	}
}
