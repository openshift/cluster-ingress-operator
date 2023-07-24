package ingress

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Test_desiredRouterPodDisruptionBudget(t *testing.T) {
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
			description:          "if replicas is 3, PDB should be 25%",
			replicas:             pointerTo(3),
			expectPDB:            true,
			expectMaxUnavailable: intstr.FromString("25%"),
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
			t.Errorf("%q: unexpected error: %v", tc.description, err)
		} else if !wantPDB {
			if tc.expectPDB {
				t.Errorf("%q: expected true, got false", tc.description)
			}
		} else if pdb == nil {
			t.Errorf("%q: expected pointer, got nil", tc.description)
		} else if pdb.Spec.MaxUnavailable == nil {
			t.Errorf("%q: expected PDB with non-nil MaxUnavailable, got %#v", tc.description, pdb)
		} else if *pdb.Spec.MaxUnavailable != tc.expectMaxUnavailable {
			t.Errorf("%q: expected %#v, got %#v", tc.description, tc.expectMaxUnavailable, pdb.Spec.MaxUnavailable)
		}
	}
}

func Test_podDisruptionBudgetChange(t *testing.T) {
	two := int32(2)
	three := int32(3)
	trueVar := true

	testCases := []struct {
		description string
		mutate      func(ic *operatorv1.IngressController)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *operatorv1.IngressController) {},
			expect:      false,
		},
		{
			description: "if replicas changes from 2 to 3",
			mutate: func(ic *operatorv1.IngressController) {
				ic.Spec.Replicas = &three
			},
			expect: true,
		},
	}

	// Set up the original ingress controller with 2 replicas
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: operatorv1.IngressControllerSpec{
			Replicas: &two,
		},
	}
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "router-default",
		UID:        "1",
		Controller: &trueVar,
	}
	// Get the original pdb based on the ingress controller and deployment
	_, originalPdb, err := desiredRouterPodDisruptionBudget(ic, deploymentRef)
	if err != nil {
		t.Errorf("expected setup to succeed, but there was a failure: %v", err)
	}

	for _, tc := range testCases {
		// Change the ingress controller and check the resulting pdb
		tc.mutate(ic)
		_, mutatedPdb, err := desiredRouterPodDisruptionBudget(ic, deploymentRef)
		if err != nil {
			t.Errorf("expected setup to succeed, but there was a failure: %v", err)
		}
		if changed, updatedPdb := podDisruptionBudgetChanged(originalPdb, mutatedPdb); changed != tc.expect {
			t.Errorf("%s, expect podDisruptionBudgetChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := podDisruptionBudgetChanged(mutatedPdb, updatedPdb); changedAgain {
				t.Errorf("%s, podDisruptionBudgetChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
