package certificate

import (
	"fmt"
	"testing"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShouldPublishRouterCA(t *testing.T) {
	var (
		empty = ""
		x     = "x"
		y     = "y"
	)
	testCases := []struct {
		description string
		input       []*string
		output      bool
	}{
		{"no ingresses", []*string{}, false},
		{"all using no default certificates", []*string{&empty, &empty, &empty}, false},
		{"all using generated default certificates", []*string{nil, nil, nil}, true},
		{"all using custom default certificates", []*string{&x, &x, &y}, false},
		{"mix of custom default certificate and no default certificate", []*string{&x, &x, &empty}, false},
		{"mix of custom default certificate and generated default certificate", []*string{&x, &x, nil}, true},
		{"mix of no default certificate and generated default certificate", []*string{&empty, &empty, nil}, true},
	}
	for _, tc := range testCases {
		ingresses := []ingressv1alpha1.ClusterIngress{}
		for i := range tc.input {
			ingress := ingressv1alpha1.ClusterIngress{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("router-%d", i),
				},
				Spec: ingressv1alpha1.ClusterIngressSpec{
					Replicas:                 1,
					DefaultCertificateSecret: tc.input[i],
				},
			}
			ingresses = append(ingresses, ingress)
		}
		if e, a := tc.output, shouldPublishRouterCA(ingresses); e != a {
			t.Errorf("%q: expected %t, got %t", tc.description, e, a)
		}

	}
}
