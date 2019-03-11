package certificate

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShouldPublishRouterCA(t *testing.T) {
	var replicas int32 = 1
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
		ingresses := []operatorv1.IngressController{}
		for i := range tc.input {
			var cert *corev1.LocalObjectReference
			if secret := tc.input[i]; secret != nil {
				cert = &corev1.LocalObjectReference{Name: *secret}
			}
			ingress := operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("router-%d", i),
				},
				Spec: operatorv1.IngressControllerSpec{
					Replicas:           &replicas,
					DefaultCertificate: cert,
				},
			}
			ingresses = append(ingresses, ingress)
		}
		if e, a := tc.output, shouldPublishRouterCA(ingresses); e != a {
			t.Errorf("%q: expected %t, got %t", tc.description, e, a)
		}

	}
}
