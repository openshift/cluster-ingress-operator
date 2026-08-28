package test

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

// Constants inlined from the cluster-ingress-operator to avoid importing the
// operator's internal packages (which are not part of this test module).
const (
	// icNamespace is the namespace where IngressControllers live.
	icNamespace = "openshift-ingress-operator"
	// operandNamespace is the namespace where the LoadBalancer-type Services live.
	operandNamespace = "openshift-ingress"
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
	return crclient.ObjectKey{Namespace: operandNamespace, Name: "router-" + icName}
}

// newNLBIngressController builds an external NLB IngressController with the given security groups.
func newNLBIngressController(name, domain string, securityGroups []operatorv1.SecurityGroupID) *operatorv1.IngressController {
	var nlbParams *operatorv1.AWSNetworkLoadBalancerParameters
	if len(securityGroups) > 0 {
		nlbParams = &operatorv1.AWSNetworkLoadBalancerParameters{SecurityGroups: securityGroups}
	}
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{Namespace: icNamespace, Name: name},
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
		if err := c.client.Get(ctx, crclient.ObjectKey{Namespace: icNamespace, Name: name}, ic); err != nil {
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
	ic := &operatorv1.IngressController{ObjectMeta: metav1.ObjectMeta{Namespace: icNamespace, Name: name}}
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
