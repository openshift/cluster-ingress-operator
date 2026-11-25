package test

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
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
	MachineSetGVR        = schema.GroupVersionResource{Group: "machine.openshift.io", Version: "v1beta1", Resource: "machinesets"}
	CredentialsReqGVR    = schema.GroupVersionResource{Group: "cloudcredential.openshift.io", Version: "v1", Resource: "credentialsrequests"}
	ServicemonitorGVR    = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}

	privateSubnetPattern       = regexp.MustCompile(`.*-private-([a-z]+)-([a-z]+)-([0-9a-z]+)$`)
	MaxAzureSourceRangePattern = regexp.MustCompile(`exceeds the maximum number of source IP addresses \(400[1-9] > 4000\)`)
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

func WithNLB() ICOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "LoadBalancerService",
			"loadBalancer": map[string]interface{}{
				"scope":               "External",
				"DNSManagementPolicy": "Managed",
				"providerParameters": map[string]interface{}{
					"type": "AWS",
					"aws": map[string]interface{}{
						"type": "NLB",
					},
				},
			},
		}
	}
}

func WithAllowedSourceRanges(cidrs []string) ICOption {
	return func(spec map[string]interface{}) {
		endpoint, ok := spec["endpointPublishingStrategy"].(map[string]interface{})
		if !ok {
			return
		}
		loadBalancer, ok := endpoint["loadBalancer"].(map[string]interface{})
		if !ok {
			return
		}
		loadBalancer["allowedSourceRanges"] = stringValues(cidrs)
	}
}

func WithTuningOptions(options map[string]interface{}) ICOption {
	return func(spec map[string]interface{}) {
		spec["tuningOptions"] = options
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

func DeleteIC(ctx context.Context, dc dynamic.Interface, name string) error {
	if err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete ingresscontroller %s: %w", name, err)
	}
	if err := wait.PollUntilContextTimeout(ctx, PollInterval, LongTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("failed waiting for ingresscontroller %s to be removed: %w", name, err)
	}
	return nil
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

// deployState renders the fields that decide whether a router deployment has
// converged, so a timeout says which one was still wrong.
func deployState(deploy *appsv1.Deployment) string {
	if deploy == nil {
		return "deployment not observed"
	}
	want := int32(0)
	if deploy.Spec.Replicas != nil {
		want = *deploy.Spec.Replicas
	}
	return fmt.Sprintf("generation=%d observedGeneration=%d replicas=%d updated=%d ready=%d available=%d",
		deploy.Generation, deploy.Status.ObservedGeneration, want,
		deploy.Status.UpdatedReplicas, deploy.Status.ReadyReplicas, deploy.Status.AvailableReplicas)
}

func WaitForDeployGeneration(ctx context.Context, kc kubernetes.Interface, icName string, gen int64, timeout time.Duration) error {
	deployName := "router-" + icName
	started := time.Now()
	var last *appsv1.Deployment
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := kc.AppsV1().Deployments(IngressNamespace).Get(ctx, deployName, metav1.GetOptions{})
		if err != nil {
			// A missing deployment is expected while the operator is still
			// creating it; anything else is a real error worth keeping.
			if !apierrors.IsNotFound(err) {
				lastErr = err
			}
			return false, nil
		}
		last, lastErr = deploy, nil
		return deploy.Generation == gen && deploy.Status.ObservedGeneration == gen &&
			deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas &&
			deploy.Status.AvailableReplicas == *deploy.Spec.Replicas, nil
	})
	if err != nil {
		if lastErr != nil {
			return fmt.Errorf("deployment %s did not reach generation %d after %s: %s: %w",
				deployName, gen, time.Since(started).Round(time.Second), deployState(last), lastErr)
		}
		return fmt.Errorf("deployment %s did not reach generation %d after %s: %s",
			deployName, gen, time.Since(started).Round(time.Second), deployState(last))
	}
	return nil
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

// HAProxyContainer returns the haproxy container of the router deployment.
// haproxy runs as a native sidecar, so it lives in initContainers (with
// restartPolicy: Always) rather than alongside the router container.
func HAProxyContainer(deploy *appsv1.Deployment) (*corev1.Container, error) {
	for i := range deploy.Spec.Template.Spec.InitContainers {
		if deploy.Spec.Template.Spec.InitContainers[i].Name == "haproxy" {
			return &deploy.Spec.Template.Spec.InitContainers[i], nil
		}
	}
	return nil, fmt.Errorf("haproxy container not found in deployment %s", deploy.Name)
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

// WaitForRouterPodStopped waits until the old router process has exited. A
// terminating pod can remain in the API during its graceful shutdown, even
// after the router container has stopped.
func WaitForRouterPodStopped(ctx context.Context, kc kubernetes.Interface, podName string, timeout time.Duration) error {
	lastState := "pod not observed"
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := kc.CoreV1().Pods(IngressNamespace).Get(ctx, podName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			return true, nil
		}
		lastState = fmt.Sprintf("phase=%s, deletionTimestamp=%v, router container status missing", pod.Status.Phase, pod.DeletionTimestamp)
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "router" {
				continue
			}
			if status.State.Terminated != nil {
				return true, nil
			}
			lastState = fmt.Sprintf("phase=%s, deletionTimestamp=%v, router container state=%+v", pod.Status.Phase, pod.DeletionTimestamp, status.State)
			break
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("router pod %s did not stop: %s: %w", podName, lastState, err)
	}
	return nil
}

func WaitForPodReady(ctx context.Context, kc kubernetes.Interface, namespace, labelSelector string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pods, err := kc.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil || len(pods.Items) == 0 {
			return false, nil
		}
		activePods := 0
		for _, p := range pods.Items {
			if p.DeletionTimestamp != nil {
				continue
			}
			activePods++
			if p.Status.Phase != corev1.PodRunning {
				return false, nil
			}
			ready := false
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady {
					if c.Status != corev1.ConditionTrue {
						return false, nil
					}
					ready = true
					break
				}
			}
			if !ready {
				return false, nil
			}
		}
		return activePods > 0, nil
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
		}, clientgoscheme.ParameterCodec)

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

func RouterPodEnvValue(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, name string) (string, bool, error) {
	output, err := ExecInRouterPod(ctx, restCfg, kc, podName, "/usr/bin/env")
	if err != nil {
		return "", false, fmt.Errorf("failed to read router pod environment: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if value, found := strings.CutPrefix(line, name+"="); found {
			return value, true, nil
		}
	}
	return "", false, nil
}

func HAProxyConfigHasLine(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, expected string) (bool, error) {
	output, err := ExecInRouterPod(ctx, restCfg, kc, podName, "cat haproxy.config")
	if err != nil {
		return false, fmt.Errorf("failed to read haproxy.config: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == expected {
			return true, nil
		}
	}
	return false, nil
}

func HAProxyConfigHasDirective(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, directive string) (bool, error) {
	output, err := ExecInRouterPod(ctx, restCfg, kc, podName, "cat haproxy.config")
	if err != nil {
		return false, fmt.Errorf("failed to read haproxy.config: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == directive || strings.HasPrefix(line, directive+" ") {
			return true, nil
		}
	}
	return false, nil
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

func PatchICJSON(ctx context.Context, dc dynamic.Interface, name, patchJSON string) error {
	_, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Patch(
		ctx, name, types.JSONPatchType, []byte(patchJSON), metav1.PatchOptions{})
	return err
}

func PatchICAndWaitForNewRouterPod(ctx context.Context, dc dynamic.Interface, kc kubernetes.Interface, icName, patchJSON string, generation int64, oldPod string) (string, error) {
	if err := PatchICMerge(ctx, dc, icName, patchJSON); err != nil {
		return "", fmt.Errorf("failed to patch ingresscontroller: %w", err)
	}
	if err := WaitForDeployGeneration(ctx, kc, icName, generation, LongTimeout); err != nil {
		return "", err
	}
	return waitForNewRouterPodForCurrentReplicaSet(ctx, kc, icName, oldPod, LongTimeout)
}

// GetCurrentRouterPod waits for a running router pod owned by the current
// ReplicaSet of the IngressController's Deployment.
func GetCurrentRouterPod(ctx context.Context, kc kubernetes.Interface, icName string, timeout time.Duration) (string, error) {
	return waitForNewRouterPodForCurrentReplicaSet(ctx, kc, icName, "", timeout)
}

// waitForNewRouterPodForCurrentReplicaSet waits for a running pod owned by the
// Deployment's current ReplicaSet. During a rollout, older pods can remain
// visible while the Deployment status has already converged, so comparing only
// pod names or creation times can select a pod from an earlier generation.
func waitForNewRouterPodForCurrentReplicaSet(ctx context.Context, kc kubernetes.Interface, icName, oldPod string, timeout time.Duration) (string, error) {
	icLabel := "ingresscontroller.operator.openshift.io/deployment-ingresscontroller=" + icName
	var podName string
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		deployment, err := kc.AppsV1().Deployments(IngressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		revision := deployment.Annotations["deployment.kubernetes.io/revision"]
		if revision == "" {
			return false, nil
		}

		replicaSets, err := kc.AppsV1().ReplicaSets(IngressNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		podTemplateHash := ""
		for i := range replicaSets.Items {
			replicaSet := &replicaSets.Items[i]
			if !metav1.IsControlledBy(replicaSet, deployment) {
				continue
			}
			if replicaSet.Annotations["deployment.kubernetes.io/revision"] == revision {
				podTemplateHash = replicaSet.Labels["pod-template-hash"]
				if podTemplateHash == "" {
					podTemplateHash = replicaSet.Spec.Template.Labels["pod-template-hash"]
				}
				break
			}
		}
		if podTemplateHash == "" {
			return false, nil
		}

		pods, err := kc.CoreV1().Pods(IngressNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: icLabel + ",pod-template-hash=" + podTemplateHash,
		})
		if err != nil {
			return false, nil
		}
		for _, pod := range pods.Items {
			if pod.Name != oldPod && pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
				podName = pod.Name
				return true, nil
			}
		}
		return false, nil
	})
	return podName, err
}

func CreateICAndWaitForRouterPod(ctx context.Context, dc dynamic.Interface, kc kubernetes.Interface, name string, opts ...ICOption) string {
	domain, err := GetBaseDomain(ctx, dc)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get base domain")
	o.Expect(CreateIC(ctx, dc, name, name+"."+domain, opts...)).To(o.Succeed(), "failed to create ingresscontroller %s", name)
	o.Expect(WaitForDeployGeneration(ctx, kc, name, 1, LongTimeout)).To(o.Succeed(), "failed to wait for ingresscontroller %s generation 1", name)
	pod, err := GetRouterPodName(ctx, kc, name)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router pod for ingresscontroller %s", name)
	return pod
}

func UpdateICAndWaitForNewRouterPod(ctx context.Context, dc dynamic.Interface, kc kubernetes.Interface, icName, patchJSON string, generation int64, oldPod string) string {
	pod, err := PatchICAndWaitForNewRouterPod(ctx, dc, kc, icName, patchJSON, generation, oldPod)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to update ingresscontroller %s to generation %d", icName, generation)
	return pod
}

func ExpectRouterPodEnv(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, name, value string) {
	actual, found, err := RouterPodEnvValue(ctx, restCfg, kc, podName, name)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to read %s from router pod %s", name, podName)
	o.Expect(found).To(o.BeTrue(), "%s was not set in router pod %s", name, podName)
	o.Expect(actual).To(o.Equal(value), "unexpected %s in router pod %s", name, podName)
}

func ExpectRouterPodEnvAbsent(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, name string) {
	_, found, err := RouterPodEnvValue(ctx, restCfg, kc, podName, name)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to read environment from router pod %s", podName)
	o.Expect(found).To(o.BeFalse(), "%s was unexpectedly set in router pod %s", name, podName)
}

func ExpectHAProxyConfigLine(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, line string) {
	found, err := HAProxyConfigHasLine(ctx, restCfg, kc, podName, line)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to read haproxy.config from router pod %s", podName)
	o.Expect(found).To(o.BeTrue(), "haproxy.config did not contain the exact line %q in router pod %s", line, podName)
}

func ExpectHAProxyConfigDirectiveAbsent(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, directive string) {
	found, err := HAProxyConfigHasDirective(ctx, restCfg, kc, podName, directive)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to read haproxy.config from router pod %s", podName)
	o.Expect(found).To(o.BeFalse(), "haproxy.config unexpectedly contained directive %q in router pod %s", directive, podName)
}

func ExpectInvalidICPatch(ctx context.Context, dc dynamic.Interface, name, patchJSON, expectedError string) {
	err := PatchICMerge(ctx, dc, name, patchJSON)
	o.Expect(err).To(o.HaveOccurred(), "patch for ingresscontroller %s unexpectedly succeeded", name)
	o.Expect(err.Error()).To(o.ContainSubstring(expectedError), "unexpected validation error for ingresscontroller %s", name)
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

// PatchICMergeAndWaitForRouterStop waits for the old router process to exit.
func PatchICMergeAndWaitForRouterStop(ctx context.Context, dc dynamic.Interface, kc kubernetes.Interface, icName, patchJSON, podName string, timeout time.Duration) error {
	if err := PatchICMerge(ctx, dc, icName, patchJSON); err != nil {
		return err
	}
	return WaitForRouterPodStopped(ctx, kc, podName, timeout)
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
			if im["routerName"] != icName {
				continue
			}
			conds, _ := im["conditions"].([]interface{})
			for _, c := range conds {
				cm, ok := c.(map[string]interface{})
				if !ok || cm["type"] != "Admitted" {
					continue
				}
				if status, _ := cm["status"].(string); status != "" {
					return false, nil
				}
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

func DeleteTestNamespace(ctx context.Context, kc kubernetes.Interface, name string) error {
	if err := kc.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete namespace %s: %w", name, err)
	}
	return nil
}

func LabelNamespace(ctx context.Context, kc kubernetes.Interface, name, key, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value)
	_, err := kc.CoreV1().Namespaces().Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// --- Workload helpers ---

// WebServerImage is the image backing the test web server.
//
// This must be an image that resolves on every lane the tests run on,
// including disconnected and IPv6-only metal.  An external registry does not
// qualify: quay.io is unreachable there, the pod never becomes ready, and the
// route assertions that follow never execute (OCPBUGS-132070).  The internal
// registry serves the payload's "tools" imagestream, so it is present wherever
// the cluster itself installed, and "image-registry.openshift-image-registry.svc"
// is on origin's known-image allow list.
//
// test/e2e/util_test.go (buildEchoPod) uses the same image for the same reason.
const WebServerImage = "image-registry.openshift-image-registry.svc:5000/openshift/tools:latest"

// defaultServiceAccountPullSecrets waits until the namespace's default service
// account references at least one usable image pull secret.
//
// A new namespace's default service account is created asynchronously, and the
// integrated-registry pull secret is attached to it a moment later. A pod
// created in that window is scheduled before the secret is available, so CRI-O
// attempts an anonymous pull, the registry returns 401, and the pod sits in
// ImagePullBackOff. The repeated BackOff events trip the
// legacy-test-framework-invariants-pathological monitor in upgrade jobs; that
// is the failure that led to the PR #1654 revert.
func defaultServiceAccountPullSecrets(ctx context.Context, kc kubernetes.Interface, namespace string, timeout time.Duration) ([]corev1.LocalObjectReference, error) {
	var refs []corev1.LocalObjectReference
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		sa, err := kc.CoreV1().ServiceAccounts(namespace).Get(ctx, "default", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}

		// OpenShift stores the generated registry credentials in a
		// "<namespace>-dockercfg-*" secret. Depending on the controller
		// version it is referenced from .secrets, .imagePullSecrets, or both,
		// and its type may be kubernetes.io/dockercfg or
		// kubernetes.io/dockerconfigjson. Accept any reference whose secret
		// actually carries credential data.
		valid := make([]corev1.LocalObjectReference, 0, len(sa.Secrets)+len(sa.ImagePullSecrets))
		seen := make(map[string]bool, len(sa.Secrets)+len(sa.ImagePullSecrets))
		check := func(name string) (bool, error) {
			if seen[name] {
				return false, nil
			}
			seen[name] = true
			secret, err := kc.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				// The service account controller can add the reference
				// before the secret is fully visible.
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if len(secret.Data[corev1.DockerConfigKey]) == 0 && len(secret.Data[corev1.DockerConfigJsonKey]) == 0 {
				return false, nil
			}
			valid = append(valid, corev1.LocalObjectReference{Name: name})
			return true, nil
		}
		for _, ref := range sa.Secrets {
			if _, err := check(ref.Name); err != nil {
				return false, err
			}
		}
		for _, ref := range sa.ImagePullSecrets {
			if _, err := check(ref.Name); err != nil {
				return false, err
			}
		}
		if len(valid) == 0 {
			return false, nil
		}
		refs = valid
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("namespace %s default service account did not receive usable registry credentials within %s: %w", namespace, timeout, err)
	}
	return refs, nil
}

func CreateWebServerPodAndService(ctx context.Context, kc kubernetes.Interface, namespace string) error {
	// Resolve the pull secret before creating the pod. Creating it first is
	// what produced the ImagePullBackOff event storm described above.
	pullSecrets, err := defaultServiceAccountPullSecrets(ctx, kc, namespace, DefaultTimeout)
	if err != nil {
		return err
	}

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
			// Set the resolved references explicitly as well as relying on
			// ServiceAccount admission, so the pod never starts a pull without
			// the integrated-registry credentials.
			ImagePullSecrets: pullSecrets,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &runAsNonRoot,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{{
				Name: "web-server",
				// socat answers every connection with a bare HTTP 200. The
				// tests only need the service to have a ready endpoint so the
				// router admits and backs the route; nothing inspects the body.
				Command: []string{"/bin/socat"},
				Args: []string{
					"TCP4-LISTEN:8080,reuseaddr,fork",
					`EXEC:'/bin/bash -c \"printf \\\"HTTP/1.0 200 OK\r\n\r\n\\\"; sed -e \\\"/^\r/q\\\"\"'`,
				},
				Image: WebServerImage,
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
	if _, err := kc.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return err
	}

	// Do not let a test continue while the backend pod is unavailable: a pod
	// stuck in ImagePullBackOff would otherwise generate repeated events for
	// the rest of the spec while the route assertions still passed.
	if err := waitForWebServerPodReady(ctx, kc, namespace, DefaultTimeout); err != nil {
		return fmt.Errorf("web server pod in namespace %s did not become ready: %w", namespace, err)
	}
	return nil
}

// waitForWebServerPodReady waits for the web server pod, but gives up after a
// short, persistent image pull failure instead of polling for the full
// timeout. Every retry emits BackOff and ImagePullBackOff events; failing
// quickly keeps a genuine image problem from tripping the pathological-events
// invariant while still allowing a transient registry blip to recover.
func waitForWebServerPodReady(ctx context.Context, kc kubernetes.Interface, namespace string, timeout time.Duration) error {
	var pullFailureSince time.Time
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pods, err := kc.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "name=web-server-deploy"})
		if err != nil {
			return false, err
		}
		if len(pods.Items) == 0 {
			return false, nil
		}
		pod := pods.Items[0]
		if pod.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("pod %s failed: %s", pod.Name, pod.Status.Message)
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.State.Waiting == nil {
				continue
			}
			switch status.State.Waiting.Reason {
			case "ErrImagePull", "ImagePullBackOff":
				if pullFailureSince.IsZero() {
					pullFailureSince = time.Now()
				}
				if time.Since(pullFailureSince) >= 30*time.Second {
					return false, fmt.Errorf("pod %s image pull failed (%s): %s", pod.Name, status.State.Waiting.Reason, status.State.Waiting.Message)
				}
				return false, nil
			}
		}
		pullFailureSince = time.Time{}
		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady {
				return condition.Status == corev1.ConditionTrue, nil
			}
		}
		return false, nil
	})
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

func DeleteSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string) error {
	if err := kc.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete secret %s/%s: %w", namespace, name, err)
	}
	return nil
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

func DeleteConfigMap(ctx context.Context, kc kubernetes.Interface, namespace, name string) error {
	if err := kc.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete configmap %s/%s: %w", namespace, name, err)
	}
	return nil
}

// --- Operator log helpers ---

func GetOperatorLogs(ctx context.Context, kc kubernetes.Interface, tailLines int64, sinceTime ...metav1.Time) (string, error) {
	const selector = "name=ingress-operator"
	pods, err := kc.CoreV1().Pods(OperatorNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		// An absent namespace, a denied request, and a throttled or dropped
		// connection are different problems with different fixes.  Reporting
		// them all as "no pod found" is what made the HyperShift failures in
		// OCPBUGS-132070 read as a missing operator rather than as the guest
		// cluster simply not hosting one.
		switch {
		case apierrors.IsNotFound(err):
			return "", fmt.Errorf("namespace %s not found while listing ingress-operator pods (expected on a HyperShift guest cluster, where the operator runs on the management cluster): %w", OperatorNamespace, err)
		case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
			return "", fmt.Errorf("not permitted to list pods in %s with selector %q: %w", OperatorNamespace, selector, err)
		default:
			return "", fmt.Errorf("failed to list pods in %s with selector %q: %w", OperatorNamespace, selector, err)
		}
	}
	podName := ""
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			podName = pod.Name
			break
		}
	}
	if podName == "" {
		return "", fmt.Errorf("no active pod matched selector %q in namespace %s", selector, OperatorNamespace)
	}
	opts := &corev1.PodLogOptions{
		Container: "ingress-operator",
		TailLines: &tailLines,
	}
	if len(sinceTime) > 0 {
		opts.SinceTime = &sinceTime[0]
	}
	result := kc.CoreV1().Pods(OperatorNamespace).GetLogs(podName, opts)
	logBytes, err := result.Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	return string(logBytes), nil
}

// --- ClusterOperator helpers ---

// clusterOperatorConditions renders the three conditions WaitForClusterOperatorNormal
// cares about, so a timeout says which one was still wrong instead of just "deadline
// exceeded".
func clusterOperatorConditions(co *unstructured.Unstructured) string {
	if co == nil {
		return "clusteroperator not observed"
	}
	conditions, found, _ := unstructured.NestedSlice(co.Object, "status", "conditions")
	if !found {
		return "no status.conditions reported"
	}
	avail, prog, deg := "Unknown", "Unknown", "Unknown"
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		status, _ := cm["status"].(string)
		switch cm["type"] {
		case "Available":
			avail = status
		case "Progressing":
			prog = status
		case "Degraded":
			deg = status
		}
	}
	return fmt.Sprintf("Available=%s Progressing=%s Degraded=%s", avail, prog, deg)
}

func WaitForClusterOperatorNormal(ctx context.Context, dc dynamic.Interface, name string, timeout time.Duration) error {
	started := time.Now()
	var last *unstructured.Unstructured
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		co, err := dc.Resource(ClusterOperatorGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			// A missing clusteroperator is unexpected but not what this wait is
			// for; keep the error for the timeout message without failing the
			// poll early on a single transient read error.
			if !apierrors.IsNotFound(err) {
				lastErr = err
			}
			return false, nil
		}
		last, lastErr = co, nil
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
	if err != nil {
		if lastErr != nil {
			return fmt.Errorf("clusteroperator/%s not Available/non-Progressing/non-Degraded after %s: %s: %w",
				name, time.Since(started).Round(time.Second), clusterOperatorConditions(last), lastErr)
		}
		return fmt.Errorf("clusteroperator/%s not Available/non-Progressing/non-Degraded after %s: %s",
			name, time.Since(started).Round(time.Second), clusterOperatorConditions(last))
	}
	return nil
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

// --- LB Security Groups helpers ---

const (
	awsLBSecurityGroupsAnnotation        = "service.beta.kubernetes.io/aws-load-balancer-security-groups"
	autoDeleteLoadBalancerAnnotation     = "ingress.operator.openshift.io/auto-delete-load-balancer"
	loadBalancerProgressingConditionType = "LoadBalancerProgressing"
)

func joinSecurityGroups(securityGroups []operatorv1.SecurityGroupID, sep string) string {
	var buffer bytes.Buffer
	first := true
	for _, sg := range securityGroups {
		if len(string(sg)) == 0 {
			continue
		}
		if !first {
			buffer.WriteString(sep)
		}
		first = false
		buffer.WriteString(string(sg))
	}
	return buffer.String()
}

func getStatusSecurityGroups(ic *operatorv1.IngressController) []operatorv1.SecurityGroupID {
	eps := ic.Status.EndpointPublishingStrategy
	if eps != nil && eps.LoadBalancer != nil && eps.LoadBalancer.ProviderParameters != nil &&
		eps.LoadBalancer.ProviderParameters.AWS != nil &&
		eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters != nil {
		return eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups
	}
	return nil
}

func getSpecSecurityGroups(ic *operatorv1.IngressController) []operatorv1.SecurityGroupID {
	eps := ic.Spec.EndpointPublishingStrategy
	if eps != nil && eps.LoadBalancer != nil && eps.LoadBalancer.ProviderParameters != nil &&
		eps.LoadBalancer.ProviderParameters.AWS != nil &&
		eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters != nil {
		return eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups
	}
	return nil
}

func (c *clients) waitForStatusMatchesSpec(ctx context.Context, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := c.client.Get(ctx, crclient.ObjectKey{Namespace: OperatorNamespace, Name: name}, ic); err != nil {
			return false, nil
		}
		return securityGroupsEqual(getSpecSecurityGroups(ic), getStatusSecurityGroups(ic)), nil
	})
}

// waitForSecurityGroupsConverged polls until the IngressController has observed its
// current generation and its spec and status both report the expected security groups.
func (c *clients) waitForSecurityGroupsConverged(ctx context.Context, name string, expected []operatorv1.SecurityGroupID, timeout time.Duration) error {
	var latestGetErr error
	pollErr := wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		latestGetErr = c.client.Get(ctx, crclient.ObjectKey{Namespace: OperatorNamespace, Name: name}, ic)
		if latestGetErr != nil {
			return false, nil
		}
		return securityGroupsConverged(ic, expected), nil
	})
	if pollErr != nil && latestGetErr != nil {
		return fmt.Errorf("polling for ingresscontroller %s/%s security groups: %w (latest read error: %w)", OperatorNamespace, name, pollErr, latestGetErr)
	}
	return pollErr
}

// securityGroupsConverged reports whether the IngressController has observed its
// current generation and its spec and status both report the expected security groups.
func securityGroupsConverged(ic *operatorv1.IngressController, expected []operatorv1.SecurityGroupID) bool {
	return ic.Generation == ic.Status.ObservedGeneration &&
		securityGroupsEqual(expected, getSpecSecurityGroups(ic)) &&
		securityGroupsEqual(expected, getStatusSecurityGroups(ic))
}

func securityGroupsEqual(a, b []operatorv1.SecurityGroupID) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[operatorv1.SecurityGroupID]int, len(a))
	for _, sg := range a {
		counts[sg]++
	}
	for _, sg := range b {
		counts[sg]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

func (c *clients) updateSecurityGroups(ctx context.Context, name string, securityGroups []operatorv1.SecurityGroupID, autoDelete bool, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := c.client.Get(ctx, crclient.ObjectKey{Namespace: OperatorNamespace, Name: name}, ic); err != nil {
			return false, nil
		}
		eps := ic.Spec.EndpointPublishingStrategy
		if eps == nil || eps.LoadBalancer == nil || eps.LoadBalancer.ProviderParameters == nil || eps.LoadBalancer.ProviderParameters.AWS == nil {
			return false, fmt.Errorf("ingresscontroller %s is not configured with AWS load balancer provider parameters", name)
		}
		awsParams := eps.LoadBalancer.ProviderParameters.AWS
		if awsParams.NetworkLoadBalancerParameters == nil {
			awsParams.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{}
		}
		awsParams.NetworkLoadBalancerParameters.SecurityGroups = securityGroups
		if autoDelete {
			if ic.Annotations == nil {
				ic.Annotations = map[string]string{}
			}
			ic.Annotations[autoDeleteLoadBalancerAnnotation] = ""
		}
		if err := c.client.Update(ctx, ic); err != nil {
			if apierrors.IsConflict(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

func (c *clients) setServiceSecurityGroupsAnnotation(ctx context.Context, icName, value string, timeout time.Duration) error {
	key := lbServiceName(icName)
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := c.client.Get(ctx, key, svc); err != nil {
			return false, nil
		}
		if svc.Annotations == nil {
			svc.Annotations = map[string]string{}
		}
		svc.Annotations[awsLBSecurityGroupsAnnotation] = value
		if err := c.client.Update(ctx, svc); err != nil {
			if apierrors.IsConflict(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// --- Cleanup and certificate helpers ---

// CleanupIC deletes an IngressController and reports a cleanup failure to w.
func CleanupIC(ctx context.Context, dc dynamic.Interface, name string, w io.Writer) {
	if err := DeleteIC(ctx, dc, name); err != nil {
		_, _ = fmt.Fprintf(w, "failed to clean up ingresscontroller %s: %v\n", name, err)
	}
}

func CleanupICWithTimeout(dc dynamic.Interface, name string, w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), LongTimeout)
	defer cancel()
	CleanupIC(ctx, dc, name, w)
}

// CleanupTestNamespace deletes a namespace, waits for it to disappear, and
// reports a cleanup failure to w. Waiting matters because a namespace can
// remain in Terminating state after the API accepts the delete request.
func CleanupTestNamespace(ctx context.Context, kc kubernetes.Interface, name string, w io.Writer) {
	if err := DeleteTestNamespace(ctx, kc, name); err != nil {
		_, _ = fmt.Fprintf(w, "failed to clean up namespace %s: %v\n", name, err)
		return
	}
	if err := wait.PollUntilContextTimeout(ctx, PollInterval, LongTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := kc.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}); err != nil {
		_, _ = fmt.Fprintf(w, "failed to wait for namespace %s to be removed: %v\n", name, err)
	}
}

// CleanupSecret deletes a secret and reports a cleanup failure to w.
func CleanupSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string, w io.Writer) {
	if err := DeleteSecret(ctx, kc, namespace, name); err != nil {
		_, _ = fmt.Fprintf(w, "failed to clean up secret %s/%s: %v\n", namespace, name, err)
	}
}

// CleanupConfigMap deletes a configmap and reports a cleanup failure to w.
func CleanupConfigMap(ctx context.Context, kc kubernetes.Interface, namespace, name string, w io.Writer) {
	if err := DeleteConfigMap(ctx, kc, namespace, name); err != nil {
		_, _ = fmt.Fprintf(w, "failed to clean up configmap %s/%s: %v\n", namespace, name, err)
	}
}

// MakeTestCAAndCertificate creates a short-lived test CA and a leaf
// certificate signed by it. The returned PEM slices are the CA certificate,
// the leaf certificate, and the leaf private key.
func MakeTestCAAndCertificate(commonName string, hosts ...string) ([]byte, []byte, []byte, error) {
	caPEM, _, certPEM, keyPEM, err := MakeTestCAAndCertificateWithKey(commonName, hosts...)
	return caPEM, certPEM, keyPEM, err
}

// MakeTestCAAndCertificateWithKey creates a short-lived test CA and a leaf
// certificate signed by it. Unlike MakeTestCAAndCertificate, it also returns
// the CA private key so callers can construct a valid TLS secret whose
// certificate is the CA.
func MakeTestCAAndCertificateWithKey(commonName string, hosts ...string) ([]byte, []byte, []byte, []byte, error) {
	caKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qe-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(cryptorand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	leafKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hosts,
	}
	leafDER, err := x509.CreateCertificate(cryptorand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	return caPEM, caKeyPEM, certPEM, keyPEM, nil
}

// ComponentRouteEntry returns the spec.componentRoutes entry for the given
// route name and namespace, or nil when there is none.
func ComponentRouteEntry(componentRoutes []interface{}, namespace, name string) interface{} {
	for _, entry := range componentRoutes {
		route, ok := entry.(map[string]interface{})
		if ok && route["name"] == name && route["namespace"] == namespace {
			return entry
		}
	}
	return nil
}

// WithComponentRoute returns componentRoutes with testEntry in place of any
// entry for the same route name and namespace.
func WithComponentRoute(componentRoutes []interface{}, namespace, name string, testEntry interface{}) []interface{} {
	updated := make([]interface{}, 0, len(componentRoutes)+1)
	for _, entry := range componentRoutes {
		route, ok := entry.(map[string]interface{})
		if ok && route["name"] == name && route["namespace"] == namespace {
			continue
		}
		updated = append(updated, entry)
	}
	return append(updated, testEntry)
}

// componentRouteMatchesTestEntry reports whether entry still matches the
// componentRoutes entry a test added. Fields the API server defaults to an
// empty value are ignored; any other field added or changed by another writer
// makes the entry no longer match.
func componentRouteMatchesTestEntry(entry, testEntry interface{}) bool {
	entryMap, ok := entry.(map[string]interface{})
	if !ok {
		return false
	}
	testMap, ok := testEntry.(map[string]interface{})
	if !ok {
		return false
	}
	for key, want := range testMap {
		if !reflect.DeepEqual(entryMap[key], want) {
			return false
		}
	}
	for key, got := range entryMap {
		if _, ok := testMap[key]; ok {
			continue
		}
		if !isEmptyComponentRouteValue(got) {
			return false
		}
	}
	return true
}

// isEmptyComponentRouteValue reports whether value is a zero value that the
// API server can add to a componentRoutes entry.
func isEmptyComponentRouteValue(value interface{}) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case map[string]interface{}:
		for _, nested := range v {
			if !isEmptyComponentRouteValue(nested) {
				return false
			}
		}
		return true
	case []interface{}:
		return len(v) == 0
	default:
		return false
	}
}

// WithoutComponentRoute returns componentRoutes without the test-owned entry
// for the given route name and namespace. An entry that another writer changed
// is preserved instead. The entry that existed before the test replaced it is
// appended only when the test-owned entry was removed.
func WithoutComponentRoute(componentRoutes []interface{}, namespace, name string, testEntry, original interface{}) []interface{} {
	restored := make([]interface{}, 0, len(componentRoutes))
	removed := false
	for _, entry := range componentRoutes {
		route, ok := entry.(map[string]interface{})
		if ok && route["name"] == name && route["namespace"] == namespace && componentRouteMatchesTestEntry(entry, testEntry) {
			removed = true
			continue
		}
		restored = append(restored, entry)
	}
	if removed && original != nil {
		restored = append(restored, original)
	}
	return restored
}

func stringValues(values []string) []interface{} {
	converted := make([]interface{}, 0, len(values))
	for _, value := range values {
		converted = append(converted, value)
	}
	return converted
}

// --- Cloud load balancer helpers ---

func SkipUnlessPlatform(ctx context.Context, dc dynamic.Interface, platform string) {
	actual, err := GetClusterPlatform(ctx, dc)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get cluster platform")
	if actual != platform {
		g.Skip(fmt.Sprintf("test skipped on platform %q", actual))
	}
}

func WaitForDefaultEndpointPublishingStrategy(ctx context.Context, dc dynamic.Interface) (string, error) {
	var strategyType string
	err := wait.PollUntilContextTimeout(ctx, PollInterval, DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		ic, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Get(ctx, "default", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		strategyType = NestedString(ic.Object, "status", "endpointPublishingStrategy", "type")
		return strategyType != "", nil
	})
	return strategyType, err
}

func DefaultICHasExplicitEndpointPublishingStrategy(ctx context.Context, dc dynamic.Interface) (bool, error) {
	ic, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get default ingresscontroller: %w", err)
	}
	strategyType, found, err := unstructured.NestedString(ic.Object, "spec", "endpointPublishingStrategy", "type")
	if err != nil {
		return false, fmt.Errorf("failed to read default ingresscontroller endpointPublishingStrategy: %w", err)
	}
	return found && strategyType != "", nil
}

func WaitForICConditions(ctx context.Context, dc dynamic.Interface, name string, want map[string]string, timeout time.Duration) error {
	started := time.Now()
	var last map[string]string
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		ic, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		conditions, _, err := unstructured.NestedSlice(ic.Object, "status", "conditions")
		if err != nil {
			return false, fmt.Errorf("failed to decode status.conditions for ingresscontroller %s: %w", name, err)
		}
		last = map[string]string{}
		for _, condition := range conditions {
			conditionMap, ok := condition.(map[string]interface{})
			if !ok {
				continue
			}
			last[fmt.Sprint(conditionMap["type"])] = fmt.Sprint(conditionMap["status"])
		}
		for conditionType, status := range want {
			if last[conditionType] != status {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("ingresscontroller %s did not reach conditions %v after %s; last conditions were %v: %w", name, want, time.Since(started).Round(time.Second), last, err)
	}
	return nil
}

func WaitForICProgressingMessage(ctx context.Context, dc dynamic.Interface, name string) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		ic, err := dc.Resource(IngressControllerGVR).Namespace(OperatorNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		conditions, _, err := unstructured.NestedSlice(ic.Object, "status", "conditions")
		if err != nil {
			return false, fmt.Errorf("failed to decode status.conditions for ingresscontroller %s: %w", name, err)
		}
		for _, condition := range conditions {
			conditionMap, ok := condition.(map[string]interface{})
			if !ok || fmt.Sprint(conditionMap["type"]) != "Progressing" {
				continue
			}
			return fmt.Sprint(conditionMap["status"]) == "True" &&
				strings.Contains(fmt.Sprint(conditionMap["message"]), "To effectuate this change, you must delete the service"), nil
		}
		return false, nil
	})
}

func WaitForLBProvisioned(ctx context.Context, kc kubernetes.Interface, icName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := getLBIngress(ctx, kc, icName)
		return err == nil, nil
	})
}

func getLBIngress(ctx context.Context, kc kubernetes.Interface, icName string) (string, error) {
	service, err := kc.CoreV1().Services(IngressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			return ingress.Hostname, nil
		}
		if ingress.IP != "" {
			return ingress.IP, nil
		}
	}
	return "", fmt.Errorf("load balancer service %s/router-%s has no ingress hostname or IP", IngressNamespace, icName)
}

func GetLBHostname(ctx context.Context, kc kubernetes.Interface, icName string) (string, error) {
	var hostname string
	err := wait.PollUntilContextTimeout(ctx, PollInterval, LongTimeout, true, func(ctx context.Context) (bool, error) {
		value, err := getLBIngress(ctx, kc, icName)
		if err != nil {
			return false, nil
		}
		hostname = value
		return strings.Contains(value, ".elb.") && strings.HasSuffix(value, ".amazonaws.com"), nil
	})
	return hostname, err
}

func WaitForLBAnnotation(ctx context.Context, kc kubernetes.Interface, icName, key string, wantExist bool, wantValue string, timeout time.Duration) error {
	started := time.Now()
	var last string
	var found bool
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		service, err := kc.CoreV1().Services(IngressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		last, found = service.Annotations[key]
		if !wantExist {
			return !found, nil
		}
		return found && last == wantValue, nil
	})
	if err != nil {
		state := fmt.Sprintf("value %q (present=%t)", last, found)
		return fmt.Errorf("load balancer service annotation %s did not converge after %s; last %s", key, time.Since(started).Round(time.Second), state)
	}
	return nil
}

func DeleteLBService(ctx context.Context, kc kubernetes.Interface, icName string) error {
	service, err := kc.CoreV1().Services(IngressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get load balancer service: %w", err)
	}
	oldUID := service.UID
	if err := kc.CoreV1().Services(IngressNamespace).Delete(ctx, service.Name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("failed to delete load balancer service: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, PollInterval, LongTimeout, true, func(ctx context.Context) (bool, error) {
		recreated, err := kc.CoreV1().Services(IngressNamespace).Get(ctx, service.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return recreated.UID != oldUID, nil
	})
}

func CleanupICAndWaitForLBService(ctx context.Context, dc dynamic.Interface, kc kubernetes.Interface, name string, w io.Writer) {
	if err := DeleteIC(ctx, dc, name); err != nil {
		_, _ = fmt.Fprintf(w, "failed to clean up ingresscontroller %s: %v\n", name, err)
	}
	if err := wait.PollUntilContextTimeout(ctx, PollInterval, LongTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := kc.CoreV1().Services(IngressNamespace).Get(ctx, "router-"+name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	}); err != nil {
		_, _ = fmt.Fprintf(w, "failed to wait for load balancer service router-%s to be removed: %v\n", name, err)
	}
}

func serviceEventMessages(ctx context.Context, kc kubernetes.Interface, icName string) (string, error) {
	service, err := kc.CoreV1().Services(IngressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get load balancer service for events: %w", err)
	}
	selector := fields.Set{
		"involvedObject.name": service.Name,
		"involvedObject.uid":  string(service.UID),
	}.AsSelector().String()
	events, err := kc.CoreV1().Events(IngressNamespace).List(ctx, metav1.ListOptions{
		FieldSelector: selector,
	})
	if err != nil {
		return "", err
	}
	messages := make([]string, 0, len(events.Items))
	for _, event := range events.Items {
		if event.InvolvedObject.Kind == "Service" {
			messages = append(messages, event.Message)
		}
	}
	return strings.Join(messages, "\n"), nil
}

func WaitForServiceEvent(ctx context.Context, kc kubernetes.Interface, icName, substring string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 3*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		messages, err := serviceEventMessages(ctx, kc, icName)
		if err != nil {
			return false, nil
		}
		return strings.Contains(messages, substring), nil
	})
}

func WaitForServiceEventPattern(ctx context.Context, kc kubernetes.Interface, icName string, pattern *regexp.Regexp, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 3*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		messages, err := serviceEventMessages(ctx, kc, icName)
		if err != nil {
			return false, nil
		}
		return pattern.MatchString(messages), nil
	})
}

func AzureMaximumSourceRanges(count int) []string {
	cidrs := make([]string, 0, count)
	for secondOctet := 1; secondOctet <= 32 && len(cidrs) < count; secondOctet++ {
		for lastOctet := 2; lastOctet <= 254 && len(cidrs) < count; lastOctet += 2 {
			cidrs = append(cidrs, fmt.Sprintf("1.1.%d.%d/32", secondOctet, lastOctet))
		}
	}
	return cidrs
}

func PublicSubnetNames(ctx context.Context, dc dynamic.Interface) ([]string, error) {
	machineSets, err := dc.Resource(MachineSetGVR).Namespace("openshift-machine-api").List(ctx, metav1.ListOptions{})
	if err == nil && len(machineSets.Items) > 0 {
		return publicSubnetNamesFromMAPI(machineSets)
	}

	capiGVR := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta2", Resource: "machinesets"}
	capiMachineSets, err := dc.Resource(capiGVR).Namespace("openshift-cluster-api").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list MachineSets: %w", err)
	}
	if len(capiMachineSets.Items) == 0 {
		return nil, fmt.Errorf("no MachineSets found")
	}

	infra, err := dc.Resource(InfrastructureGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get infrastructure config: %w", err)
	}
	infrastructureName := NestedString(infra.Object, "status", "infrastructureName")
	if infrastructureName == "" {
		return nil, fmt.Errorf("infrastructure name not found")
	}

	names := make([]string, 0, len(capiMachineSets.Items))
	seen := map[string]bool{}
	for _, machineSet := range capiMachineSets.Items {
		availabilityZone, found, err := unstructured.NestedString(machineSet.Object, "spec", "template", "spec", "failureDomain")
		if err != nil || !found || availabilityZone == "" {
			return nil, fmt.Errorf("machineset %q has no failureDomain", machineSet.GetName())
		}
		name := infrastructureName + "-subnet-public-" + availabilityZone
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

func publicSubnetNamesFromMAPI(machineSets *unstructured.UnstructuredList) ([]string, error) {
	names := make([]string, 0, len(machineSets.Items))
	seen := map[string]bool{}
	for _, machineSet := range machineSets.Items {
		subnets, err := machineSetSubnets(machineSet)
		if err != nil {
			return nil, err
		}
		for _, subnet := range subnets {
			if !strings.Contains(subnet, "subnet-private") || !privateSubnetPattern.MatchString(subnet) {
				continue
			}
			publicSubnet := strings.Replace(subnet, "subnet-private", "subnet-public", 1)
			if !seen[publicSubnet] {
				seen[publicSubnet] = true
				names = append(names, publicSubnet)
			}
		}
	}
	return names, nil
}

func machineSetSubnets(machineSet unstructured.Unstructured) ([]string, error) {
	subnet, found, err := unstructured.NestedString(machineSet.Object, "spec", "template", "spec", "providerSpec", "value", "subnet", "id")
	if err != nil {
		return nil, fmt.Errorf("failed to decode subnet.id for machineset %s: %w", machineSet.GetName(), err)
	}
	if found && subnet != "" {
		return []string{subnet}, nil
	}

	// Supported MAPI schemas use either a list of filter strings or a list of
	// objects with a values field. Decode the generic list first so an object
	// element is not rejected before the object form can be handled.
	filters, found, err := unstructured.NestedSlice(machineSet.Object, "spec", "template", "spec", "providerSpec", "value", "subnet", "filters")
	if err != nil {
		return nil, fmt.Errorf("failed to decode subnet.filters for machineset %s: %w", machineSet.GetName(), err)
	}
	if !found {
		return nil, nil
	}
	values := make([]string, 0, len(filters))
	for i, filter := range filters {
		switch value := filter.(type) {
		case string:
			values = append(values, value)
		case map[string]interface{}:
			filterValues, _, err := unstructured.NestedStringSlice(value, "values")
			if err != nil {
				return nil, fmt.Errorf("failed to decode subnet filter values for machineset %s: %w", machineSet.GetName(), err)
			}
			values = append(values, filterValues...)
		default:
			return nil, fmt.Errorf("unsupported subnet filter type %T at index %d for machineset %s", filter, i, machineSet.GetName())
		}
	}
	return values, nil
}

func AllocateElasticIPs(ctx context.Context, client *ec2.Client, count int) ([]string, error) {
	allocationIDs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		result, err := client.AllocateAddress(ctx, &ec2.AllocateAddressInput{
			Domain: ec2types.DomainTypeVpc,
		})
		if err != nil {
			allocationErr := fmt.Errorf("failed to allocate Elastic IP %d of %d: %w", i+1, count, err)
			rollbackErr := ReleaseElasticIPs(ctx, client, allocationIDs, io.Discard)
			if rollbackErr != nil {
				return allocationIDs, errors.Join(allocationErr, fmt.Errorf("failed to release partially allocated Elastic IPs: %w", rollbackErr))
			}
			return nil, allocationErr
		}
		allocationIDs = append(allocationIDs, aws.ToString(result.AllocationId))
	}
	return allocationIDs, nil
}

func ReleaseElasticIPs(ctx context.Context, client *ec2.Client, allocationIDs []string, w io.Writer) error {
	var releaseErrors []error
	for _, allocationID := range allocationIDs {
		err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			_, err := client.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: aws.String(allocationID)})
			if err == nil || isAWSErrorCode(err, "InvalidAllocationID.NotFound") {
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			releaseErr := fmt.Errorf("failed to release Elastic IP %s: %w", allocationID, err)
			_, _ = fmt.Fprintln(w, releaseErr)
			releaseErrors = append(releaseErrors, releaseErr)
		}
	}
	return errors.Join(releaseErrors...)
}

func QuoteJoin(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return strings.Join(quoted, ",")
}
