// +build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/openshift/cluster-ingress-operator/test/manifests"
	"github.com/sirupsen/logrus"

	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func testDefaultIngress(t *testing.T, tc TestConfig) {
	namespace := "ingress-test-" + RandSeq(6)
	logrus.Infof("testing in namespace %s", namespace)

	f := manifests.NewFactory(tc.ClusterName, namespace)

	// Ensure we clean up the test app.
	defer func() {
		ns := f.AppIngressNamespace()
		err := sdk.Delete(ns)
		if err != nil && !errors.IsNotFound(err) {
			t.Logf("failed to clean up test app namespace %s: %s", ns.Name, err)
		}
	}()

	// Create the test app.
	testAssets := []sdk.Object{
		f.AppIngressNamespace(),
		f.AppIngressDeployment(),
		f.AppIngressService(),
		f.AppIngressRoute(),
	}
	for _, obj := range testAssets {
		err := sdk.Create(obj)
		if err != nil {
			t.Fatalf("error creating object %v: %v", obj, err)
		}
	}

	// Hard-coded reference to the default router.
	// TODO: we could probably discover this
	router := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "router-default",
			Namespace: "openshift-cluster-ingress-router",
		},
	}

	// Wait for the load balancer to be created.
	err := wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
		err := sdk.Get(router)
		if err != nil {
			return false, nil
		}
		for _, ingress := range router.Status.LoadBalancer.Ingress {
			if len(ingress.Hostname) > 0 {
				t.Logf("router service %s/%s has external hostname %s", router.Namespace, router.Name, ingress.Hostname)
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for default router %s/%s: %s", router.Namespace, router.Name, err)
	}

	// Wait for the app route to be available for the router's load balancer.
	client := &http.Client{}
	routeHostname := f.AppIngressRoute().Spec.Host
	routerHostname := router.Status.LoadBalancer.Ingress[0].Hostname

	err = wait.Poll(1*time.Second, 5*time.Minute, func() (bool, error) {
		req, err := http.NewRequest("GET", "http://"+routerHostname, nil)
		req.Host = routeHostname
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for route endpoint %q via router hostname %q: %s", routeHostname, routerHostname, err)
	}
}
