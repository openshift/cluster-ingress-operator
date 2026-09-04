package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	operatorNamespace      = "openshift-ingress-operator"
	ingressNamespace       = "openshift-ingress"
	pollInterval           = 5 * time.Second
	defaultTimeout         = 3 * time.Minute
	longTimeout            = 5 * time.Minute
	clusterOperatorTimeout = 2 * time.Minute
)

var (
	ingressControllerGVR = schema.GroupVersionResource{Group: "operator.openshift.io", Version: "v1", Resource: "ingresscontrollers"}
	routeGVR             = schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}
	ingressConfigGVR     = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "ingresses"}
	dnsConfigGVR         = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "dnses"}
	clusterOperatorGVR   = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusteroperators"}
	infrastructureGVR    = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "infrastructures"}
	dnsRecordGVR         = schema.GroupVersionResource{Group: "ingress.operator.openshift.io", Version: "v1", Resource: "dnsrecords"}
	credentialsReqGVR    = schema.GroupVersionResource{Group: "cloudcredential.openshift.io", Version: "v1", Resource: "credentialsrequests"}
	servicemonitorGVR    = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
)

// --- IngressController helpers ---

type icOption func(map[string]interface{})

func withNodePort() icOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "NodePortService",
			"nodePort": map[string]interface{}{
				"protocol": "TCP",
			},
		}
	}
}

func withPrivateLB() icOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "LoadBalancerService",
			"loadBalancer": map[string]interface{}{
				"scope": "Internal",
			},
		}
	}
}

func withExternalLB() icOption {
	return func(spec map[string]interface{}) {
		spec["endpointPublishingStrategy"] = map[string]interface{}{
			"type": "LoadBalancerService",
			"loadBalancer": map[string]interface{}{
				"scope": "External",
			},
		}
	}
}

func withCLB() icOption {
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

func withHostNetwork(httpPort, httpsPort, statsPort int64) icOption {
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

func withDefaultCertificate(secretName string) icOption {
	return func(spec map[string]interface{}) {
		spec["defaultCertificate"] = map[string]interface{}{
			"name": secretName,
		}
	}
}

func withClientTLS(caConfigMapName string) icOption {
	return func(spec map[string]interface{}) {
		spec["clientTLS"] = map[string]interface{}{
			"clientCA": map[string]interface{}{
				"name": caConfigMapName,
			},
			"clientCertificatePolicy": "Required",
		}
	}
}

func createIC(ctx context.Context, dc dynamic.Interface, name, domain string, opts ...icOption) error {
	spec := map[string]interface{}{
		"replicas": int64(1),
		"domain":   domain,
	}
	for _, opt := range opts {
		opt(spec)
	}
	if _, ok := spec["endpointPublishingStrategy"]; !ok {
		withNodePort()(spec)
	}
	ic := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operator.openshift.io/v1",
			"kind":       "IngressController",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": operatorNamespace,
			},
			"spec": spec,
		},
	}
	_, err := dc.Resource(ingressControllerGVR).Namespace(operatorNamespace).Create(ctx, ic, metav1.CreateOptions{})
	return err
}

func deleteIC(ctx context.Context, dc dynamic.Interface, name string) {
	_ = dc.Resource(ingressControllerGVR).Namespace(operatorNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	_ = wait.PollUntilContextTimeout(ctx, pollInterval, longTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := dc.Resource(ingressControllerGVR).Namespace(operatorNamespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

func waitForICAvailable(ctx context.Context, dc dynamic.Interface, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		ic, err := dc.Resource(ingressControllerGVR).Namespace(operatorNamespace).Get(ctx, name, metav1.GetOptions{})
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

func waitForDeployGeneration(ctx context.Context, kc kubernetes.Interface, icName string, gen int64, timeout time.Duration) error {
	deployName := "router-" + icName
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := kc.AppsV1().Deployments(ingressNamespace).Get(ctx, deployName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return deploy.Generation == gen && deploy.Status.ObservedGeneration == gen &&
			deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas &&
			deploy.Status.AvailableReplicas == *deploy.Spec.Replicas, nil
	})
}

func getRouterDeployment(ctx context.Context, kc kubernetes.Interface, icName string) (*appsv1.Deployment, error) {
	return kc.AppsV1().Deployments(ingressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
}

func routerContainer(deploy *appsv1.Deployment) (*corev1.Container, error) {
	for i := range deploy.Spec.Template.Spec.Containers {
		if deploy.Spec.Template.Spec.Containers[i].Name == "router" {
			return &deploy.Spec.Template.Spec.Containers[i], nil
		}
	}
	return nil, fmt.Errorf("router container not found in deployment %s", deploy.Name)
}

// --- Pod helpers ---

func getRouterPodName(ctx context.Context, kc kubernetes.Interface, icName string) (string, error) {
	label := "ingresscontroller.operator.openshift.io/deployment-ingresscontroller=" + icName
	pods, err := kc.CoreV1().Pods(ingressNamespace).List(ctx, metav1.ListOptions{LabelSelector: label})
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

func getRouterPodNames(ctx context.Context, kc kubernetes.Interface, icName string) ([]string, error) {
	label := "ingresscontroller.operator.openshift.io/deployment-ingresscontroller=" + icName
	pods, err := kc.CoreV1().Pods(ingressNamespace).List(ctx, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range pods.Items {
		names = append(names, p.Name)
	}
	return names, nil
}

func waitForRouterPod(ctx context.Context, kc kubernetes.Interface, icName string, timeout time.Duration) (string, error) {
	var podName string
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		name, err := getRouterPodName(ctx, kc, icName)
		if err != nil {
			return false, nil
		}
		podName = name
		return true, nil
	})
	return podName, err
}

func waitForNewRouterPod(ctx context.Context, kc kubernetes.Interface, icName, oldPod string, timeout time.Duration) (string, error) {
	var podName string
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		name, err := getRouterPodName(ctx, kc, icName)
		if err != nil || name == oldPod {
			return false, nil
		}
		podName = name
		return true, nil
	})
	return podName, err
}

func waitForPodDisappear(ctx context.Context, kc kubernetes.Interface, namespace, podName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := kc.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

func waitForPodReady(ctx context.Context, kc kubernetes.Interface, namespace, labelSelector string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
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

func getPodsByLabel(ctx context.Context, kc kubernetes.Interface, namespace, label string) ([]corev1.Pod, error) {
	pods, err := kc.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func deleteAllPodsByLabel(ctx context.Context, kc kubernetes.Interface, namespace, label string) error {
	return kc.CoreV1().Pods(namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: label})
}

func getWorkerNodeCount(ctx context.Context, kc kubernetes.Interface) (int, error) {
	nodes, err := kc.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/worker"})
	if err != nil {
		return 0, err
	}
	return len(nodes.Items), nil
}

// --- Pod exec ---

func execInPod(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, namespace, podName, container string, command []string) (string, string, error) {
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

func execInRouterPod(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName string, cmd string) (string, error) {
	stdout, _, err := execInPod(ctx, restCfg, kc, ingressNamespace, podName, "router", []string{"bash", "-c", cmd})
	return stdout, err
}

// --- Cluster info helpers ---

func getBaseDomain(ctx context.Context, dc dynamic.Interface) (string, error) {
	dns, err := dc.Resource(dnsConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	domain, found, err := unstructured.NestedString(dns.Object, "spec", "baseDomain")
	if err != nil || !found {
		return "", fmt.Errorf("baseDomain not found in dns.config/cluster")
	}
	return domain, nil
}

func getClusterPlatform(ctx context.Context, dc dynamic.Interface) (string, error) {
	infra, err := dc.Resource(infrastructureGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	platform, _, _ := unstructured.NestedString(infra.Object, "status", "platformStatus", "type")
	return strings.ToLower(platform), nil
}

func getIngressDomain(ctx context.Context, dc dynamic.Interface) (string, error) {
	ingress, err := dc.Resource(ingressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	domain, _, _ := unstructured.NestedString(ingress.Object, "spec", "domain")
	return domain, nil
}

func getDefaultPlacement(ctx context.Context, dc dynamic.Interface) (string, error) {
	ingress, err := dc.Resource(ingressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	placement, _, _ := unstructured.NestedString(ingress.Object, "status", "defaultPlacement")
	return placement, nil
}

// --- Patch helpers ---

func patchICMerge(ctx context.Context, dc dynamic.Interface, name, patchJSON string) error {
	_, err := dc.Resource(ingressControllerGVR).Namespace(operatorNamespace).Patch(
		ctx, name, types.MergePatchType, []byte(patchJSON), metav1.PatchOptions{})
	return err
}

func patchResourceMerge(ctx context.Context, dc dynamic.Interface, gvr schema.GroupVersionResource, namespace, name, patchJSON string) error {
	_, err := dc.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patchJSON), metav1.PatchOptions{})
	return err
}

func patchClusterResourceMerge(ctx context.Context, dc dynamic.Interface, gvr schema.GroupVersionResource, name, patchJSON string) error {
	_, err := dc.Resource(gvr).Patch(
		ctx, name, types.MergePatchType, []byte(patchJSON), metav1.PatchOptions{})
	return err
}

// --- Route helpers ---

func createRoute(ctx context.Context, dc dynamic.Interface, namespace, name, serviceName, hostname string) error {
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
	_, err := dc.Resource(routeGVR).Namespace(namespace).Create(ctx, route, metav1.CreateOptions{})
	return err
}

func waitForRouteAdmittedByIC(ctx context.Context, dc dynamic.Interface, namespace, routeName, icName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(routeGVR).Namespace(namespace).Get(ctx, routeName, metav1.GetOptions{})
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

func waitForRouteNotAdmittedByIC(ctx context.Context, dc dynamic.Interface, namespace, routeName, icName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(routeGVR).Namespace(namespace).Get(ctx, routeName, metav1.GetOptions{})
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

func labelRoute(ctx context.Context, dc dynamic.Interface, namespace, name, key, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value)
	return patchResourceMerge(ctx, dc, routeGVR, namespace, name, patch)
}

// --- Namespace helpers ---

func createTestNamespace(ctx context.Context, kc kubernetes.Interface, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	_, err := kc.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

func deleteTestNamespace(ctx context.Context, kc kubernetes.Interface, name string) {
	_ = kc.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
}

func labelNamespace(ctx context.Context, kc kubernetes.Interface, name, key, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value)
	_, err := kc.CoreV1().Namespaces().Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// --- Workload helpers ---

func createWebServerPodAndService(ctx context.Context, kc kubernetes.Interface, namespace string) error {
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

func createTLSSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string, certPEM, keyPEM []byte) error {
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

func deleteSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string) {
	_ = kc.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func createConfigMap(ctx context.Context, kc kubernetes.Interface, namespace, name string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	_, err := kc.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	return err
}

func deleteConfigMap(ctx context.Context, kc kubernetes.Interface, namespace, name string) {
	_ = kc.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// --- Operator log helpers ---

func getOperatorLogs(ctx context.Context, kc kubernetes.Interface, tailLines int64) (string, error) {
	pods, err := kc.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{LabelSelector: "name=ingress-operator"})
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
	result := kc.CoreV1().Pods(operatorNamespace).GetLogs(podName, opts)
	logBytes, err := result.Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	return string(logBytes), nil
}

// --- ClusterOperator helpers ---

func waitForClusterOperatorNormal(ctx context.Context, dc dynamic.Interface, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		co, err := dc.Resource(clusterOperatorGVR).Get(ctx, name, metav1.GetOptions{})
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

func nestedString(obj map[string]interface{}, fields ...string) string {
	val, _, _ := unstructured.NestedString(obj, fields...)
	return val
}

// --- JSON helpers ---

func toJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
