package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/onsi/ginkgo/v2"
	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	operatorv1client "github.com/openshift/client-go/operator/clientset/versioned"
	ingressv1client "github.com/openshift/client-go/operatoringress/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	admissionapi "k8s.io/pod-security-admission/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1client "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
)

type CLI struct {
	execPath         string
	adminConfigPath  string
	configPath       string
	commandArgs      []string
	globalArgs       []string
	finalArgs        []string
	env              []string
	addEnvVars       map[string]string
	verbose          bool
	verb             string
	username         string
	cleanupFunctions []cleanupFunc
	namespace        string
	restCfg          *rest.Config
	hasError         bool
	kclient          client.Client
	openshiftVersion *configv1.ClusterVersion
	stdin            *bytes.Buffer
	stdout           io.Writer
	stderr           io.Writer
}

func NewCLI(project string, level admissionapi.Level, kclient client.Client, restcfg *rest.Config, isOpenshift bool) (*CLI, error) {
	if project == "" {
		return nil, fmt.Errorf("project name cannot be empty")
	}
	if kclient == nil {
		return nil, fmt.Errorf("client cannot be empty")
	}
	if restcfg == nil {
		return nil, fmt.Errorf("restcfg cannot be empty")
	}

	var cv *configv1.ClusterVersion
	if isOpenshift {
		c, err := configv1client.NewForConfig(restcfg)
		if err != nil {
			return nil, err
		}

		cv, err = c.ConfigV1().ClusterVersions().Get(context.Background(), "version", metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
	}

	namespaceGeneratedName := names.SimpleNameGenerator.GenerateName(fmt.Sprintf("e2e-test-%s-", project))
	ns, nsCleanupFunc, err := CreateNamespace(context.Background(), namespaceGeneratedName, level, kclient)
	if err != nil {
		return &CLI{cleanupFunctions: []cleanupFunc{nsCleanupFunc}}, err
	}

	cli := &CLI{
		execPath:         "oc",
		username:         "admin",
		adminConfigPath:  os.Getenv("KUBECONFIG"),
		namespace:        ns.GetName(),
		cleanupFunctions: []cleanupFunc{nsCleanupFunc},
		restCfg:          restcfg,
		kclient:          kclient,
		openshiftVersion: cv,
	}
	return cli, nil

}

// Cleanup will execute the cleanup functions on a reverse order, from the latest
// function to the first one (Last in, first out)
// It should be added as an afterEach on each call
func (c *CLI) Cleanup(ctx context.Context, dumpNamespace bool) error {
	if dumpNamespace {
		Infof("Dumping events in namespace %q...", c.namespace)
		eventList := &corev1.EventList{}
		if err := c.kclient.List(context.TODO(), eventList, client.InNamespace(c.namespace)); err != nil {
			WarnContextf("failed to list events for namespace %s: %v", c.namespace, err)
		} else {
			for _, e := range eventList.Items {
				Infof("%s", fmt.Sprintln(e.FirstTimestamp, e.Source, e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, e.Message))
			}
		}
	}
	for i := len(c.cleanupFunctions) - 1; i >= 0; i-- {
		if err := c.cleanupFunctions[i](ctx); err != nil {
			return err
		}
	}
	return nil
}

// AllCapabilitiesEnabled returns true if all of the given capabilities are enabled on the cluster.
func (c *CLI) AllCapabilitiesEnabled(caps ...configv1.ClusterVersionCapability) (bool, error) {
	cv, err := c.GetOpenshiftClusterVersion()
	if err != nil {
		return false, err
	}

	enabledCaps := make(map[configv1.ClusterVersionCapability]struct{}, len(cv.Status.Capabilities.EnabledCapabilities))
	for _, c := range cv.Status.Capabilities.EnabledCapabilities {
		enabledCaps[c] = struct{}{}
	}

	for _, c := range caps {
		if _, found := enabledCaps[c]; !found {
			return false, nil
		}
	}

	return true, nil
}

// Args sets the additional arguments for the OpenShift CLI command
func (c *CLI) Args(args ...string) *CLI {
	c.commandArgs = args
	return c
}

// AsAdmin changes current config file path to the admin config.
func (c *CLI) AsAdmin() *CLI {
	nc := *c
	nc.configPath = c.adminConfigPath
	return &nc
}

func (c *CLI) AdminConfig() *rest.Config {
	return c.restCfg
}

func (c *CLI) AdminApiextensionsClient() apiextensionsclient.Interface {
	return apiextensionsclient.NewForConfigOrDie(c.restCfg)
}

func (c *CLI) AdminConfigClient() configv1client.Interface {
	return configv1client.NewForConfigOrDie(c.restCfg)
}

// AdminGatewayApiClient provides a GatewayAPI client for the cluster admin user.
func (c *CLI) AdminGatewayApiClient() gatewayapiv1client.Interface {
	return gatewayapiv1client.NewForConfigOrDie(c.restCfg)
}

func (c *CLI) AdminIngressClient() ingressv1client.Interface {
	return ingressv1client.NewForConfigOrDie(c.AdminConfig())
}

// AdminKubeClient provides a Kubernetes client for the cluster admin user.
func (c *CLI) AdminKubeClient() kubernetes.Interface {
	return kubernetes.NewForConfigOrDie(c.restCfg)
}

func (c *CLI) AdminOperatorClient() operatorv1client.Interface {
	return operatorv1client.NewForConfigOrDie(c.restCfg)
}

// TODO: this should be a user and not an admin, need to check!
// GatewayApiClient provides a GatewayAPI client for the current namespace user.
func (c *CLI) GatewayApiClient() gatewayapiv1client.Interface {
	return gatewayapiv1client.NewForConfigOrDie(c.restCfg)
}

// KubeClient provides a Kubernetes client for the current namespace
func (c *CLI) KubeClient() kubernetes.Interface {
	return kubernetes.NewForConfigOrDie(c.restCfg)
}

// END TODO

func (c *CLI) GetNamespace() string {
	return c.namespace
}

func (c *CLI) GetOpenshiftClusterVersion() (*configv1.ClusterVersion, error) {
	if c.openshiftVersion == nil {
		return nil, fmt.Errorf("this is not an openshift cluster")
	}
	return c.openshiftVersion, nil
}

func (c *CLI) GetCurrentOpenshiftVersion() string {
	if c.openshiftVersion == nil {
		return ""
	}
	cv := c.openshiftVersion
	for _, h := range c.openshiftVersion.Status.History {
		if h.State == configv1.CompletedUpdate {
			return h.Version
		}
	}
	// Empty history should only occur if method is called early in startup before history is populated.
	if len(cv.Status.History) != 0 {
		return cv.Status.History[len(cv.Status.History)-1].Version
	}
	return ""
}

// Check for the existence of the okd-scos string in the version name to determine if it is OKD
func (c *CLI) IsOKD() bool {
	current := c.GetCurrentOpenshiftVersion()
	return strings.Contains(current, "okd-scos")
}

// Run executes given OpenShift CLI command verb (iow. "oc <verb>").
// This function also override the default 'stdout' to redirect all output
// to a buffer and prepare the global flags such as namespace and config path.
func (c *CLI) Run(commands ...string) *CLI {
	in, out, errout := &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}
	nc := &CLI{
		execPath:        c.execPath,
		verb:            commands[0],
		adminConfigPath: c.adminConfigPath,
		configPath:      c.configPath,
		username:        c.username,
		globalArgs:      commands,
	}
	if len(c.configPath) > 0 {
		nc.globalArgs = append([]string{fmt.Sprintf("--kubeconfig=%s", c.configPath)}, nc.globalArgs...)
	}
	/*if len(c.configPath) == 0 && len(c.token) > 0 {
		nc.globalArgs = append([]string{fmt.Sprintf("--token=%s", c.token)}, nc.globalArgs...)
	}
	if !c.withoutNamespace {
		nc.globalArgs = append([]string{fmt.Sprintf("--namespace=%s", c.Namespace())}, nc.globalArgs...)
	}*/
	nc.stdin, nc.stdout, nc.stderr = in, out, errout
	return nc.setOutput(c.stdout)
}

// Execute executes the current command and return error if the execution failed
// This function will set the default output to Ginkgo writer.
func (c *CLI) Execute() error {
	out, err := c.Output()
	if _, err := io.Copy(ginkgo.GinkgoWriter, strings.NewReader(out+"\n")); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: Unable to copy the output to ginkgo writer")
	}
	os.Stdout.Sync()
	return err
}

// Output executes the command and returns stdout/stderr combined into one string
func (c *CLI) Output() (string, error) {
	var buff bytes.Buffer
	_, _, err := c.outputs(&buff, &buff)
	return strings.TrimSpace(string(buff.Bytes())), err
}

// setOutput allows to override the default command output
func (c *CLI) setOutput(out io.Writer) *CLI {
	c.stdout = out
	return c
}

func (c *CLI) outputs(stdOutBuff, stdErrBuff *bytes.Buffer) (string, string, error) {
	cmd, err := c.start(stdOutBuff, stdErrBuff)
	if err != nil {
		return "", "", err
	}
	err = cmd.Wait()

	stdOutBytes := stdOutBuff.Bytes()
	stdErrBytes := stdErrBuff.Bytes()
	stdOut := strings.TrimSpace(string(stdOutBytes))
	stdErr := strings.TrimSpace(string(stdErrBytes))

	switch err.(type) {
	case nil:
		c.stdout = bytes.NewBuffer(stdOutBytes)
		c.stderr = bytes.NewBuffer(stdErrBytes)
		return stdOut, stdErr, nil
	case *exec.ExitError:
		Infof("Error running %s %s:\nStdOut>\n%s\nStdErr>\n%s\n", c.execPath, RedactBearerToken(strings.Join(c.finalArgs, " ")), stdOut, stdErr)
		wrappedErr := fmt.Errorf("Error running %s %s:\nStdOut>\n%s\nStdErr>\n%s\n%w\n", c.execPath, RedactBearerToken(strings.Join(c.finalArgs, " ")), stdOut[getStartingIndexForLastN(stdOutBytes, 4096):], stdErr[getStartingIndexForLastN(stdErrBytes, 4096):], err)
		return stdOut, stdErr, wrappedErr
	default:
		FatalErr(fmt.Errorf("unable to execute %q: %v", c.execPath, err))
		// unreachable code
		return "", "", nil
	}
}

func (c *CLI) start(stdOutBuff, stdErrBuff *bytes.Buffer) (*exec.Cmd, error) {
	c.finalArgs = append(c.globalArgs, c.commandArgs...)
	if c.verbose {
		fmt.Printf("DEBUG: oc %s\n", c.printCmd())
	}
	cmd := exec.Command(c.execPath, c.finalArgs...)
	cmd.Stdin = c.stdin
	// Redact any bearer token information from the log.
	Infof("Running '%s %s'", c.execPath, RedactBearerToken(strings.Join(c.finalArgs, " ")))

	cmd.Env = c.env
	if len(c.addEnvVars) > 0 {
		// This is a nil check to allow setting empty environment with Env()
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		for name, value := range c.addEnvVars {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", name, value))
		}
	}

	cmd.Stdout = stdOutBuff
	cmd.Stderr = stdErrBuff
	err := cmd.Start()

	return cmd, err
}

func (c *CLI) printCmd() string {
	return strings.Join(c.finalArgs, " ")
}

func RedactBearerToken(args string) string {
	if strings.Contains(args, "Authorization: Bearer") {
		// redact bearer token
		re := regexp.MustCompile(`Authorization:\s+Bearer.*\s+`)
		args = re.ReplaceAllString(args, "Authorization: Bearer <redacted> ")
	}
	return args
}

// getStartingIndexForLastN calculates a byte offset in a byte slice such that when using
// that offset, we get the last N (size) bytes.
func getStartingIndexForLastN(byteString []byte, size int) int {
	len := len(byteString)
	if len < size {
		// byte slice is less than size, so use all of it.
		return 0
	}
	return len - size
}
