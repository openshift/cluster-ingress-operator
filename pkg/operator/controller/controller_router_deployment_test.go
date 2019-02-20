package controller

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
)

func TestDesiredRouterDeployment(t *testing.T) {
	ci := &ingressv1alpha1.ClusterIngress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: ingressv1alpha1.ClusterIngressSpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Replicas: 1,
			RouteSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"baz": "quux",
				},
			},
		},
	}
	routerImage := "quay.io/openshift/router:latest"
	infraConfig := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			Platform: configv1.AWSPlatform,
		},
	}

	deployment, err := desiredRouterDeployment(ci, routerImage, infraConfig)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}

	namespaceSelector := ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "NAMESPACE_LABELS" {
			namespaceSelector = envVar.Value
			break
		}
	}
	if namespaceSelector == "" {
		t.Error("router Deployment has no namespace selector")
	} else if namespaceSelector != "foo=bar" {
		t.Errorf("router Deployment has unexpected namespace selectors: %v",
			namespaceSelector)
	}

	routeSelector := ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTE_LABELS" {
			routeSelector = envVar.Value
			break
		}
	}
	if routeSelector == "" {
		t.Error("router Deployment has no route selector")
	} else if routeSelector != "baz=quux" {
		t.Errorf("router Deployment has unexpected route selectors: %v",
			routeSelector)
	}

	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	}
	if *deployment.Spec.Replicas != 1 {
		t.Errorf("expected replicas to be 1, got %d", *deployment.Spec.Replicas)
	}

	if len(deployment.Spec.Template.Spec.NodeSelector) == 0 {
		t.Error("router Deployment has no default node selector")
	}

	proxyProtocolEnabled := false
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_USE_PROXY_PROTOCOL" {
			if v, err := strconv.ParseBool(envVar.Value); err == nil {
				proxyProtocolEnabled = v
			}
			break
		}
	}
	if proxyProtocolEnabled {
		t.Errorf("router Deployment unexpected proxy protocol")
	}

	canonicalHostname := ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_CANONICAL_HOSTNAME" {
			canonicalHostname = envVar.Value
			break
		}
	}
	if canonicalHostname != "" {
		t.Errorf("router Deployment has unexpected canonical hostname: %q", canonicalHostname)
	}

	if deployment.Spec.Template.Spec.Volumes[0].Secret == nil {
		t.Error("router Deployment has no secret volume")
	}

	defaultSecretName := fmt.Sprintf("router-certs-%s", ci.Name)
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != defaultSecretName {
		t.Errorf("router Deployment expected volume with secret %s, got %s",
			defaultSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}

	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	ci.Spec.HighAvailability = &ingressv1alpha1.ClusterIngressHighAvailability{
		Type: ingressv1alpha1.CloudClusterIngressHA,
	}
	ci.Status.IngressDomain = "example.com"
	deployment, err = desiredRouterDeployment(ci, routerImage, infraConfig)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}
	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	proxyProtocolEnabled = false
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_USE_PROXY_PROTOCOL" {
			if v, err := strconv.ParseBool(envVar.Value); err == nil {
				proxyProtocolEnabled = v
			}
			break
		}
	}
	if !proxyProtocolEnabled {
		t.Errorf("router Deployment expected proxy protocol")
	}

	canonicalHostname = ""
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == "ROUTER_CANONICAL_HOSTNAME" {
			canonicalHostname = envVar.Value
			break
		}
	}
	if canonicalHostname == "" {
		t.Error("router Deployment has no canonical hostname")
	} else if canonicalHostname != ci.Status.IngressDomain {
		t.Errorf("router Deployment has unexpected canonical hostname: %q, expected %q", canonicalHostname, ci.Status.IngressDomain)
	}

	secretName := fmt.Sprintf("secret-%v", time.Now().UnixNano())
	ci.Spec.DefaultCertificateSecret = &secretName
	ci.Spec.HighAvailability.Type = ingressv1alpha1.UserDefinedClusterIngressHA
	ci.Spec.NodePlacement = &ingressv1alpha1.NodePlacement{
		NodeSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"xyzzy": "quux",
			},
		},
	}
	ci.Spec.Replicas = 3
	deployment, err = desiredRouterDeployment(ci, routerImage, infraConfig)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	if len(deployment.Spec.Template.Spec.NodeSelector) != 1 ||
		deployment.Spec.Template.Spec.NodeSelector["xyzzy"] != "quux" {
		t.Errorf("router Deployment has unexpected node selector: %#v",
			deployment.Spec.Template.Spec.NodeSelector)
	}
	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	}
	if *deployment.Spec.Replicas != 3 {
		t.Errorf("expected replicas to be 3, got %d", *deployment.Spec.Replicas)
	}
	if e, a := routerImage, deployment.Spec.Template.Spec.Containers[0].Image; e != a {
		t.Errorf("expected router Deployment image %q, got %q", e, a)
	}

	if deployment.Spec.Template.Spec.HostNetwork != true {
		t.Error("expected host network to be true")
	}
	if deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host != "localhost" {
		t.Errorf("expected liveness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host != "localhost" {
		t.Errorf("expected liveness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	if deployment.Spec.Template.Spec.Volumes[0].Secret == nil {
		t.Error("router Deployment has no secret volume")
	}
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != secretName {
		t.Errorf("expected router Deployment volume with secret %s, got %s",
			secretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}
}
