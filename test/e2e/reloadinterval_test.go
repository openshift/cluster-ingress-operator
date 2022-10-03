//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReloadInterval(t *testing.T) {
	t.Parallel()

	// values that do not satisfy the regex validation will generate this error
	const expectedErr string = `spec.tuningOptions.reloadInterval in body should match '^(0|([0-9]+(\.[0-9]+)?(ns|us|µs|μs|ms|s|m|h))+)$'`

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "reloadinterval-test"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}

	defer assertIngressControllerDeleted(t, kclient, ic)
	t.Logf("waiting for ingresscontroller %v", icName)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := getIngressController(t, kclient, icName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller router deployment: %v", err)
	}

	for _, testCase := range []struct {
		description    string
		reloadInterval metav1.Duration
		expectedEnvVar string
		expectSuccess  bool
	}{
		// set ReloadInterval to a value that does not pass the regex validation
		// expect validation error
		{"set reloadInterval -1s", metav1.Duration{Duration: -1 * time.Second}, "5s", false},

		// the following values all pass the regex validation
		//
		// set Spec.TuningOptions.ReloadInterval to a non default value within allowed bounds
		// expect no error
		{"set reloadInterval 100s", metav1.Duration{Duration: 100 * time.Second}, "100s", true},
		{"set reloadInterval 1m", metav1.Duration{Duration: 1 * time.Minute}, "1m", true},

		// set ReloadInterval to 0, which should give default of 5s
		// expect no error
		{"set reloadInterval default", metav1.Duration{Duration: 0 * time.Second}, "5s", true},

		// set ReloadInterval to a value outside allowed bounds
		// expect no error
		{"set reloadInterval 200ns", metav1.Duration{Duration: 200 * time.Nanosecond}, "5s", true},
		{"set reloadInterval 5us", metav1.Duration{Duration: 5 * time.Microsecond}, "5s", true},
		{"set reloadInterval 500ms", metav1.Duration{Duration: 500 * time.Millisecond}, "5s", true},
		{"set reloadInterval 130s", metav1.Duration{Duration: 130 * time.Second}, "2m", true},
		{"set reloadInterval 2h", metav1.Duration{Duration: 2 * time.Hour}, "2m", true},
	} {
		t.Log(testCase.description)
		// cases with values that pass the regex validation
		if testCase.expectSuccess {
			if err := setReloadIntervalValidValue(t, kclient, 1*time.Minute, testCase.reloadInterval, icName); err != nil {
				t.Errorf("failed to update ingresscontroller with reloadInterval=%v: %v", testCase.reloadInterval, err)
				continue
			}
			// cases with values that fail the regex validation
		} else {
			err := setReloadIntervalInvalidValue(t, kclient, testCase.reloadInterval, icName)
			if err == nil {
				t.Errorf("expected an error when attempting invalid value")
				continue
			}
			if !strings.Contains(err.Error(), expectedErr) {
				t.Errorf("expected error message %q, got %v", expectedErr, err)
				continue
			}
		}

		if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
			t.Fatalf("failed to observe expected conditions: %v", err)
		}
		if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, ingresscontroller.RouterReloadIntervalEnvName, testCase.expectedEnvVar); err != nil {
			t.Fatalf("router deployment not updated with %s=%v: %v", ingresscontroller.RouterReloadIntervalEnvName, testCase.expectedEnvVar, err)
		}
	}
}

func setReloadIntervalValidValue(t *testing.T, client client.Client, timeout time.Duration, reloadInterval metav1.Duration, name types.NamespacedName) error {
	t.Helper()

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		ic := operatorv1.IngressController{}
		if err := client.Get(context.TODO(), name, &ic); err != nil {
			t.Logf("Get %q failed: %v, retrying ...", name, err)
			return false, nil
		}
		ic.Spec.TuningOptions.ReloadInterval = reloadInterval
		if err := client.Update(context.TODO(), &ic); err != nil {
			t.Logf("Update %q failed: %v retrying ...", name, err)
			return false, nil
		}
		return true, nil
	})
}

func setReloadIntervalInvalidValue(t *testing.T, client client.Client, reloadInterval metav1.Duration, name types.NamespacedName) error {
	t.Helper()

	ic := operatorv1.IngressController{}
	if err := client.Get(context.TODO(), name, &ic); err != nil {
		t.Logf("Get %q failed: %v", name, err)
		return err
	}
	ic.Spec.TuningOptions.ReloadInterval = reloadInterval
	return client.Update(context.TODO(), &ic)
}
