package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	OperatorNamespace      = "openshift-ingress-operator"
	IngressNamespace       = "openshift-ingress"
	PollInterval           = 5 * time.Second
	DefaultTimeout         = 3 * time.Minute
	LongTimeout            = 5 * time.Minute
	ClusterOperatorTimeout = 2 * time.Minute
)

var (
	IngressControllerGVR = schema.GroupVersionResource{Group: "operator.openshift.io", Version: "v1", Resource: "ingresscontrollers"}
	RouteGVR             = schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}
	IngressConfigGVR     = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "ingresses"}
	DnsConfigGVR         = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "dnses"}
	ClusterOperatorGVR   = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusteroperators"}
	InfrastructureGVR    = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "infrastructures"}
	DnsRecordGVR         = schema.GroupVersionResource{Group: "ingress.operator.openshift.io", Version: "v1", Resource: "dnsrecords"}
	CredentialsReqGVR    = schema.GroupVersionResource{Group: "cloudcredential.openshift.io", Version: "v1", Resource: "credentialsrequests"}
	ServicemonitorGVR    = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
)

// clients wraps a controller-runtime client for the API groups these tests use.
type clients struct {
	client crclient.Client
}

// newClients builds a controller-runtime client from the ambient REST config
// (KUBECONFIG or in-cluster), with a scheme covering the core, operator, and
// config API groups these tests use.
func newClients() (*clients, error) {
	restConfig, err := crconfig.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get REST config: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add client-go scheme: %w", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add operator scheme: %w", err)
	}
	if err := configv1.Install(scheme); err != nil {
		return nil, fmt.Errorf("failed to add config scheme: %w", err)
	}
	c, err := crclient.New(restConfig, crclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}
	return &clients{client: c}, nil
}

// getInfrastructure returns the infrastructures.config.openshift.io/cluster object.
func (c *clients) getInfrastructure(ctx context.Context) (*configv1.Infrastructure, error) {
	infra := &configv1.Infrastructure{}
	if err := c.client.Get(ctx, crclient.ObjectKey{Name: "cluster"}, infra); err != nil {
		return nil, err
	}
	return infra, nil
}

// getBaseDomain returns the cluster base domain from the dnses.config.openshift.io/cluster object.
func (c *clients) getBaseDomain(ctx context.Context) (string, error) {
	dns := &configv1.DNS{}
	if err := c.client.Get(ctx, crclient.ObjectKey{Name: "cluster"}, dns); err != nil {
		return "", fmt.Errorf("failed to get dns config: %w", err)
	}
	return dns.Spec.BaseDomain, nil
}

// getClusterName returns the cluster's infrastructure name.
func getClusterName(infra *configv1.Infrastructure) (string, error) {
	if len(infra.Status.InfrastructureName) != 0 {
		return infra.Status.InfrastructureName, nil
	}
	return "", fmt.Errorf("cluster name not found")
}

// uniqueICName returns a unique IngressController name with the given prefix. A random
// suffix keeps specs independent so they do not collide when openshift-tests runs them
// concurrently (each spec runs in its own process).
func uniqueICName(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, rand.String(5))
}

// lbServiceName returns the NamespacedName of the LoadBalancer-type Service for the named IngressController.
func lbServiceName(icName string) crclient.ObjectKey {
	return crclient.ObjectKey{Namespace: IngressNamespace, Name: "router-" + icName}
}

// newNLBIngressController builds an external NLB IngressController with the given security groups.
func newNLBIngressController(name, domain string, securityGroups []operatorv1.SecurityGroupID) *operatorv1.IngressController {
	var nlbParams *operatorv1.AWSNetworkLoadBalancerParameters
	if len(securityGroups) > 0 {
		nlbParams = &operatorv1.AWSNetworkLoadBalancerParameters{SecurityGroups: securityGroups}
	}
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{Namespace: OperatorNamespace, Name: name},
		Spec: operatorv1.IngressControllerSpec{
			Domain:   domain,
			Replicas: ptr.To(int32(1)),
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope:               operatorv1.ExternalLoadBalancer,
					DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type:                          operatorv1.AWSNetworkLoadBalancer,
							NetworkLoadBalancerParameters: nlbParams,
						},
					},
				},
			},
		},
	}
}

// getICCondition returns the status of the named IngressController condition, or "" if not present.
func getICCondition(ic *operatorv1.IngressController, condType string) operatorv1.ConditionStatus {
	for _, cond := range ic.Status.Conditions {
		if cond.Type == condType {
			return cond.Status
		}
	}
	return ""
}

// waitForICCondition polls until the IngressController reports the expected status for the named condition.
func (c *clients) waitForICCondition(ctx context.Context, name, condType string, want operatorv1.ConditionStatus, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := c.client.Get(ctx, crclient.ObjectKey{Namespace: OperatorNamespace, Name: name}, ic); err != nil {
			return false, nil
		}
		return getICCondition(ic, condType) == want, nil
	})
}

// waitForLBAnnotation polls until the LB Service's annotation matches the expected existence/value.
func (c *clients) waitForLBAnnotation(ctx context.Context, icName, annotation string, wantExist bool, wantValue string, timeout time.Duration) error {
	key := lbServiceName(icName)
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := c.client.Get(ctx, key, svc); err != nil {
			return false, nil
		}
		value, ok := svc.Annotations[annotation]
		if !wantExist {
			return !ok, nil
		}
		return ok && value == wantValue, nil
	})
}

// waitForLBProvisioned polls until the LB Service reports a load balancer ingress hostname or IP.
func (c *clients) waitForLBProvisioned(ctx context.Context, icName string, timeout time.Duration) error {
	key := lbServiceName(icName)
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := c.client.Get(ctx, key, svc); err != nil {
			return false, nil
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.Hostname != "" || ing.IP != "" {
				return true, nil
			}
		}
		return false, nil
	})
}

// recreateLBService deletes the LB Service and waits for the operator to recreate it.
func (c *clients) recreateLBService(ctx context.Context, icName string, timeout time.Duration) error {
	key := lbServiceName(icName)
	svc := &corev1.Service{}
	if err := c.client.Get(ctx, key, svc); err != nil {
		return fmt.Errorf("failed to get service: %w", err)
	}
	oldUID := svc.UID
	if err := c.client.Delete(ctx, svc); err != nil {
		return fmt.Errorf("failed to delete service: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		recreated := &corev1.Service{}
		if err := c.client.Get(ctx, key, recreated); err != nil {
			return false, nil
		}
		return recreated.UID != oldUID, nil
	})
}

// createWithRetryOnError creates the object, retrying on transient API errors (for
// example, the admission webhook not yet being ready) until the timeout. An
// AlreadyExists error is treated as success.
func (c *clients) createWithRetryOnError(ctx context.Context, obj crclient.Object, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		if err := c.client.Create(ctx, obj); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return true, nil
			}
			return false, nil
		}
		return true, nil
	})
}

// deleteIngressController deletes the IngressController and waits for its LB Service to be removed.
func (c *clients) deleteIngressController(ctx context.Context, name string, timeout time.Duration) error {
	ic := &operatorv1.IngressController{ObjectMeta: metav1.ObjectMeta{Namespace: OperatorNamespace, Name: name}}
	if err := c.client.Delete(ctx, ic); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete ingresscontroller: %w", err)
	}
	key := lbServiceName(name)
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		err := c.client.Get(ctx, key, svc)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// --- IngressController helpers (dynamic client) ---

type ICOption func(map[string]interface{})

func WithNodePort() ICOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "NodePortService",
			"nodePort": map[string]interface{}{
				"protocol": "TCP",
			},
		}
	}
}

func WithPrivateLB() ICOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "LoadBalancerService",
			"loadBalancer": map[string]interface{}{
				"scope": "Internal",
			},
		}
	}
}

func WithExternalLB() ICOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "LoadBalancerService",
			"loadBalancer": map[string]interface{}{
				"scope": "External",
			},
		}
	}
}

func WithCLB() ICOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "LoadBalancerService",
			"loadBalancer": map[string]interface{}{
				"scope": "External",
				"providerParameters": map[string]interface{}{
					"type": "AWS",
					"aws": map[string]interface{}{
						"type": "Classic",
					},
				},
			},
		}
	}
}

func WithHostNetwork(httpPort, httpsPort, statsPort int64) ICOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "HostNetwork",
			"hostNetwork": map[string]interface{}{
				"httpPort":  httpPort,
				"httpsPort": httpsPort,
				"statsPort": statsPort,
			},
		}
	}
}

func WithDefaultCertificate(secretName string) ICOption {
	return func(spec map[string]interface{}) {
		spec["defaultCertificate"] = map[string]interface{}{
			"name": secretName,
		}
	}
}

func WithClientTLS(caConfigMapName string) ICOption {
	return func(spec map[string]interface{}) {
		spec["clientTLS"] = map[string]interface{}{
			"clientCA": map[string]interface{}{
				"name": caConfigMapName,
			},
			"clientCertificatePolicy": "Required",
		}
	}
}

func CreateIC(ctx context.Context, dc dynamic.Interface, name, domain string, opts ...ICOption) error {
	spec := map[string]interface{}{
		"replicas": int64(1),
		"domain":   domain,
	}
	for _, opt := range opts {
		opt(spec)
	}
	if _, ok := spec["endpointPublishingStrategy"]; !ok {
		WithNodePort()(spec)
	}
	ic := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operator.openshift.io/v1",
			"kind":       "IngressController",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": OperatorNamespace,
			},
			"spec": spec,
		},
	}
	_, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Create(ctx, ic, metav1.CreateOptions{})
	return err
}

func DeleteIC(ctx context.Context, dc dynamic.Interface, name string) {
	_ = dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	_ = wait.PollUntilContextTimeout(ctx, PollInterval, LongTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

func WaitForICAvailable(ctx context.Context, dc dynamic.Interface, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		ic, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		conditions, found, _ := unstructured.NestedSlice(ic.Object, "status", "conditions")
		if !found {
			return false, nil
		}
		for _, c := range conditions {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cm["type"] == "Available" && cm["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

func WaitForDeployGeneration(ctx context.Context, kc kubernetes.Interface, icName string, gen int64, timeout time.Duration) error {
	deployName := "router-" + icName
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := kc.AppsV1().Deployments(IngressNamespace).Get(ctx, deployName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return deploy.Generation == gen && deploy.Status.ObservedGeneration == gen &&
			deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas &&
			deploy.Status.AvailableReplicas == *deploy.Spec.Replicas, nil
	})
}

func GetRouterDeployment(ctx context.Context, kc kubernetes.Interface, icName string) (*appsv1.Deployment, error) {
	return kc.AppsV1().Deployments(IngressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
}

func RouterContainer(deploy *appsv1.Deployment) (*corev1.Container, error) {
	for i := range deploy.Spec.Template.Spec.Containers {
		if deploy.Spec.Template.Spec.Containers[i].Name == "router" {
			return &deploy.Spec.Template.Spec.Containers[i], nil
		}
	}
	return nil, fmt.Errorf("router container not found in deployment %s", deploy.Name)
}

// --- Pod helpers (dynamic client) ---

func GetRouterPodName(ctx context.Context, kc kubernetes.Interface, icName string) (string, error) {
	label := "ingresscontroller.operator.openshift.io/deployment-ingresscontroller=" + icName
	pods, err := kc.CoreV1().Pods(IngressNamespace).List(ctx, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no running router pod found for IC %s", icName)
}

func GetRouterPodNames(ctx context.Context, kc kubernetes.Interface, icName string) ([]string, error) {
	label := "ingresscontroller.operator.openshift.io/deployment-ingresscontroller=" + icName
	pods, err := kc.CoreV1().Pods(IngressNamespace).List(ctx, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range pods.Items {
		names = append(names, p.Name)
	}
	return names, nil
}

func WaitForRouterPod(ctx context.Context, kc kubernetes.Interface, icName string, timeout time.Duration) (string, error) {
	var podName string
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		name, err := GetRouterPodName(ctx, kc, icName)
		if err != nil {
			return false, nil
		}
		podName = name
		return true, nil
	})
	return podName, err
}

func WaitForNewRouterPod(ctx context.Context, kc kubernetes.Interface, icName, oldPod string, timeout time.Duration) (string, error) {
	var podName string
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		name, err := GetRouterPodName(ctx, kc, icName)
		if err != nil || name == oldPod {
			return false, nil
		}
		podName = name
		return true, nil
	})
	return podName, err
}

func WaitForPodDisappear(ctx context.Context, kc kubernetes.Interface, namespace, podName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := kc.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

func WaitForPodReady(ctx context.Context, kc kubernetes.Interface, namespace, labelSelector string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pods, err := kc.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil || len(pods.Items) == 0 {
			return false, nil
		}
		for _, p := range pods.Items {
			if p.Status.Phase != corev1.PodRunning {
				return false, nil
			}
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status != corev1.ConditionTrue {
					return false, nil
				}
			}
		}
		return true, nil
	})
}

func GetPodsByLabel(ctx context.Context, kc kubernetes.Interface, namespace, label string) ([]corev1.Pod, error) {
	pods, err := kc.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func DeleteAllPodsByLabel(ctx context.Context, kc kubernetes.Interface, namespace, label string) error {
	return kc.CoreV1().Pods(namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: label})
}

func GetWorkerNodeCount(ctx context.Context, kc kubernetes.Interface) (int, error) {
	nodes, err := kc.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/worker"})
	if err != nil {
		return 0, err
	}
	return len(nodes.Items), nil
}

// --- Pod exec ---

func ExecInPod(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, namespace, podName, container string, command []string) (string, string, error) {
	req := kc.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command:   command,
			Container: container,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restCfg, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.String(), stderr.String(), err
}

func ExecInRouterPod(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName string, cmd string) (string, error) {
	stdout, _, err := ExecInPod(ctx, restCfg, kc, IngressNamespace, podName, "router", []string{"bash", "-c", cmd})
	return stdout, err
}

// --- Cluster info helpers (dynamic client) ---

func GetBaseDomain(ctx context.Context, dc dynamic.Interface) (string, error) {
	dns, err := dc.Resource(DnsConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	domain, found, err := unstructured.NestedString(dns.Object, "spec", "baseDomain")
	if err != nil || !found {
		return "", fmt.Errorf("baseDomain not found in dns.config/cluster")
	}
	return domain, nil
}

func GetClusterPlatform(ctx context.Context, dc dynamic.Interface) (string, error) {
	infra, err := dc.Resource(InfrastructureGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	platform, _, _ := unstructured.NestedString(infra.Object, "status", "platformStatus", "type")
	return strings.ToLower(platform), nil
}

func GetIngressDomain(ctx context.Context, dc dynamic.Interface) (string, error) {
	ingress, err := dc.Resource(IngressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	domain, _, _ := unstructured.NestedString(ingress.Object, "spec", "domain")
	return domain, nil
}

func GetDefaultPlacement(ctx context.Context, dc dynamic.Interface) (string, error) {
	ingress, err := dc.Resource(IngressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	placement, _, _ := unstructured.NestedString(ingress.Object, "status", "defaultPlacement")
	return placement, nil
}

// --- Patch helpers ---

func PatchICMerge(ctx context.Context, dc dynamic.Interface, name, patchJSON string) error {
	_, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Patch(
		ctx, name, types.MergePatchType, []byte(patchJSON), metav1.PatchOptions{})
	return err
}

func PatchResourceMerge(ctx context.Context, dc dynamic.Interface, gvr schema.GroupVersionResource, namespace, name, patchJSON string) error {
	_, err := dc.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patchJSON), metav1.PatchOptions{})
	return err
}

func PatchClusterResourceMerge(ctx context.Context, dc dynamic.Interface, gvr schema.GroupVersionResource, name, patchJSON string) error {
	_, err := dc.Resource(gvr).Patch(
		ctx, name, types.MergePatchType, []byte(patchJSON), metav1.PatchOptions{})
	return err
}

func PatchICMergeAndWaitForPodDisappear(ctx context.Context, dc dynamic.Interface, kc kubernetes.Interface, icName, patchJSON, podName string, timeout time.Duration) error {
	if err := PatchICMerge(ctx, dc, icName, patchJSON); err != nil {
		return err
	}
	return WaitForPodDisappear(ctx, kc, IngressNamespace, podName, timeout)
}

// --- Route helpers ---

func CreateRoute(ctx context.Context, dc dynamic.Interface, namespace, name, serviceName, hostname string) error {
	route := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "route.openshift.io/v1",
			"kind":       "Route",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"to": map[string]interface{}{
					"kind": "Service",
					"name": serviceName,
				},
			},
		},
	}
	if hostname != "" {
		_ = unstructured.SetNestedField(route.Object, hostname, "spec", "host")
	}
	_, err := dc.Resource(RouteGVR).Namespace(namespace).Create(ctx, route, metav1.CreateOptions{})
	return err
}

func WaitForRouteAdmittedByIC(ctx context.Context, dc dynamic.Interface, namespace, routeName, icName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(RouteGVR).Namespace(namespace).Get(ctx, routeName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		ingress, found, _ := unstructured.NestedSlice(route.Object, "status", "ingress")
		if !found {
			return false, nil
		}
		for _, ing := range ingress {
			im, ok := ing.(map[string]interface{})
			if !ok {
				continue
			}
			if im["routerName"] == icName {
				conds, ok := im["conditions"].([]interface{})
				if !ok {
					continue
				}
				for _, c := range conds {
					cm, ok := c.(map[string]interface{})
					if !ok {
						continue
					}
					if cm["type"] == "Admitted" && cm["status"] == "True" {
						return true, nil
					}
				}
			}
		}
		return false, nil
	})
}

func WaitForRouteNotAdmittedByIC(ctx context.Context, dc dynamic.Interface, namespace, routeName, icName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(RouteGVR).Namespace(namespace).Get(ctx, routeName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		ingress, found, _ := unstructured.NestedSlice(route.Object, "status", "ingress")
		if !found {
			return true, nil
		}
		for _, ing := range ingress {
			im, ok := ing.(map[string]interface{})
			if !ok {
				continue
			}
			if im["routerName"] == icName {
				return false, nil
			}
		}
		return true, nil
	})
}

func LabelRoute(ctx context.Context, dc dynamic.Interface, namespace, name, key, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value)
	return PatchResourceMerge(ctx, dc, RouteGVR, namespace, name, patch)
}

// --- Namespace helpers (dynamic client) ---

func CreateTestNamespace(ctx context.Context, kc kubernetes.Interface, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	_, err := kc.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

func DeleteTestNamespace(ctx context.Context, kc kubernetes.Interface, name string) {
	_ = kc.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
}

func LabelNamespace(ctx context.Context, kc kubernetes.Interface, name, key, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value)
	_, err := kc.CoreV1().Namespaces().Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// --- Workload helpers ---

func CreateWebServerPodAndService(ctx context.Context, kc kubernetes.Interface, namespace string) error {
	labels := map[string]string{"name": "web-server-deploy", "app": "web-server"}

	allowEscalation := false
	runAsNonRoot := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-server-deploy",
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &runAsNonRoot,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{{
				Name:  "nginx",
				Image: "quay.io/openshifttest/nginx-alpine@sha256:cee6930776b92dc1e93b73f9e5965925d49cff3d2e91e1d071c2f0ff72cbca29",
				Ports: []corev1.ContainerPort{{ContainerPort: 8080, Name: "http"}},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &allowEscalation,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
			}},
		},
	}
	if _, err := kc.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return err
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-unsecure",
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Port:       8080,
				TargetPort: intstr.FromInt32(8080),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	_, err := kc.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	return err
}

// --- Secret helpers ---

func CreateTLSSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string, certPEM, keyPEM []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}
	_, err := kc.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	return err
}

func DeleteSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string) {
	_ = kc.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func CreateConfigMap(ctx context.Context, kc kubernetes.Interface, namespace, name string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	_, err := kc.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	return err
}

func DeleteConfigMap(ctx context.Context, kc kubernetes.Interface, namespace, name string) {
	_ = kc.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// --- Operator log helpers ---

func GetOperatorLogs(ctx context.Context, kc kubernetes.Interface, tailLines int64) (string, error) {
	pods, err := kc.CoreV1().Pods(OperatorNamespace).List(ctx, metav1.ListOptions{LabelSelector: "name=ingress-operator"})
	if err != nil {
		return "", fmt.Errorf("failed to list ingress-operator pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no ingress-operator pod found")
	}
	podName := pods.Items[0].Name
	opts := &corev1.PodLogOptions{
		Container: "ingress-operator",
		TailLines: &tailLines,
	}
	result := kc.CoreV1().Pods(OperatorNamespace).GetLogs(podName, opts)
	logBytes, err := result.Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	return string(logBytes), nil
}

// --- ClusterOperator helpers ---

func WaitForClusterOperatorNormal(ctx context.Context, dc dynamic.Interface, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		co, err := dc.Resource(ClusterOperatorGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		conditions, found, _ := unstructured.NestedSlice(co.Object, "status", "conditions")
		if !found {
			return false, nil
		}
		avail, prog, deg := false, false, false
		for _, c := range conditions {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			switch cm["type"] {
			case "Available":
				avail = cm["status"] == "True"
			case "Progressing":
				prog = cm["status"] == "True"
			case "Degraded":
				deg = cm["status"] == "True"
			}
		}
		return avail && !prog && !deg, nil
	})
}

// --- Unstructured field helpers ---

func NestedString(obj map[string]interface{}, fields ...string) string {
	val, _, _ := unstructured.NestedString(obj, fields...)
	return val
}

// --- JSON helpers ---

func ToJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
