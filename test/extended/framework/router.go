package framework

// This file was copied and adapted from openshift/origin/test/extended/util/router.go

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	kapierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

func WaitForRouterInternalIP(oc *CLI) (string, error) {
	return waitForNamedRouterServiceIP(oc, "router-internal-default")
}

func waitForRouterExternalIP(oc *CLI) (string, error) {
	return waitForNamedRouterServiceIP(oc, "router-default")
}

func routerShouldHaveExternalService(oc *CLI) (bool, error) {
	foundLoadBalancerServiceStrategyType := false
	err := wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 30*time.Second, false, func(ctx context.Context) (bool, error) {
		ic, err := oc.AdminOperatorClient().OperatorV1().IngressControllers("openshift-ingress-operator").Get(ctx, "default", metav1.GetOptions{})
		// Previously this utils would keep trying until the service is found, and then
		// if there was an error different of not found, it would fail
		// For the sake of resiliency of the suite, we are not always failing if err != nil,
		// we will log a message and retry
		if err != nil {
			WarnContextf("an error happened when trying to get the ingresscontroller, retrying...; %s", err)
			return false, nil
		}

		if ic.Status.EndpointPublishingStrategy == nil {
			return false, nil
		}
		if ic.Status.EndpointPublishingStrategy.Type == "LoadBalancerService" {
			foundLoadBalancerServiceStrategyType = true
		}
		return true, nil
	})
	return foundLoadBalancerServiceStrategyType, err
}

func WaitForRouterServiceIP(oc *CLI) (string, error) {
	if useExternal, err := routerShouldHaveExternalService(oc); err != nil {
		return "", err
	} else if useExternal {
		return waitForRouterExternalIP(oc)
	}
	return WaitForRouterInternalIP(oc)
}

func waitForNamedRouterServiceIP(oc *CLI, name string) (string, error) {
	_, ns, err := GetRouterPodTemplate(oc)
	if err != nil {
		return "", err
	}

	// wait for the service to show up
	var endpoint string
	err = wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 60*time.Second, false, func(ctx context.Context) (bool, error) {
		svc, err := oc.AdminKubeClient().CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			WarnContextf("an error happened when trying to get the services, retrying...; %s", err)
			return false, nil
		}

		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			if len(svc.Status.LoadBalancer.Ingress) != 0 {
				if len(svc.Status.LoadBalancer.Ingress[0].IP) != 0 {
					endpoint = svc.Status.LoadBalancer.Ingress[0].IP
					return true, nil
				}
				if len(svc.Status.LoadBalancer.Ingress[0].Hostname) != 0 {
					endpoint = svc.Status.LoadBalancer.Ingress[0].Hostname
					return true, nil
				}
			}
			return false, nil
		}
		endpoint = svc.Spec.ClusterIP
		return true, nil
	})
	return endpoint, err
}

// GetRouterPodTemplate finds the router pod template across different namespaces,
// helping to mitigate the transition from the default namespace to an operator
// namespace.
func GetRouterPodTemplate(oc *CLI) (*corev1.PodTemplateSpec, string, error) {
	k8sappsclient := oc.AdminKubeClient().AppsV1()
	for _, ns := range []string{"default", "openshift-ingress", "tectonic-ingress"} {
		deploy, err := k8sappsclient.Deployments(ns).Get(context.Background(), "router", metav1.GetOptions{})
		if err == nil {
			return &deploy.Spec.Template, ns, nil
		}
		if !kapierrs.IsNotFound(err) {
			return nil, "", err
		}
		deploy, err = k8sappsclient.Deployments(ns).Get(context.Background(), "router-default", metav1.GetOptions{})
		if err == nil {
			return &deploy.Spec.Template, ns, nil
		}
		if !kapierrs.IsNotFound(err) {
			return nil, "", err
		}
	}
	return nil, "", kapierrs.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "router")
}
