package manifests

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"
	"github.com/openshift/cluster-ingress-operator/pkg/util"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
)

func installConfig() *util.InstallConfig {
	return &util.InstallConfig{
		BaseDomain: "apps.ingress.test",
		Platform: util.InstallConfigPlatform{
			AWS: &util.InstallConfigPlatformAWS{
				Region: "northsouth-not-eastwest",
			},
		},
	}
}

func TestManifests(t *testing.T) {
	config := operatorconfig.Config{RouterImage: "quay.io/openshift/router:latest"}
	f := NewFactory(config, installConfig())

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

	if _, err := f.RouterNamespace(); err != nil {
		t.Errorf("invalid RouterNamespace: %v", err)
	}

	if _, err := f.RouterServiceAccount(); err != nil {
		t.Errorf("invalid RouterServiceAccount: %v", err)
	}

	if _, err := f.RouterClusterRole(); err != nil {
		t.Errorf("invalid RouterClusterRole: %v", err)
	}

	if _, err := f.RouterClusterRoleBinding(); err != nil {
		t.Errorf("invalid RouterClusterRoleBinding: %v", err)
	}

	deployment, err := f.RouterDeployment(ci)
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

	if deployment.Spec.Template.Spec.Volumes[0].Secret == nil {
		t.Error("router Deployment has no secret volume")
	}

	defaultSecretName := fmt.Sprintf("router-certs-%s", ci.Name)
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != defaultSecretName {
		t.Errorf("router Deployment expected volume with secret %s, got %s",
			defaultSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	if svc, err := f.RouterServiceInternal(ci); err != nil {
		t.Errorf("invalid RouterServiceInternal: %v", err)
	} else if svc.Annotations[ServingCertSecretAnnotation] != defaultSecretName {
		t.Errorf("RouterServiceInternal expected serving secret annotation %s, got %s",
			defaultSecretName, svc.Annotations[ServingCertSecretAnnotation])
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
	deployment, err = f.RouterDeployment(ci)
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
	deployment, err = f.RouterDeployment(ci)
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
	if e, a := config.RouterImage, deployment.Spec.Template.Spec.Containers[0].Image; e != a {
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

	if svc, err := f.RouterServiceInternal(ci); err != nil {
		t.Errorf("invalid RouterServiceInternal: %v", err)
	} else if svc.Annotations[ServingCertSecretAnnotation] != defaultSecretName {
		t.Errorf("RouterServiceInternal expected serving secret annotation %s, got %s",
			defaultSecretName, svc.Annotations[ServingCertSecretAnnotation])
	}

	if _, err := f.RouterServiceCloud(ci); err != nil {
		t.Errorf("invalid RouterServiceCloud: %v", err)
	}
}

func TestDefaultClusterIngress(t *testing.T) {
	ingressDomain := "user.cluster.openshift.com"

	def, err := NewFactory(operatorconfig.Config{
		RouterImage:          "test",
		DefaultIngressDomain: ingressDomain,
		Platform:             configv1.NonePlatform,
	}, installConfig()).DefaultClusterIngress()
	if err != nil {
		t.Fatal(err)
	}
	if e, a := ingressDomain, *def.Spec.IngressDomain; e != a {
		t.Errorf("expected default clusteringress ingressDomain=%s, got %s", e, a)
	}
	if highAvailability := def.Spec.HighAvailability; highAvailability == nil {
		t.Error("expected default clusteringress highAvailability to be non-nil")
	}
	if e, a := ingressv1alpha1.UserDefinedClusterIngressHA, def.Spec.HighAvailability.Type; e != a {
		t.Errorf("expected default clusteringress highAvailability.type=%s, got %s", e, a)
	}

	def, err = NewFactory(operatorconfig.Config{
		RouterImage:          "test",
		DefaultIngressDomain: ingressDomain,
		Platform:             configv1.AWSPlatform,
	}, installConfig()).DefaultClusterIngress()
	if err != nil {
		t.Fatal(err)
	}
	if highAvailability := def.Spec.HighAvailability; highAvailability == nil {
		t.Error("expected default clusteringress highAvailability to be non-nil")
	}
	if e, a := ingressv1alpha1.CloudClusterIngressHA, def.Spec.HighAvailability.Type; e != a {
		t.Errorf("expected default clusteringress highAvailability.type=%s, got %s", e, a)
	}
}
