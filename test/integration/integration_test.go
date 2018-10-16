// +build integration

package integration

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/sirupsen/logrus"
)

var clusterName = flag.String("cluster-name", "", "cluster name")
var manifestsDir = flag.String("manifests-dir", "", "manifests directory")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func TestIntegration(t *testing.T) {
	tc := NewTestConfig(t, *clusterName, *manifestsDir)

	tc.setup(t)
	defer tc.teardown(t)

	// Execute subtests
	t.Run("TestDefaultIngress", func(t *testing.T) { testDefaultIngress(t, tc) })
}

type TestConfig struct {
	operatorNamespace string
	clusterName       string
	manifestsDir      string
	kubeConfig        string
}

func NewTestConfig(t *testing.T, clusterName string, manifestsDir string) *TestConfig {
	// Check prerequisites
	kubeConfig := os.Getenv("KUBECONFIG")
	if len(kubeConfig) == 0 {
		t.Fatalf("KUBECONFIG is required")
	}
	// The operator-sdk uses KUBERNETES_CONFIG...
	os.Setenv("KUBERNETES_CONFIG", kubeConfig)

	if len(clusterName) == 0 {
		t.Fatalf("cluster name is required")
	}

	if len(manifestsDir) == 0 {
		t.Fatalf("manifests directory is required")
	}

	return &TestConfig{
		clusterName:       clusterName,
		kubeConfig:        kubeConfig,
		manifestsDir:      manifestsDir,
		operatorNamespace: "openshift-cluster-ingress-operator",
	}
}

func (tc *TestConfig) setup(t *testing.T) {
	// uninstall the CVO
	tc.runShellCmdNonFatal(t, `oc patch -n openshift-cluster-version daemonsets/cluster-version-operator --patch '{"spec": {"template": {"spec": {"nodeSelector": {"node-role.kubernetes.io/fake": ""}}}}}'`)

	// uninstall tectonic-ingress
	tc.runShellCmdNonFatal(t, `oc delete namespaces/openshift-ingress`)

	// uninstall any existing operator
	tc.uninstallOperator(t)

	// reinstall the operator
	tc.runShellCmdNonFatal(t, fmt.Sprintf(`oc apply -f %s`, tc.manifestsDir))
}

func (tc *TestConfig) uninstallOperator(t *testing.T) {
	tc.runShellCmdNonFatal(t, `oc delete -n openshift-cluster-ingress-operator --force --grace-period=0 clusteringresses/default`)
	tc.runShellCmdNonFatal(t, `oc delete namespaces/openshift-cluster-ingress-operator`)
	tc.runShellCmdNonFatal(t, `oc delete namespaces/openshift-cluster-ingress-router`)
	tc.runShellCmdNonFatal(t, `oc delete clusterroles/cluster-ingress-operator:operator`)
	tc.runShellCmdNonFatal(t, `oc delete clusterroles/cluster-ingress:router`)
	tc.runShellCmdNonFatal(t, `oc delete clusterrolebindings/cluster-ingress-operator:operator`)
	tc.runShellCmdNonFatal(t, `oc delete clusterrolebindings/cluster-ingress:router`)
	tc.runShellCmdNonFatal(t, `oc delete customresourcedefinition.apiextensions.k8s.io/clusteringresses.ingress.openshift.io`)
}

func (tc *TestConfig) teardown(t *testing.T) {
	tc.uninstallOperator(t)
}

func (tc *TestConfig) runShellCmdNonFatal(t *testing.T, command string) {
	tc.runShellCmd(t, command, "", false)
}

func (tc *TestConfig) runShellCmd(t *testing.T, command string, msg string, failOnError bool) {
	cmd := []string{"sh", "-c", command}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = os.Environ()
	c.Env = append(c.Env, fmt.Sprintf("KUBECONFIG=%s", tc.kubeConfig))
	output, err := c.CombinedOutput()
	if err != nil && failOnError {
		t.Fatalf("failed to %s: %v", msg, err)
	}
	logrus.Infof("cmd output: %s", output)
}
