package test

import (
	"context"
	"fmt"
	"reflect"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
)

// getDefaultIngressDomain returns the default ingress domain
// (e.g., "apps.mycluster.example.com"). Uses AdminConfigClient.
func getDefaultIngressDomain(ctx context.Context, oc *CLI) (string, error) {
	var domain string
	err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		ingress, err := oc.AdminConfigClient().ConfigV1().Ingresses().Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(g.GinkgoWriter, "Get ingresses.config/cluster failed: %v, retrying...\n", err)
			return false, nil
		}
		domain = ingress.Spec.Domain
		return true, nil
	})
	return domain, err
}

// shardConfig holds configuration for deploying a router shard.
type shardConfig struct {
	// Domain is the domain for the IngressController to host.
	Domain string
	// Type is the namespace selector match label value.
	Type string
	// LoadBalancer optionally specifies the load balancer strategy.
	LoadBalancer *operatorv1.LoadBalancerStrategy
}

// deployNewRouterShard creates a new IngressController and waits for it to
// become available with all expected conditions met. Uses AdminOperatorClient.
func deployNewRouterShard(ctx context.Context, oc *CLI, timeout time.Duration, cfg shardConfig) (*operatorv1.IngressController, error) {
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Type,
			Namespace: "openshift-ingress-operator",
			Annotations: map[string]string{
				"ingress.operator.openshift.io/default-enable-http2": "true",
			},
		},
		Spec: operatorv1.IngressControllerSpec{
			Replicas: ptr.To[int32](1),
			Domain:   cfg.Domain,
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type:         operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: cfg.LoadBalancer,
			},
			NodePlacement: &operatorv1.NodePlacement{
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"node-role.kubernetes.io/worker": "",
					},
				},
			},
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"type": cfg.Type,
				},
			},
		},
	}

	operatorCl := oc.AdminOperatorClient()
	_, err := operatorCl.OperatorV1().IngressControllers(ic.Namespace).Create(ctx, ic, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	expectedConditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
		{Type: "Admitted", Status: operatorv1.ConditionTrue},
	}
	err = waitForIngressControllerCondition(ctx, oc, timeout, types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}, expectedConditions...)
	return ic, err
}

// waitForIngressControllerCondition waits until the IngressController has
// all the expected conditions. Uses AdminOperatorClient.
func waitForIngressControllerCondition(ctx context.Context, oc *CLI, timeout time.Duration, name types.NamespacedName, conditions ...operatorv1.OperatorCondition) error {
	expected := operatorConditionMap(conditions...)
	return wait.PollUntilContextTimeout(ctx, 3*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		ic, err := oc.AdminOperatorClient().OperatorV1().IngressControllers(name.Namespace).Get(ctx, name.Name, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(g.GinkgoWriter, "failed to get ingresscontroller %s/%s: %v, retrying...\n", name.Namespace, name.Name, err)
			return false, nil
		}
		current := operatorConditionMap(ic.Status.Conditions...)
		met := conditionsMatchExpected(expected, current)
		if !met {
			fmt.Fprintf(g.GinkgoWriter, "ingresscontroller %s/%s conditions not met; wanted %+v, got %+v, retrying...\n", name.Namespace, name.Name, expected, current)
		}
		return met, nil
	})
}

func operatorConditionMap(conditions ...operatorv1.OperatorCondition) map[string]string {
	conds := map[string]string{}
	for _, cond := range conditions {
		conds[cond.Type] = string(cond.Status)
	}
	return conds
}

func conditionsMatchExpected(expected, actual map[string]string) bool {
	filtered := map[string]string{}
	for k := range actual {
		if _, comparable := expected[k]; comparable {
			filtered[k] = actual[k]
		}
	}
	return reflect.DeepEqual(expected, filtered)
}

// createEdgeRoute creates an edge-terminated TLS route targeting the
// given service on port 8080. The shardType label must match the
// IngressController's namespace selector. Uses the regular user's
// RouteClient; the route is created in oc.Namespace().
func createEdgeRoute(ctx context.Context, oc *CLI, name, host, serviceName, shardType string) {
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"type": shardType,
			},
		},
		Spec: routev1.RouteSpec{
			Host: host,
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromInt32(8080),
			},
			TLS: &routev1.TLSConfig{
				Termination:                   routev1.TLSTerminationEdge,
				InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
			},
			To: routev1.RouteTargetReference{
				Kind:   "Service",
				Name:   serviceName,
				Weight: ptr.To[int32](100),
			},
			WildcardPolicy: routev1.WildcardPolicyNone,
		},
	}
	_, err := oc.RouteClient().RouteV1().Routes(oc.Namespace()).Create(ctx, route, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())
}

// waitForRouteAdmitted waits until the route is admitted by the ingress
// controller for the given host. Uses the regular user's RouteClient.
func waitForRouteAdmitted(ctx context.Context, oc *CLI, name, host string, timeout time.Duration) {
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		r, err := oc.RouteClient().RouteV1().Routes(oc.Namespace()).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(g.GinkgoWriter, "failed to get route: %v, retrying...\n", err)
			return false, nil
		}
		for _, ingress := range r.Status.Ingress {
			if ingress.Host == host {
				for _, condition := range ingress.Conditions {
					if condition.Type == routev1.RouteAdmitted && condition.Status == corev1.ConditionTrue {
						return true, nil
					}
				}
			}
		}
		return false, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "route was not admitted")
}

// dumpRouterPodLogs dumps recent logs from the router deployment for the
// given shard. This is a best-effort debug helper used in AfterEach.
func dumpRouterPodLogs(oc *CLI, shardName string) {
	output, err := oc.AsAdmin().Run("logs").Args("deployment/router-"+shardName,
		"-n", "openshift-ingress", "--all-containers", "--tail=50").Output()
	if err != nil {
		fmt.Fprintf(g.GinkgoWriter, "failed to get router pod logs for shard %s: %v\n", shardName, err)
		return
	}
	fmt.Fprintf(g.GinkgoWriter, "Router pod logs for shard %s:\n%s\n", shardName, output)
}
