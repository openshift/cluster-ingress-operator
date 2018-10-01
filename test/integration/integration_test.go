// +build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	stub "github.com/openshift/cluster-ingress-operator/pkg/stub"
	"github.com/openshift/cluster-ingress-operator/test/manifests"

	sdk "github.com/operator-framework/operator-sdk/pkg/sdk"
	k8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
)

var testConfig *TestConfig

func TestIntegration(t *testing.T) {
	testConfig = NewTestConfig(t)

	testConfig.createCRD()
	defer testConfig.deleteCRD()

	testConfig.startOperator()

	// Execute subtests
	t.Run("TestMultipleIngresses", testMultipleIngresses)
}

func testMultipleIngresses(t *testing.T) {
	f := manifests.NewFactory(testConfig.clusterName)

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
	appRouteInternal, err := f.AppIngressRouteInternal()
	if err != nil {
		t.Fatal(err)
	}

	clusterIngressDefault, err := f.ClusterIngressDefault()
	if err != nil {
		t.Fatal(err)
	}
	clusterIngressDefault.Namespace = testConfig.operatorNamespace

	clusterIngressInternal, err := f.ClusterIngressInternal()
	if err != nil {
		t.Fatal(err)
	}
	clusterIngressInternal.Namespace = testConfig.operatorNamespace

	routerNamespace, err := f.RouterNamespace()
	if err != nil {
		t.Fatal(err)
	}
	defaultService, err := f.RouterServiceCloud(clusterIngressDefault)
	if err != nil {
		t.Fatal(err)
	}
	internalService, err := f.RouterServiceCloud(clusterIngressInternal)
	if err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		leftovers := []sdk.Object{
			clusterIngressDefault,
			clusterIngressInternal,
			routerNamespace,
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

	err = sdk.Create(appNamespace)
	if err != nil {
		t.Fatal(err)
	}
	err = sdk.Create(appDeployment)
	if err != nil {
		t.Fatal(err)
	}
	err = sdk.Create(appService)
	if err != nil {
		t.Fatal(err)
	}
	err = sdk.Create(appRouteDefault)
	if err != nil {
		t.Fatal(err)
	}
	err = sdk.Create(appRouteInternal)
	if err != nil {
		t.Fatal(err)
	}
	err = sdk.Create(clusterIngressDefault)
	if err != nil {
		t.Fatal(err)
	}
	err = sdk.Create(clusterIngressInternal)
	if err != nil {
		t.Fatal(err)
	}

	for _, service := range []*corev1.Service{defaultService, internalService} {
		err := wait.Poll(1*time.Second, 2*time.Minute, func() (bool, error) {
			err := sdk.Get(service)
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			for _, ingress := range service.Status.LoadBalancer.Ingress {
				if len(ingress.IP) > 0 {
					t.Logf("service %s/%s has ingress.IP %s", service.Namespace, service.Name, ingress.IP)
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("timed out waiting for service %s/%s: %s", service.Namespace, service.Name, err)
		}
	}

	client := &http.Client{}
	for routeHost, ingressIP := range map[string]string{
		appRouteDefault.Spec.Host:  defaultService.Status.LoadBalancer.Ingress[0].IP,
		appRouteInternal.Spec.Host: internalService.Status.LoadBalancer.Ingress[0].IP,
	} {
		err := wait.Poll(1*time.Second, 2*time.Minute, func() (bool, error) {
			req, err := http.NewRequest("GET", "http://"+ingressIP, nil)
			req.Host = routeHost
			resp, err := client.Do(req)
			defer resp.Body.Close()
			if err != nil {
				return false, err
			}
			if resp.StatusCode == http.StatusOK {
				return true, nil
			}
			return false, fmt.Errorf("last response: %s", resp.Status)
		})
		if err != nil {
			t.Fatalf("timed out waiting for route endpoint %q at ingress IP %q: %s", routeHost, ingressIP, err)
		}
	}
}

type TestConfig struct {
	operatorNamespace string
	clusterName       string
	kubeConfig        string

	t *testing.T
}

func NewTestConfig(t *testing.T) *TestConfig {
	config := &TestConfig{t: t}

	// Check prerequisites
	kubeConfig := os.Getenv("KUBECONFIG")
	if len(kubeConfig) == 0 {
		t.Fatalf("KUBECONFIG is required")
	}
	// The operator-sdk uses KUBERNETES_CONFIG...
	os.Setenv("KUBERNETES_CONFIG", kubeConfig)
	config.kubeConfig = kubeConfig

	clusterName := os.Getenv("CLUSTER_NAME")
	if len(clusterName) == 0 {
		t.Fatalf("CLUSTER_NAME is required")
	}
	config.clusterName = clusterName

	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		namespace = "default"
		os.Setenv("WATCH_NAMESPACE", namespace)
	}
	config.operatorNamespace = namespace

	return config
}

func (tc *TestConfig) startOperator() {
	resource := "ingress.openshift.io/v1alpha1"
	kind := "ClusterIngress"
	resyncPeriod := 10 * time.Minute
	tc.t.Logf("Watching %s, %s, %s, %d", resource, kind, tc.operatorNamespace, resyncPeriod)
	sdk.Watch(resource, kind, tc.operatorNamespace, resyncPeriod)
	sdk.Handle(stub.NewHandler())
	go sdk.Run(context.TODO())
}

func (tc *TestConfig) createCRD() {
	tc.runShellCmd(fmt.Sprintf("oc apply -f ../../manifests/00-custom-resource-definition.yaml -n %s", tc.operatorNamespace), "create cluster ingress CRD")
}

func (tc *TestConfig) deleteCRD() {
	tc.runShellCmd(fmt.Sprintf("oc delete crd clusteringresses.ingress.openshift.io -n %s", tc.operatorNamespace), "delete cluster ingress CRD")
}

func (tc *TestConfig) runShellCmd(command, msg string) {
	cmd := []string{"sh", "-c", command}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = os.Environ()
	c.Env = append(c.Env, fmt.Sprintf("KUBECONFIG=%s", tc.kubeConfig))
	if err := c.Run(); err != nil {
		tc.t.Fatalf("failed to %s: %v", msg, err)
	}
}
