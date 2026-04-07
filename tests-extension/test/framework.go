package test

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	oauthv1 "github.com/openshift/api/oauth/v1"
	projectv1 "github.com/openshift/api/project/v1"
	userv1 "github.com/openshift/api/user/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	oauthclient "github.com/openshift/client-go/oauth/clientset/versioned"
	operatorclient "github.com/openshift/client-go/operator/clientset/versioned"
	projectclient "github.com/openshift/client-go/project/clientset/versioned"
	routeclient "github.com/openshift/client-go/route/clientset/versioned"
	userclient "github.com/openshift/client-go/user/clientset/versioned"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// cliState holds the shared framework state across CLI copies.
// It is created once by NewCLI and shared by all copies returned
// from AsAdmin() and Run().
type cliState struct {
	baseName         string
	podSecurityLevel string
	namespace        string
	username         string
	adminConfigPath  string
	userConfigPath   string // temp kubeconfig for oc CLI user commands (OAuth)
	impersonateUser  string // non-empty when using impersonation fallback

	adminConfig *rest.Config
	userConfig  *rest.Config

	// Cached clients (lazily initialized).
	cachedAdminKubeClient     kubernetes.Interface
	cachedKubeClient          kubernetes.Interface
	cachedAdminConfigClient   configclient.Interface
	cachedAdminOperatorClient operatorclient.Interface
	cachedAdminRouteClient    routeclient.Interface
	cachedRouteClient         routeclient.Interface

	cleanupFns []func(ctx context.Context)
}

// CLI provides typed Kubernetes and OpenShift clients and an oc CLI
// wrapper, matching exutil.CLI's admin/user split without depending
// on k8s.io/kubernetes or openshift/origin.
//
// User authentication tries real OAuth tokens first (User + OAuthAccessToken),
// falling back to ServiceAccount impersonation when the User API is
// unavailable (e.g., Hypershift guest clusters).
//
// Namespace provisioning uses ProjectRequest when available (standard OCP),
// falling back to plain Namespace creation with a manual RoleBinding.
type CLI struct {
	*cliState

	// Per-invocation oc command builder state.
	verb           string
	commandArgs    []string
	useAdminConfig bool
}

// CLIOption configures a CLI instance.
type CLIOption func(*cliState)

// WithPodSecurityLevel sets the pod security admission level for the
// test namespace. Valid values: "privileged", "baseline", "restricted".
// Default is "restricted".
func WithPodSecurityLevel(level string) CLIOption {
	return func(s *cliState) {
		s.podSecurityLevel = level
	}
}

// NewCLI creates a lightweight CLI instance and registers Ginkgo
// BeforeEach/AfterEach hooks for project setup and teardown.
// Must be called inside a Describe/Context block, outside of It blocks.
func NewCLI(baseName string, opts ...CLIOption) *CLI {
	state := &cliState{
		baseName:         baseName,
		podSecurityLevel: "restricted",
		adminConfigPath:  os.Getenv("KUBECONFIG"),
	}
	for _, opt := range opts {
		opt(state)
	}
	cli := &CLI{cliState: state}
	g.BeforeEach(cli.SetupProject)
	g.AfterEach(cli.TeardownProject)
	return cli
}

// SetupProject provisions a test namespace and user identity.
// Called automatically by the registered BeforeEach hook.
func (c *CLI) SetupProject() {
	ctx := context.Background()

	// Build admin REST config from KUBECONFIG.
	var err error
	c.adminConfig, err = clientcmd.BuildConfigFromFlags("", c.adminConfigPath)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to build REST config from KUBECONFIG")

	adminKC, err := kubernetes.NewForConfig(c.adminConfig)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create admin kubernetes client")
	c.cachedAdminKubeClient = adminKC

	c.namespace = fmt.Sprintf("e2e-test-%s-%s", c.baseName, utilrand.String(5))
	c.username = fmt.Sprintf("%s-user", c.namespace)

	// Try real OAuth user first, fall back to impersonation.
	userAPIExists, err := doesAPIResourceExist(c.adminConfig, "users", "user.openshift.io")
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to check for User API")

	if userAPIExists {
		c.setupOAuthUser(ctx)

		projectAPIExists, err := doesAPIResourceExist(c.adminConfig, "projectrequests", "project.openshift.io")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to check for Project API")

		if projectAPIExists {
			c.setupProjectNamespace(ctx)
		} else {
			c.setupPlainNamespace(ctx, adminKC)
		}
	} else {
		c.setupPlainNamespace(ctx, adminKC)
		c.setupImpersonationUser()
	}

	// Wait for the default ServiceAccount to be provisioned.
	c.waitForServiceAccount(ctx, "default")

	// Apply pod security labels to the namespace.
	c.applyPodSecurityLabels(ctx, adminKC)

	// Write a temp kubeconfig for oc CLI user commands (OAuth path only).
	if c.impersonateUser == "" && c.userConfig != nil {
		c.userConfigPath, err = writeKubeconfig(c.userConfig, c.namespace)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to write user kubeconfig")
	}

	fmt.Fprintf(g.GinkgoWriter, "Test namespace %q ready (user: %s, auth: %s)\n",
		c.namespace, c.username, c.authMethod())
}

// TeardownProject cleans up all resources created during SetupProject.
// Called automatically by the registered AfterEach hook.
func (c *CLI) TeardownProject() {
	ctx := context.Background()

	// Run cleanup functions in reverse order.
	for i := len(c.cleanupFns) - 1; i >= 0; i-- {
		c.cleanupFns[i](ctx)
	}
	c.cleanupFns = nil

	if c.userConfigPath != "" {
		os.Remove(c.userConfigPath)
		c.userConfigPath = ""
	}

	// Reset cached state for next test.
	c.cachedAdminKubeClient = nil
	c.cachedKubeClient = nil
	c.cachedAdminConfigClient = nil
	c.cachedAdminOperatorClient = nil
	c.cachedAdminRouteClient = nil
	c.cachedRouteClient = nil
	c.adminConfig = nil
	c.userConfig = nil
}

// --- Client accessors ---

// Namespace returns the test namespace name.
func (c *CLI) Namespace() string { return c.namespace }

// AdminConfig returns the admin REST config (cluster-admin from KUBECONFIG).
func (c *CLI) AdminConfig() *rest.Config { return c.adminConfig }

// UserConfig returns the regular user REST config (OAuth token or impersonation).
func (c *CLI) UserConfig() *rest.Config { return c.userConfig }

// AdminKubeClient returns a cached admin Kubernetes client.
func (c *CLI) AdminKubeClient() kubernetes.Interface {
	if c.cachedAdminKubeClient == nil {
		client, err := kubernetes.NewForConfig(c.adminConfig)
		o.Expect(err).NotTo(o.HaveOccurred())
		c.cachedAdminKubeClient = client
	}
	return c.cachedAdminKubeClient
}

// KubeClient returns a cached regular-user Kubernetes client.
func (c *CLI) KubeClient() kubernetes.Interface {
	if c.cachedKubeClient == nil {
		client, err := kubernetes.NewForConfig(c.userConfig)
		o.Expect(err).NotTo(o.HaveOccurred())
		c.cachedKubeClient = client
	}
	return c.cachedKubeClient
}

// AdminConfigClient returns a cached admin OpenShift config client.
func (c *CLI) AdminConfigClient() configclient.Interface {
	if c.cachedAdminConfigClient == nil {
		client, err := configclient.NewForConfig(c.adminConfig)
		o.Expect(err).NotTo(o.HaveOccurred())
		c.cachedAdminConfigClient = client
	}
	return c.cachedAdminConfigClient
}

// AdminOperatorClient returns a cached admin OpenShift operator client.
func (c *CLI) AdminOperatorClient() operatorclient.Interface {
	if c.cachedAdminOperatorClient == nil {
		client, err := operatorclient.NewForConfig(c.adminConfig)
		o.Expect(err).NotTo(o.HaveOccurred())
		c.cachedAdminOperatorClient = client
	}
	return c.cachedAdminOperatorClient
}

// AdminRouteClient returns a cached admin OpenShift route client.
func (c *CLI) AdminRouteClient() routeclient.Interface {
	if c.cachedAdminRouteClient == nil {
		client, err := routeclient.NewForConfig(c.adminConfig)
		o.Expect(err).NotTo(o.HaveOccurred())
		c.cachedAdminRouteClient = client
	}
	return c.cachedAdminRouteClient
}

// RouteClient returns a cached regular-user OpenShift route client.
func (c *CLI) RouteClient() routeclient.Interface {
	if c.cachedRouteClient == nil {
		client, err := routeclient.NewForConfig(c.userConfig)
		o.Expect(err).NotTo(o.HaveOccurred())
		c.cachedRouteClient = client
	}
	return c.cachedRouteClient
}

// --- oc CLI wrapper ---

// AsAdmin returns a CLI copy that runs oc commands as cluster-admin.
func (c *CLI) AsAdmin() *CLI {
	return &CLI{
		cliState:       c.cliState,
		useAdminConfig: true,
	}
}

// Run returns a CLI copy with the oc verb set. Use with Args() and
// Execute()/Output() for method chaining:
//
//	oc.AsAdmin().Run("label").Args("namespace", ns, "type="+ns).Execute()
func (c *CLI) Run(verb string) *CLI {
	return &CLI{
		cliState:       c.cliState,
		verb:           verb,
		useAdminConfig: c.useAdminConfig,
	}
}

// Args appends arguments to the oc command. Returns self for chaining.
func (c *CLI) Args(args ...string) *CLI {
	c.commandArgs = append(c.commandArgs, args...)
	return c
}

// Execute runs the oc command and returns any error.
func (c *CLI) Execute() error {
	_, err := c.runOCCommand()
	return err
}

// Output runs the oc command and returns its combined output.
func (c *CLI) Output() (string, error) {
	return c.runOCCommand()
}

// Exec runs a command inside a pod via oc exec (as admin).
func (c *CLI) Exec(ns, podName, command string) (string, error) {
	return c.AsAdmin().Run("exec").Args("-n", ns, podName, "--", "/bin/sh", "-c", command).Output()
}

func (c *CLI) runOCCommand() (string, error) {
	args := []string{c.verb}
	args = append(args, c.commandArgs...)

	switch {
	case c.useAdminConfig:
		// Admin commands rely on the KUBECONFIG env var.
	case c.userConfigPath != "":
		// OAuth: use the temp kubeconfig with bearer token.
		args = append(args, "--kubeconfig="+c.userConfigPath)
	case c.impersonateUser != "":
		// Impersonation: add --as flag.
		args = append(args, "--as="+c.impersonateUser)
	}

	fmt.Fprintf(g.GinkgoWriter, "Running: oc %s\n", strings.Join(args, " "))
	cmd := exec.Command("oc", args...)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		return output, fmt.Errorf("oc %s failed: %v: %s", strings.Join(args, " "), err, output)
	}
	return output, nil
}

// --- Internal setup helpers ---

// setupOAuthUser creates a real OpenShift User with an OAuthAccessToken
// and builds a REST config authenticated with the bearer token.
func (c *CLI) setupOAuthUser(ctx context.Context) {
	// Create the User.
	userCl, err := userclient.NewForConfig(c.adminConfig)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create user client")

	user, err := userCl.UserV1().Users().Create(ctx, &userv1.User{
		ObjectMeta: metav1.ObjectMeta{Name: c.username},
	}, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create User %s", c.username)
	c.addCleanup(func(ctx context.Context) {
		_ = userCl.UserV1().Users().Delete(ctx, user.Name, metav1.DeleteOptions{})
	})

	// Create the OAuthClient.
	oauthCl, err := oauthclient.NewForConfig(c.adminConfig)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create oauth client")

	oauthClientName := "e2e-client-" + c.namespace
	_, err = oauthCl.OauthV1().OAuthClients().Create(ctx, &oauthv1.OAuthClient{
		ObjectMeta:  metav1.ObjectMeta{Name: oauthClientName},
		GrantMethod: oauthv1.GrantHandlerAuto,
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create OAuthClient")
	}
	c.addCleanup(func(ctx context.Context) {
		_ = oauthCl.OauthV1().OAuthClients().Delete(ctx, oauthClientName, metav1.DeleteOptions{})
	})

	// Create the OAuthAccessToken.
	privToken, pubToken := generateOAuthTokenPair()
	_, err = oauthCl.OauthV1().OAuthAccessTokens().Create(ctx, &oauthv1.OAuthAccessToken{
		ObjectMeta:  metav1.ObjectMeta{Name: pubToken},
		ClientName:  oauthClientName,
		UserName:    c.username,
		UserUID:     string(user.UID),
		Scopes:      []string{"user:full"},
		RedirectURI: "https://localhost:8443/oauth/token/implicit",
	}, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create OAuthAccessToken")
	c.addCleanup(func(ctx context.Context) {
		_ = oauthCl.OauthV1().OAuthAccessTokens().Delete(ctx, pubToken, metav1.DeleteOptions{})
	})

	// Build user REST config: strip all auth, then add bearer token.
	c.userConfig = rest.AnonymousClientConfig(rest.CopyConfig(c.adminConfig))
	c.userConfig.BearerToken = privToken
}

// setupImpersonationUser builds a REST config that impersonates the
// default ServiceAccount in the test namespace.
func (c *CLI) setupImpersonationUser() {
	c.username = fmt.Sprintf("system:serviceaccount:%s:default", c.namespace)
	c.impersonateUser = c.username
	c.userConfig = rest.CopyConfig(c.adminConfig)
	c.userConfig.Impersonate = rest.ImpersonationConfig{
		UserName: c.impersonateUser,
	}
}

// setupProjectNamespace creates the test namespace via the OpenShift
// ProjectRequest API as the regular user. The project controller
// automatically provisions default SAs and role bindings.
func (c *CLI) setupProjectNamespace(ctx context.Context) {
	projectCl, err := projectclient.NewForConfig(c.userConfig)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create project client")

	_, err = projectCl.ProjectV1().ProjectRequests().Create(ctx, &projectv1.ProjectRequest{
		ObjectMeta: metav1.ObjectMeta{Name: c.namespace},
	}, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create ProjectRequest %s", c.namespace)

	c.addCleanup(func(ctx context.Context) {
		_ = c.AdminKubeClient().CoreV1().Namespaces().Delete(ctx, c.namespace, metav1.DeleteOptions{})
	})
}

// setupPlainNamespace creates a plain Kubernetes namespace and grants
// the user edit access via a RoleBinding.
func (c *CLI) setupPlainNamespace(ctx context.Context, adminKC kubernetes.Interface) {
	nsObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: c.namespace},
	}
	_, err := adminKC.CoreV1().Namespaces().Create(ctx, nsObj, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create namespace %s", c.namespace)

	c.addCleanup(func(ctx context.Context) {
		_ = adminKC.CoreV1().Namespaces().Delete(ctx, c.namespace, metav1.DeleteOptions{})
	})

	// Determine the RBAC subject based on auth method.
	var subject rbacv1.Subject
	if c.impersonateUser != "" {
		subject = rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      "default",
			Namespace: c.namespace,
		}
	} else {
		subject = rbacv1.Subject{
			Kind: "User",
			Name: c.username,
		}
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-user-edit",
			Namespace: c.namespace,
		},
		Subjects: []rbacv1.Subject{subject},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "edit",
		},
	}
	_, err = adminKC.RbacV1().RoleBindings(c.namespace).Create(ctx, rb, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create rolebinding for test user")
}

// applyPodSecurityLabels sets the pod security admission labels on the
// test namespace.
func (c *CLI) applyPodSecurityLabels(ctx context.Context, adminKC kubernetes.Interface) {
	ns, err := adminKC.CoreV1().Namespaces().Get(ctx, c.namespace, metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}
	ns.Labels["pod-security.kubernetes.io/enforce"] = c.podSecurityLevel
	ns.Labels["pod-security.kubernetes.io/audit"] = c.podSecurityLevel
	ns.Labels["pod-security.kubernetes.io/warn"] = c.podSecurityLevel
	ns.Labels["security.openshift.io/scc.podSecurityLabelSync"] = "false"

	_, err = adminKC.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to set pod security labels")
}

// waitForServiceAccount waits for a ServiceAccount to be provisioned
// in the test namespace.
func (c *CLI) waitForServiceAccount(ctx context.Context, name string) {
	err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := c.cachedAdminKubeClient.CoreV1().ServiceAccounts(c.namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "timed out waiting for ServiceAccount %s/%s", c.namespace, name)
}

func (c *CLI) addCleanup(fn func(ctx context.Context)) {
	c.cleanupFns = append(c.cleanupFns, fn)
}

func (c *CLI) authMethod() string {
	if c.impersonateUser != "" {
		return "impersonation"
	}
	return "oauth"
}

// --- Standalone helpers ---

// doesAPIResourceExist checks whether a named API resource exists in
// the given API group using the discovery client.
func doesAPIResourceExist(config *rest.Config, resourceName, group string) (bool, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return false, err
	}
	_, resourceLists, err := dc.ServerGroupsAndResources()
	if err != nil {
		var groupErr *discovery.ErrGroupDiscoveryFailed
		if !errors.As(err, &groupErr) {
			return false, err
		}
		// Tolerate partial discovery failures for unrelated groups.
	}
	for _, rl := range resourceLists {
		if groupFromGV(rl.GroupVersion) != group {
			continue
		}
		for _, r := range rl.APIResources {
			if r.Name == resourceName {
				return true, nil
			}
		}
	}
	return false, nil
}

// groupFromGV extracts the group from a "group/version" string.
// For core API "v1", returns "v1" (callers only use this for
// OpenShift groups like "user.openshift.io").
func groupFromGV(groupVersion string) string {
	return strings.Split(groupVersion, "/")[0]
}

// generateOAuthTokenPair returns a private bearer token and its
// SHA-256 hashed public name for OAuthAccessToken storage.
func generateOAuthTokenPair() (privToken, pubToken string) {
	const sha256Prefix = "sha256~"
	b := make([]byte, 16)
	_, err := cryptorand.Read(b)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to generate random token")
	randomToken := base64.RawURLEncoding.EncodeToString(b)
	hashed := sha256.Sum256([]byte(randomToken))
	return sha256Prefix + randomToken, sha256Prefix + base64.RawURLEncoding.EncodeToString(hashed[:])
}

// writeKubeconfig writes a minimal kubeconfig file for the given
// REST config and returns the file path.
func writeKubeconfig(config *rest.Config, namespace string) (string, error) {
	kubeConfig := clientcmdapi.NewConfig()

	cluster := clientcmdapi.NewCluster()
	cluster.Server = config.Host
	cluster.CertificateAuthority = config.TLSClientConfig.CAFile
	cluster.CertificateAuthorityData = config.TLSClientConfig.CAData
	cluster.InsecureSkipTLSVerify = config.TLSClientConfig.Insecure
	kubeConfig.Clusters["default"] = cluster

	authInfo := clientcmdapi.NewAuthInfo()
	authInfo.Token = config.BearerToken
	authInfo.ClientCertificate = config.TLSClientConfig.CertFile
	authInfo.ClientCertificateData = config.TLSClientConfig.CertData
	authInfo.ClientKey = config.TLSClientConfig.KeyFile
	authInfo.ClientKeyData = config.TLSClientConfig.KeyData
	kubeConfig.AuthInfos["default"] = authInfo

	ctx := clientcmdapi.NewContext()
	ctx.Cluster = "default"
	ctx.AuthInfo = "default"
	ctx.Namespace = namespace
	kubeConfig.Contexts["default"] = ctx
	kubeConfig.CurrentContext = "default"

	f, err := os.CreateTemp("", "kubeconfig-e2e-")
	if err != nil {
		return "", err
	}
	f.Close()

	if err := clientcmd.WriteToFile(*kubeConfig, f.Name()); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
