package ingress

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestDesiredPodDisruptionBudget(t *testing.T) {
	pointerTo := func(v_ int) *int32 { v := int32(v_); return &v }
	testCases := []struct {
		description          string
		replicas             *int32
		expectPDB            bool
		expectMaxUnavailable intstr.IntOrString
	}{
		{
			description:          "if replicas is not set, PDB should be 50%",
			replicas:             nil,
			expectPDB:            true,
			expectMaxUnavailable: intstr.FromString("50%"),
		},
		{
			description:          "if replicas is 1, PDB should be absent",
			replicas:             pointerTo(1),
			expectPDB:            false,
			expectMaxUnavailable: intstr.FromString("50%"),
		},
		{
			description:          "if replicas is 2, PDB should be 50%",
			replicas:             pointerTo(2),
			expectPDB:            true,
			expectMaxUnavailable: intstr.FromString("50%"),
		},
		{
			description:          "if replicas is 3, PDB should be 50%",
			replicas:             pointerTo(3),
			expectPDB:            true,
			expectMaxUnavailable: intstr.FromString("50%"),
		},
		{
			description:          "if replicas is 4, PDB should be 25%",
			replicas:             pointerTo(4),
			expectPDB:            true,
			expectMaxUnavailable: intstr.FromString("25%"),
		},
		{
			description:          "if replicas is 5, PDB should be 25%",
			replicas:             pointerTo(5),
			expectPDB:            true,
			expectMaxUnavailable: intstr.FromString("25%"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			trueVar := true
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
				Spec: operatorv1.IngressControllerSpec{
					Replicas: tc.replicas,
				},
			}
			deploymentRef := metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "router-default",
				UID:        "1",
				Controller: &trueVar,
			}
			wantPDB, pdb, err := desiredRouterPodDisruptionBudget(ic, deploymentRef)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch {
			case !wantPDB:
				if tc.expectPDB {
					t.Error("expected true, got false")
				}
			case pdb == nil:
				t.Error("expected pointer, got nil")
			case pdb.Spec.MaxUnavailable == nil:
				t.Errorf("expected PDB with non-nil MaxUnavailable, got %#v", pdb)
			case *pdb.Spec.MaxUnavailable != tc.expectMaxUnavailable:
				t.Errorf("expected %#v, got %#v", tc.expectMaxUnavailable, pdb.Spec.MaxUnavailable)
			}
		})
	}
}
