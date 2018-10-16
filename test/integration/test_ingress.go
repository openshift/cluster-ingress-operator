// +build integration

package integration

import (
	"math/rand"
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

func init() {
	rand.Seed(time.Now().UnixNano())
}

var letters = []rune("abcdefghijklmnopqrstuvwxyz")

func randSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func testDefaultIngress(t *testing.T, tc *TestConfig) {
	namespace := "ingress-test-" + randSeq(6)
	logrus.Infof("testing in namespace %s", namespace)

	f := manifests.NewFactory(tc.clusterName, namespace)

	appNamespace, err := f.AppIngressNamespace()
	if err != nil {
		t.Fatal(err)
	}
	appDeployment, err := f.AppIngressDeployment()
	if err != nil {
		t.Fatal(err)
	}
	appService, err := f.AppIngressService()
	if err != nil {
		t.Fatal(err)
	}
	appRouteDefault, err := f.AppIngressRouteDefault()
	if err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		leftovers := []sdk.Object{
			appNamespace,
		}
		anyFailed := false
		for _, o := range leftovers {
			err := sdk.Delete(o)
			if err != nil && !errors.IsNotFound(err) {
				t.Logf("failed to clean up object %#v: %s", o, err)
				anyFailed = true
			}
		}
		if anyFailed {
			t.Fatalf("failed to clean up resources")
		}
	}
	defer cleanup()

	for _, obj := range []sdk.Object{appNamespace, appDeployment, appService, appRouteDefault} {
		err := sdk.Create(obj)
		if err != nil {
			t.Fatalf("error creating object %v: %v", obj, err)
		}
	}

	defaultRouter := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "router-default",
			Namespace: "openshift-cluster-ingress-router",
		},
	}

	err = wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
		err := sdk.Get(defaultRouter)
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		for _, ingress := range defaultRouter.Status.LoadBalancer.Ingress {
			if len(ingress.Hostname) > 0 {
				t.Logf("service %s/%s has ingress.Hostname %s", defaultRouter.Namespace, defaultRouter.Name, ingress.Hostname)
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for default router %s/%s: %s", defaultRouter.Namespace, defaultRouter.Name, err)
	}

	client := &http.Client{}
	for routeHost, ingressIP := range map[string]string{
		appRouteDefault.Spec.Host: defaultRouter.Status.LoadBalancer.Ingress[0].Hostname,
	} {
		err := wait.Poll(1*time.Second, 5*time.Minute, func() (bool, error) {
			req, err := http.NewRequest("GET", "http://"+ingressIP, nil)
			req.Host = routeHost
			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("timed out waiting for route endpoint %q at ingress IP %q: %s", routeHost, ingressIP, err)
		}
	}
}
